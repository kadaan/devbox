# Plan: Replace NixHub with a Self-Hosted nxv Index

> Source RFC: `docs/development/rfcs/0001-replace-nixhub-with-self-hosted-nxv.md`

## Architectural Decisions

Durable decisions that apply across all phases. These come from the RFC's Decisions table; the
*why* is carried across because several look arbitrary without it.

- **Proxy home and language**: Go, in this fork, at `cmd/devbox-search-proxy/`. It imports
  `go.jetify.com/devbox/internal/searcher` for the response types, so the wire JSON cannot drift
  from what Devbox parses — the structs are literally the same ones. This is only possible
  in-module: `internal/` is not importable across modules, so a separate repo would require
  hand-mirrored structs and make shape drift a live failure mode.
- **Integration point**: `DEVBOX_SEARCH_HOST` only (`internal/searcher/client.go:34`). No other
  Devbox change is required for the proxy to take effect, which is why the translation lives
  outside the fork's own resolution path rather than inside `internal/searcher`.
- **Endpoints served** (all four, Tier 3): `/v2/search`, `/v2/pkg`, `/v2/resolve`, `/v1/resolve`.
- **Upstream**: nxv's HTTP API (`/api/v1/*`), co-located with the proxy so `/v2/search`'s N+1
  fan-out to `/packages/{attr}` stays loopback-local.
- **Name→attr resolution**: exact attr match first, then mechanical family suffixes, accepting
  only candidates that verify as an exact `(attribute_path, version)` row. **No pname fallback** —
  dropping it deletes the entire ambiguity failure class rather than policing it.
- **Pinned rev**: `first_commit_hash`, with `first_commit_date` emitted as `last_modified`.
  Chosen for determinism: it is frozen once recorded, so a pin resolves identically regardless of
  who resolves it or when. `last_commit_hash` moves with the index head for any currently-current
  version.
- **Constraint semantics**: `@latest` and prefixes select the version whose range extends to the
  index head (`last_commit_hash == last_indexed_commit`), not the greatest version string. Nix
  `compareVersions` is the tiebreak and the fallback for superseded versions, never the primary
  selector.
- **Store paths**: never computed by the proxy. Closed client-side by patching
  `FixMissingStorePaths` to loop over configured systems.
- **Index lifecycle**: bootstrap with `nxv sync`, then incremental self-index. `--full` is
  forbidden on a bootstrapped database.

## Out of Scope

Carried forward from the RFC so no phase widens into it:

- Pointing Devbox at `search.nixos.org`. It holds one snapshot per channel and no version→commit
  history, so it cannot serve resolution at all.
- Computing store paths, output hashes, or NAR presence in the proxy. This needs a full nixpkgs
  eval per (package, version, system) and an eval farm, and cross-system `outPath` eval breaks on
  packages with platform-gated asserts or impure eval.
- Reproducing NixHub's version-string normalization in general. The one measured divergence
  (`jdk@25.0.0` vs nixpkgs' `25+36`) is handled as a documented expected failure, not a
  normalization layer.
- Upstreaming devbox-compat endpoints into `utensils/nxv`. Considered and rejected — it puts the
  freshness path back behind another maintainer's PR queue, which is the dependency being left.
- Running the pre-2020 `--backfill-evals` ingest (~1.5–3h). The bootstrap skips it.
- Migrating the estate's existing lockfiles. They never consult the service
  (`lock/lockfile.go:77-80`), so there is nothing to migrate.

## Testing Approach

The RFC's acceptance suite is **the real estate, not synthetic fixtures**: 146 versioned pins
extracted from 199 `devbox.lock` files. Phase 2 commits that corpus as testdata and every
resolution phase afterwards asserts against it.

Two entries in the corpus are **expected failures**, asserted as such so nobody chases them:

- `jdk@25.0.0` — NixHub reports temurin's `25.0.0`; nixpkgs' `jdk25` calls the same build
  `25+36`. The version exists under `javaPackages.compiler.temurin-bin.jdk-25`. A pairing
  mismatch, not missing data.
- `python@3.9.9` — a genuine index hole. `python39` runs 3.9.0–3.9.4 then jumps to 3.9.13.
  Phase 7 was expected to close this by ingesting the `nixos-*` stable channels; that was tested
  and **disproved**. Ingesting nixos-21.11 over 2021-11-29..2021-12-19, exactly the window when 3.9.9 was current, added 88 rows but no `python3` or `python39` row at all, so the expectation stands.

Two pins resolve to a *different but equivalent* attr than NixHub chose (`go@1.22.1`→`go`,
`nodejs@24.12.0`→`nodejs`, because Tier A is tried first and the unversioned attr held that exact
version at some commit). Same package, same version, different rev. Assert the version, not the
attr, for those two.

Tests must not depend on a live nxv instance. Record nxv responses for the corpus as fixtures and
serve them from an `httptest` server; keep at most one build-tagged test that hits a real nxv.

---

## Phase 1: Multi-system store paths

**Covers**: RFC decision 12. Independent of the proxy work in phases 2–6; sequenced first so the
store-path fix lands before any new service exists.

### What to build

The fork patch that lets one developer populate store paths for every configured platform.
`SetOutputsForPackage` (`internal/lock/lockfile.go:234`) currently writes only
`p.Systems[nix.System()]`, so `devbox install --tidy-lockfile` fills the local platform and
leaves the rest to be filled one machine at a time. Store paths are a pure function of
(rev, attr, system) and computable at eval time, so `FixMissingStorePaths`
(`internal/devbox/packages.go:717`) loops the project's configured systems and evaluates each.

Per-system failure must be tolerated rather than aborting the whole run — packages with
platform-gated asserts or impure eval will fail for some systems, and the current code returns the
first error. A system that cannot be evaluated is skipped, leaving the existing slow path intact
for it.

This changes lockfile *contents* but not lockfile *semantics*: `resolved` and `version` are
already platform-independent and are not touched.

Because this is the first phase, it also carries the repo hygiene fix that unblocks the check
gate. `devbox run lint` exits 1 on a clean tree at HEAD, on two pre-existing goimports violations
in `internal/boxcli/search.go` and `internal/searcher/client.go` from commit `580595ce`. Whichever
phase runs first must clear these or its check gate fails before any work is reviewed.

### Acceptance criteria

- [x] `devbox run lint` exits 0 on a clean tree
- [x] `devbox install --tidy-lockfile` populates `systems.<sys>.outputs` for every configured
      system, not only the host's
- [x] A system whose evaluation fails is skipped, other systems still populate, and the command
      exits 0
- [x] Store paths written for the host system are byte-identical to those the pre-patch code wrote
- [x] A lockfile with no `systems` entries still resolves and installs, exercising the
      `Installables()` → `urlForInstall()` fallback
- [x] `resolved` and `version` are unchanged by a tidy run
- [x] Existing lockfile tests pass unchanged

---

## Phase 2: Tracer bullet — exact-version resolve end-to-end

**Covers**: RFC decisions 5, 9. Establishes the wire contract.

### What to build

A new `cmd/devbox-search-proxy` that serves `GET /v2/resolve?name=&version=` for **exact version
pins only**, translating from nxv's `/api/v1/packages/{attr}/versions/{version}`. The name is
treated as an attribute path verbatim in this phase — no candidate generation yet.

Responses are constructed as `internal/searcher.ResolveResponse` values and marshalled, so the
shape is defined by the same struct Devbox unmarshals into. The pinned rev is
`first_commit_hash`, `last_modified` is `first_commit_date`, and `systems` is populated per
platform with empty `outputs`.

End-to-end verification means Devbox's own client, not a hand-rolled HTTP assertion: point
`searcher.Client()` at the proxy via `DEVBOX_SEARCH_HOST` and assert the returned
`ResolveResponse`. Then confirm a real `devbox add <pin>` in a temporary project produces a
lockfile whose `resolved` and `version` match.

The 146-pin corpus lands as testdata in this phase, along with recorded nxv fixtures for it.

### Acceptance criteria

- [x] The proxy serves `/v2/resolve` for an exact pin and Devbox's `searcher.Client()` parses the
      response without error
- [x] `resolved` is `github:NixOS/nixpkgs/<first_commit_hash>#<attr>` and `last_modified` is
      derived from `first_commit_date`
- [x] A `devbox add` against the proxy writes a lockfile entry whose `resolved` and `version`
      match what the proxy returned
- [x] The 146-pin corpus and its nxv fixtures are committed as testdata
- [x] Every exact pin in the corpus whose devbox name already equals its attr resolves
- [x] A pin absent from the index returns 404 and surfaces as `searcher.ErrNotFound`
- [x] Tests pass with no live nxv reachable

---

## Phase 3: Name→attr resolution

**Covers**: RFC decisions 7, 8.

### What to build

Candidate-based name→attr resolution in the proxy's `/v2/resolve` path. Tier A yields the name
itself; Tier B yields mechanical family suffixes derived from the requested version's leading
numeric components (`python`+`3.12`→`python312`, `nodejs`+`18`→`nodejs_18` and `nodejs-18_x`,
`jdk`+`8`→`jdk8`, `go`+`1.22`→`go_1_22`, `zulu`+`8`→`zulu8`, `ruby`+`3.2`→`ruby_3_2`). Candidates
are tried in order and one is accepted only when the exact `(attr, version)` row exists.

There is deliberately **no pname fallback**. Attr-first already covers the cases whose pname
differs from their attr (`icu`/`icu4c`, `maven`/`apache-maven`, `mysql80`/`mysql`) and the ones
that look ambiguous under pname lookup but are exact under attr lookup (`clang`, `pandoc`,
`pkg-config`, `binutils`).

When no candidate verifies, return 404.

### Acceptance criteria

- [x] 142 of the 144 resolvable corpus pins resolve to an attr
- [x] 140 resolve to the identical attr NixHub chose, asserted per-pin rather than in aggregate
- [x] `go@1.22.1` and `nodejs@24.12.0` resolve to the correct *version*; their attr is asserted as
      either the NixHub attr or the unversioned attr
- [x] `jdk@25.0.0` and `python@3.9.9` are asserted as expected failures with their documented causes
- [x] Both `nodejs-18_x` and `nodejs_18` era pins resolve, proving the suffix conventions cover
      the nixpkgs naming change
- [x] No candidate is ever returned without an exact index row backing it
- [x] No code path performs a pname lookup

The first two counts are corpus-wide. 7 of the 146 pins carry a prefix or `latest` constraint that
Phase 4 resolves, so Phase 3's suite covers the 139 exact pins: 137 resolve, 135 on NixHub's own
attr, 2 on the equivalent unversioned attr, and the 2 expected failures are the only misses. The
attr half of the remaining 7 is already covered — NixHub's attr is among the candidates for all
146 pins — so Phase 4 adds only the version choice.

---

## Phase 4: Constraint resolution

**Covers**: RFC decisions 10, 11.

### What to build

`@latest` and prefix constraints (`N`, `N.N`) in `/v2/resolve`. Candidates are filtered by
prefix, then the one whose `last_commit_hash` equals the index's `last_indexed_commit` wins.
Where nothing sits at head — normal for a superseded version — fall back to the greatest version
under Nix's `compareVersions` algorithm.

That algorithm is implemented here: split on non-alphanumerics and at digit/alpha boundaries,
compare numeric components numerically, sort `pre` below everything, and treat a missing
component as lower than a numeric one. `golang.org/x/mod/semver` cannot be used — 27 of the
corpus's 141 distinct versions are not strict semver.

Head-presence is the primary selector specifically to avoid reproducing `nix-env -u`'s worst
behaviour: for `go-bindata`, Nix ranks the 2016 string `20151023-a0ff256` above `4.0.2` from 2023
because the first component compares `20151023 > 4`.

### Acceptance criteria

- [x] `@latest` resolves to the version at index head for every `@latest` pin in the corpus
- [x] `go-bindata@latest` resolves to `4.0.2`, not `20151023-a0ff256`
- [x] Prefix pins resolve within their prefix: `python@3.12` to a `3.12.x`, `nodejs@24` to a `24.x`
- [x] All 27 non-semver version strings in the corpus order correctly under `compareVersions`,
      asserted as explicit pairwise cases
- [x] `jdk8@latest` resolves to the newest `8uNNN-bNN`, proving build-suffix ordering
- [x] A prerelease at head is not selected over a stable release also at head
- [x] The head-presence path is exercised without a live index by pinning
      `last_indexed_commit` in fixtures

---

## Phase 5: Discovery endpoints

**Covers**: RFC decisions 3, 6.

### What to build

`GET /v2/search?q=` and `GET /v2/pkg?name=`, so `devbox search` works end-to-end.

`/v2/search` maps nxv's `/api/v1/search` and emits nxv's `attribute_path` in the `name` field.
`/v2/pkg` is only ever called with a `name` taken from our own `/v2/search` response
(`internal/searcher/client.go:57`), so it receives an attr and does a direct keyed lookup against
`/api/v1/packages/{attr}` — no name mapping on this path at all. nxv returns every version for
the attr in one call, and its `platforms` field populates the search table's platform column.

Verification is `devbox search <term>` rendering a populated table through the proxy.

### Acceptance criteria

- [x] `devbox search <term>` renders a table of results with versions and platforms
- [x] `/v2/search` emits `attribute_path` as `name`, and feeding any such name straight back into
      `/v2/pkg` succeeds
- [x] `/v2/pkg` returns every release nxv holds for the attr, with per-platform entries
- [x] Devbox's `Search` (`internal/searcher/client.go`) parses both responses without error
- [x] A query with no matches yields Devbox's "No results found" output rather than an error
- [x] Platform strings are normalized to Nix system identifiers as Devbox's mapping expects
      (`arm64`→`aarch64`, `macos`→`darwin`)

---

## Phase 6: Legacy resolve endpoint

**Covers**: RFC decision 3.

### What to build

`GET /v1/resolve?name=&version=`, returning the older `searcher.PackageVersion` shape rather than
`ResolveResponse`, so `devbox search <pkg>@<ver>` (`internal/boxcli/search.go:48`) and
`devbox info` (`internal/devbox/devbox.go:410`) work against the proxy. It reuses the Phase 3 and
4 resolution logic; only the serialization differs.

`devbox info` tolerates a not-found by falling through, so 404 must remain distinguishable from a
server error on this path.

### Acceptance criteria

- [x] `devbox search <pkg>@<ver>` prints the `resolves to: <name>@<version>` line
- [x] `devbox info <pkg>@<ver>` renders package information
- [x] The response carries `commit_hash` and `attr_paths` per system as the legacy shape requires
- [x] The same pin resolves to the same rev and version through `/v1/resolve` and `/v2/resolve`
- [x] A missing pin returns 404 and `devbox info` falls through instead of erroring

---

## Phase 7: Index operations

**Covers**: RFC decisions 13, 14, 15, 16.

### What to build

The operational surface that keeps the index fresh and detectably healthy.

Ingest configuration adds `nixos-*` stable channels alongside nxv's defaults
(`nixpkgs-unstable`, `nixos-unstable-small`). They do **not** close the `python@3.9.9` hole --
ingesting nixos-21.11 over the window when 3.9.9 was current added 88 rows but no `python3` or
`python39` row at all -- and they were subsequently removed: their releases fail nxv's date-keyed
snapshot gates, and each failure retries on backoff and reports the index unhealthy.

A refresh wrapper runs the **default incremental** `nxv index` and **refuses `--full`** or any
other re-planning flag. This guard is not cosmetic: run against a bootstrapped database, `--full`
re-queued all 8,505 known releases as pending and, having ingested only the capped few, left the
watermark deriving from a 2020 release at `head lag: 56410h`, `status: UNHEALTHY`. Package data
survived, but 4,185 of the re-queued releases are pre-2020 and unreachable without
`--backfill-evals`, so the watermark can never re-settle. A genuine rebuild goes into a fresh
database and is swapped in.

A health check parses `nxv index --report` JSON and fails on `status != healthy` or on head lag
beyond a threshold. Refresh cadence is the lag, and lag is what this whole effort is about.

### Acceptance criteria

- [x] The refresh wrapper runs incremental `nxv index` and exits non-zero if handed `--full`
      or another re-planning flag
- [x] The health check fails on a report fixture with `status: UNHEALTHY`
- [x] The health check fails on a report fixture whose head lag exceeds the threshold
- [x] The health check passes on a healthy fixture with `head lag: 7h`
- [x] Ingest configuration covers nxv's unstable channels; the `nixos-*` stable branches were
      trialled, measured, and removed
- [x] Tested whether the stable channels make `python@3.9.9` resolve. They do not; the pin stays
      an expected failure, with the measurement recorded as its documented cause
- [x] The runbook records that `--full` is forbidden on a bootstrapped index and why
- [x] Health-check tests run against fixtures, with no live nxv or index required

---

```html
<!-- workflow
{
  "name": "run-replace-nixhub-with-self-hosted-nxv-tdd",
  "description": "Implement plan 0001 (replace NixHub with a self-hosted nxv index behind a devbox-compat proxy) phase-by-phase with TDD, one commit per phase",
  "gates": {
    "check": "devbox run lint",
    "test": "devbox run test",
    "focused": "devbox run -- go test -race"
  },
  "houseRules": ["CONTRIBUTING.md"],
  "newFilesToCommit": [
    "docs/development/plans/0001-replace-nixhub-with-self-hosted-nxv.md",
    "docs/development/rfcs/0001-replace-nixhub-with-self-hosted-nxv.md"
  ],
  "phases": [
    { "n": 1, "kind": "implementation", "title": "Phase 1", "name": "Multi-system store paths", "detail": "Fork patch: FixMissingStorePaths loops configured systems with per-system failure tolerance instead of only nix.System(); also clears the pre-existing goimports failures blocking the check gate" },
    { "n": 2, "kind": "implementation", "title": "Phase 2", "name": "Tracer bullet: exact-version resolve end-to-end", "detail": "cmd/devbox-search-proxy serving /v2/resolve for exact pins via nxv; first_commit_hash pinning; 146-pin corpus as testdata" },
    { "n": 3, "kind": "implementation", "title": "Phase 3", "name": "Name to attr resolution", "detail": "Attr-first plus mechanical family suffixes, index-verified, no pname fallback; golden test over the full pin corpus" },
    { "n": 4, "kind": "implementation", "title": "Phase 4", "name": "Constraint resolution", "detail": "@latest and prefix constraints via index-head presence, with a Nix compareVersions implementation as tiebreak and fallback" },
    { "n": 5, "kind": "implementation", "title": "Phase 5", "name": "Discovery endpoints", "detail": "/v2/search and /v2/pkg emitting attribute_path as name; devbox search renders end-to-end" },
    { "n": 6, "kind": "implementation", "title": "Phase 6", "name": "Legacy resolve endpoint", "detail": "/v1/resolve in the older PackageVersion shape for devbox search pkg@ver and devbox info" },
    { "n": 7, "kind": "implementation", "title": "Phase 7", "name": "Index operations", "detail": "Stable-channel ingest config, incremental refresh wrapper refusing --full, health check parsing nxv index --report JSON" },
    { "n": 8, "kind": "review", "title": "Review", "name": "Deep review", "detail": "Deep review of all phases, fix findings, commit" }
  ]
}
-->
```
