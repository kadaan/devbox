# RFC: Survive a Nix Garbage Collection

## Problem

After `nix store gc`, a Devbox project can keep a `.devbox` directory that describes an
environment the store no longer has. The symptom is a dead directory on `PATH`: a tool that
should be there reports `command not found`, with nothing to connect that to the garbage
collection. Devbox does not notice and does not recover, so the remedy people converge on is
`rm -rf .devbox`, which throws away correct state along with the stale part.

Nix itself does not have this problem, for two reasons that Devbox only half has:

- **GC roots.** `nix develop` roots the dev shell's closure, so a collection cannot take it.
- **Re-realisation.** Nix re-evaluates on every invocation, so anything missing is rebuilt.

Devbox caches to avoid Nix's evaluation cost, which is the right call for startup latency. The
defect is that the cache has no validity condition.

## What was measured

All figures below are from this machine, not estimates. The reproduction used a controlled
`hello` project; store loss was simulated by repointing symlinks and doctoring the cache, never
by running a real collection.

`internal/lock/lockfile.go:IsUpToDateAndInstalled` gates every command that enters an
environment. It checked exactly three things: the lockfile is not dirty, and two content hashes
(`nix_profile_manifest_hash`, `nix_print_dev_env_hash`) from `internal/lock/statehash.go`.

**None of them consult the store.** They hash the bytes of `manifest.json` and
`.devbox/.nix-print-dev-env-cache`. Deleting the store paths those files *name* does not change
either file, so the hashes still match, so Devbox reports "up to date" and skips the recompute.

Four candidate failure modes were tested. Two are not bugs:

| Failure mode | Detected before this change? | Recovered? |
|---|---|---|
| Profile store path collected | Yes | Yes |
| Store paths in generated `flake.nix` | n/a, all inside the rooted profile closure | n/a |
| Plugin virtenv files | Yes | Yes |
| **Store paths named only by the dev-env cache** | **No** | **No** |

- **Profile loss already recovers.** `cachehash.JSONFile` returns `("", nil)` for a missing file,
  so an unreadable `manifest.json` yields an empty hash, which mismatches and forces a recompute.
  Verified: repointing `default-1-link` at an absent path made Devbox rebuild the profile and
  succeed. 60 of 152 projects on this machine are already in this state; they look broken but
  self-heal on next use.
- **Plugin files are not affected.** No symlink under any `.devbox/virtenv` points into the store.
  They indirect through `.devbox/nix/profile/default/bin/...`, and `pyvenv.cfg`'s `home` does
  too, so the rooted profile covers them. A dangling link also fails the plugin's `[ -f ]` test,
  which recreates the venv. The `is_devbox_venv` check is not a workaround for collection.
- **The dev-env cache is the bug.** 47 of the 49 store paths it names sit outside the profile's
  closure -- stdenv setup hooks, `gnutar`, `findutils`, `diffutils`, `gzip`. Nothing roots them.
  With the cache doctored to name one collected path and `state.json` updated so all three hashes
  still agree, Devbox recomputed nothing, warned about nothing, and put
  `/nix/store/zzzz...-collected/bin` straight into `PATH`, `HOST_PATH` and `DEVBOX_NIX_ENV_PATH_*`.

## Decisions

- **Root the dev environment, do not just recover from losing it.** `nix print-dev-env` accepts
  `--profile`, which is how `nix-direnv` roots a dev shell. Measured: it moved all 47 unrooted
  paths to rooted, 100% coverage, and Nix confirms the result as a real GC root. It costs no extra
  invocation because it is a flag on a call Devbox already makes. Prevention beats recovery here
  because recovery still makes the user wait for a rebuild.
- **Separate profile for the dev environment** (`.devbox/nix/devenv-profile`), not the package
  profile. The two hold different things; the package profile deliberately does not carry stdenv
  tooling. Verified that generations do not accumulate: repeated recomputes leave exactly one
  generation link, matching the existing package profile's behaviour.
- **Detect with `stat`, confirm with Nix.** The check gates every command, so the common case must
  be cheap. `stat` per path costs **0.06 ms** for 49 paths against a ~230 ms warm invocation
  (0.03%). `nix-store --check-validity` would be authoritative but costs **~100 ms**, i.e. 40% of
  a warm run. So: `stat` everything, and only when something looks absent ask Nix, whose answer
  cannot be fooled by a path this code parsed wrongly.
- **Ambiguity resolves to "fresh".** If Nix cannot answer, report not-collected. A false "stale"
  recomputes on every command; a false "fresh" is no worse than the behaviour being replaced.
- **Two independent mechanisms, deliberately.** The freshness gate reports staleness so the user
  sees the existing "Ensuring packages are installed." message instead of an unexplained pause
  (`PrintDevEnv`'s internal recompute shows no spinner, because the spinner is tied to
  `usePrintDevEnvCache`). `PrintDevEnv` separately discards an invalid cache, which is what
  actually rebuilds, and which covers every caller including those that bypass the gate.

## The parsing hazard

Extracting store paths from the cache is the riskiest part, because a path that is named but
cannot exist makes the environment *permanently* stale -- a recompute on every command. Two
concrete traps, both found in real cache files and both covered by tests:

- **Unresolved-output placeholders.** `print-dev-env` emits
  `/nix/store/eeeeeeee...eeee-libsbuf-14.1.0-dev` for outputs it has not resolved. These never
  exist. Nix's base-32 alphabet omits `e`, `o`, `t` and `u`, so a strict hash class excludes them
  for free -- the exclusion is load-bearing, not cosmetic.
- **`=` as a delimiter.** `-fmacro-prefix-map=<old>=<new>` puts `=` directly after a store path,
  yielding `...-libsbuf-14.1.0-dev=`. Nix permits `=` in a store name, so a permissive class
  absorbs it and produces a path that does not exist. Confirmed with Nix: the bare path is valid,
  the `=`-suffixed one is not. `=` and `?` are therefore excluded from the name class, and the
  Nix confirmation step covers the residual risk of truncating a genuine name.

## Out of scope

- **Plugin-generated files.** Measured as unaffected; see above. There *is* an unrelated defect in
  `plugins/python/venvShellHook.sh` -- a failed `is_devbox_venv` runs `read reply` from a shell
  init hook, which blocks or consumes stdin non-interactively -- but it triggers on a Python
  version change, not on a collection.
- **Pruning GC roots for deleted projects.** `gcroots/auto` on this machine holds many entries
  pointing at long-gone temp directories. Nix removes such links itself when it next collects.
- **Reducing what the dev environment pulls in.** Rooting ~47 more paths per project means stale
  project directories hold more disk. Worth revisiting, separate from correctness.
- **Making the recompute converge in a single run.** `updateLockfile` writes `state.json` before
  the cache is regenerated, so a recovery run can leave the two briefly inconsistent. Measured as
  converging after exactly one recompute, so this is not worth the ordering refactor.
