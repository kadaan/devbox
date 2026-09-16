# RFC: Replace NixHub with a Self-Hosted nxv Index

## Problem

Devbox resolves versioned package pins through Jetify's hosted search service at
`https://search.devbox.sh` (NixHub). That service has been lagging nixpkgs, and the
commit and contributor activity on `jetify-com/devbox` suggests the project may become
unmaintained. Our estate depends on it: 218 `devbox.json` and 199 `devbox.lock` files,
with 146 versioned pins across 141 distinct versions.

The coupling itself is small. `internal/searcher/client.go:34` reads the host from an
environment variable:

```go
host: envir.GetValueOrDefault(envir.DevboxSearchHost, searchAPIEndpoint)
```

So `DEVBOX_SEARCH_HOST` already repoints Devbox with no code change. The problem is not
redirection — it is that something must speak NixHub's contract:

| Endpoint | Caller | Purpose |
|---|---|---|
| `GET /v2/search?q=` | `boxcli/search.go:41` | free-text discovery |
| `GET /v2/pkg?name=` | `searcher/client.go:57` (N+1 per search hit) | release + platform list |
| `GET /v2/resolve?name=&version=` | `lock/resolve.go:99` | lockfile resolution (default; `enable("RESOLVE_V2")`) |
| `GET /v1/resolve?name=&version=` | `boxcli/search.go:48`, `devbox/devbox.go:410` | `devbox search pkg@ver`, `devbox info` |

Two findings bound the urgency and the scope.

**Existing lockfiles are immune.** `lock/lockfile.go:77-80` short-circuits resolution on
any entry that already has `resolved` set:

```go
entry, hasEntry := f.Packages[pkg]
if hasEntry && entry.Resolved != "" {
    return f.Packages[pkg], nil
}
```

No network call. All 199 lockfiles keep working indefinitely with NixHub dead. Only
`devbox add <pkg>@<ver>`, `devbox update`, `devbox search`, and `devbox info` touch the
service. This is a freshness and new-pin problem, not an outage waiting to happen.

**`search.nixos.org` cannot substitute.** Beyond having no wire compatibility (it proxies
an Elasticsearch cluster with hardcoded credentials and no stability guarantee), it indexes
one snapshot per channel. It holds no version-to-commit history, so it can never answer
"which nixpkgs commit had `python` 3.10" — precisely the query `/v2/resolve` exists to serve.

## Decisions

| # | Decision |
|---|----------|
| 1 | Do not point Devbox at `search.nixos.org`. It has no version history, so it cannot serve lockfile resolution. |
| 2 | Use `utensils/nxv` as the version index. It ingests `nixpkgs-unstable` (history spine back to 2016) and `nixos-unstable-small` (currency) from `releases.nixos.org`, and returns nixpkgs commit hashes per (attr, version). |
| 3 | Implement Tier 3 — all four NixHub endpoints — so `devbox search` and `devbox info` keep working alongside resolution. |
| 4 | Run nxv in server mode. MVP: a local sidecar on one developer's machine. Target: a remote nxv server. |
| 5 | Put the translation in a **standalone proxy** co-located with nxv, not in the Devbox fork. `DEVBOX_SEARCH_HOST` stays the sole integration point. Resolution-semantics bugs are then fixed in one place rather than by rolling a forked binary to every developer and CI image, and the fork stays a thin patch set. |
| 6 | `/v2/search` emits nxv's `attribute_path` in the `name` field. `/v2/pkg` is only ever called with a `name` taken from our own `/v2/search` response (`client.go:57`), so both sides are self-consistent and need no name mapping. |
| 7 | `/v2/resolve` maps name to attr by **exact attr match first, then mechanical family suffixes**, accepting only candidates that verify as an exact `(attribute_path, version)` row in the index. |
| 8 | **No pname fallback.** Dropping it removes the entire ambiguity failure class; attr-first plus family suffixes already resolves 142 of 144 pins, 140 of them to the identical attr NixHub chose. |
| 9 | Pin `first_commit_hash`, and emit `first_commit_date` as `last_modified`. Chosen for determinism: it is frozen once recorded, so a pin resolves identically regardless of who resolves it or when. |
| 10 | Define `@latest` and prefix constraints as **"the version whose range extends to the index head"** — filter candidates, then select the one whose `last_commit_hash` equals the index's `last_indexed_commit`. |
| 11 | Implement Nix's `compareVersions` algorithm for tiebreaks and for the fallback when no candidate sits at head. It is not the primary selector. |
| 12 | Close the store-path gap with a patch to our Devbox fork: `FixMissingStorePaths` loops over the project's configured systems instead of only `nix.System()`. The proxy does not compute store paths. |
| 13 | Bootstrap the index with `nxv sync`, then self-index incrementally from that database. This skips the one-time pre-2020 `--backfill-evals` cost (~1.5–3h) while making us independent for ongoing freshness. |
| 14 | **`--full` is forbidden** on a bootstrapped index. It re-queues every known release and the pre-2020 era can never settle without `--backfill-evals`, leaving the index permanently unhealthy. |
| 15 | **Do not** ingest the `nixos-*` stable channels. This decision originally added them to close the `python@3.9.9` hole; measurement disproved that, nxv's own design notes advise against stable branches, and their gate failures flap the health signal — see Implementation Recommendations. |
| 16 | Alert on the `nxv index --report` JSON: `status != healthy`, and `head lag`. Our refresh cadence is our lag, and lag is what we left NixHub over. |

## Solution

### Topology

```
devbox (forked)  --DEVBOX_SEARCH_HOST-->  devbox-compat proxy  -->  nxv serve
                                          (4 NixHub endpoints)      (/api/v1/*)
                                                   \______ co-located ______/
```

Co-location matters: `/v2/search` fans out to one `/packages/{attr}` call per result
(`client.go:57`). Behind the proxy that N+1 is loopback-local, so a remote deployment costs
one WAN round trip per `devbox search` rather than eleven.

### Endpoint mapping

| Devbox endpoint | nxv endpoint | Notes |
|---|---|---|
| `/v2/search?q=` | `/api/v1/search?q=` | one row per attr; carries name, description, `platforms` |
| `/v2/pkg?name=<attr>` | `/api/v1/packages/{attr}` | returns **all** versions for the attr in one call |
| `/v2/resolve`, `/v1/resolve` | `/api/v1/packages/{attr}/versions/{version}` | exact lookup only; constraint logic lives in the proxy |

nxv performs no constraint resolution. Everything in decisions 7–11 is proxy-side logic.

### Name-to-attr resolution

```
resolve(name, version):
    for candidate in candidates(name, version):
        if index.has(candidate, version):        # exact (attr, version) row
            return candidate
    return 404

candidates(name, version):
    yield name                                   # Tier A: attr == devbox name
    maj, min = leading numeric components of version
    yield name + {"", "_", "-"} x {maj, maj+min, maj+"_"+min}   # Tier B: family suffixes
    yield name + "-" + maj + "_x"                # legacy nixpkgs convention
```

Tier A covers the plain majority, including cases whose *pname* differs from their attr
(`icu`/`icu4c`, `maven`/`apache-maven`, `mysql80`/`mysql`) and the ones that look ambiguous
under pname lookup but are exact under attr lookup (`clang`, `pandoc`, `pkg-config`,
`binutils`).

Tier B covers the versioned families, where the attr depends on which nixpkgs era the
version lived in — note both conventions appear in our own estate:

```
nodejs -> nodejs-12_x, nodejs_16, nodejs-18_x, nodejs_18, nodejs-19_x, nodejs_22, nodejs_24, nodejs_25
python -> python3, python310, python312, python313, python314
jdk    -> jdk8, jdk17, jdk25          go -> go_1_22, go_1_24, go_1_26
zulu   -> zulu8, zulu17               ruby -> ruby_3_2
```

Verification against the index is what makes candidate generation safe: a guess is never
accepted unless that exact `(attr, version)` row exists.

### Constraint resolution

```
select(candidates):
    at_head = [v for v in candidates if v.last_commit_hash == index.last_indexed_commit]
    if at_head: return max(at_head, key=nix_compare)
    return max(candidates, key=nix_compare)      # fallback for superseded versions
```

Head-presence rather than version-string maximum, because a faithful Nix `compareVersions`
maximum reproduces `nix-env -u`'s worst behaviour. Measured example from our own pin set:
`go-bindata` has versions `20151023-a0ff256` (2016) and `4.0.2` (2023), and Nix ranks the
2016 string higher because the first component compares `20151023 > 4`. Head-presence picks
`4.0.2`. The same rule is immune to backport inversion, where a security backport of an
older version lands at a newer commit.

Nix `compareVersions` is still required — 27 of our 141 distinct versions (19%) are not
strict semver (`8u462-b08`, `17.0.11+9`, `20240122`, `2025-11-05`, `20260107.1`, `11076708`,
`76.1`, `3.7.0.2`, `0.08`) and `golang.org/x/mod/semver`, Devbox's only version helper
(`internal/vercheck/vercheck.go:288`, `nix/nix.go:295`), rejects every one.

Scope is modest: of 176 lockfile pins, 150 are exact, 19 are `N.N`, 4 are `N`, 3 are
`latest`. Only 15% touch constraint resolution.

### Store paths (the fork patch)

`resolved` and `version` are top-level, single-valued, platform-independent fields
(`lock/package.go:16-25`). One nixpkgs rev plus one attr path pins the build identically on
every platform — that *is* the pin. `systems[<sys>]` holds only `outputs` and `store_path`,
which are a binary-cache lookup cache. Verified fallback chain when they are absent:

```
missing systems entry -> outputsForOutputName returns empty
                      -> areExpectedOutputsInCacheOnce false   (narinfo_cache.go:95-108)
                      -> IsInBinaryCache false
                      -> Installables() falls back to urlForInstall()  (package.go:275-283)
                      -> nix substitutes from the pinned rev
```

So an empty `systems` map is fully pinned on all platforms; what is lost is the narinfo fast
path, i.e. cold-cache wall-clock, not version drift. Precedent already exists in-tree:
`devbox.lock`'s `darwin.apple_sdk.frameworks.Security` entry carries only `resolved` and
`source`.

The patch makes `--tidy-lockfile` fill every configured platform from one machine, since
store paths are a pure function of (rev, attr, system) and computable at eval time:

```go
// lock/lockfile.go:234 — currently writes only p.Systems[nix.System()]
func (f *File) SetOutputsForPackage(pkg string, outputs []Output) error
// becomes, per configured system:
func (f *File) SetOutputsForSystem(pkg, system string, outputs []Output) error
```

with `FixMissingStorePaths` (`devbox/packages.go:717`) looping over configured systems and
calling `nix eval --system <sys> nixpkgs/<rev>#<attr>.outPath`.

## Implementation Recommendations

**Migration order.** Stand up nxv plus the proxy locally and set `DEVBOX_SEARCH_HOST`
against a scratch project first. Existing lockfiles never consult the service
(`lockfile.go:77-80`), so there is no big-bang cutover and no rollback risk to the estate —
the blast radius is limited to new `devbox add` and `devbox update` invocations.

**Build the resolve endpoints before the search endpoints.** `/v2/resolve` is load-bearing;
`/v2/search` is discovery convenience that `nxv search` and the nxv web UI already cover.

**Regression-test against the real estate, not synthetic fixtures.** The pin set extracted
from our own lockfiles is the acceptance suite: all 146 pins must resolve, and the resulting
attr must match what NixHub chose for at least the 140 that already do. The two known
exceptions are expected failures, not bugs to chase:

- `jdk@25.0.0` — NixHub reports temurin's `25.0.0`; nixpkgs' `jdk25` calls the same build
  `25+36`. The version exists in the index under
  `javaPackages.compiler.temurin-bin.jdk-25`. This is a version-string pairing mismatch, not
  missing data. Either add a narrow normalization rule or rewrite the pin.
- `python@3.9.9` — a genuine hole, and **not** fixed by decision 15, though it was written
  expecting to be. `python39` runs 3.9.0–3.9.4 then jumps to 3.9.13. The hypothesis was that
  3.9.5–3.9.12 lived on `nixos-21.11`/`nixos-22.05`, outside nxv's default channels. Tested by
  ingesting `nixos-21.11` over 2021-11-29..2021-12-19 — exactly the window when 3.9.9 was
  current: the ingest succeeded and added 88 rows the unstable channels never had (`glibc
  2.33-59`, several kernel packages), but no `python3` or `python39` row at all. Nothing in the
  index covers either attribute path across that era, so the pin stays unresolvable. Confirmed
  again at full scale afterwards, with 136 `nixos-21.11` and 181 `nixos-22.05` releases ingested.
  The stable channels were then removed altogether: they contributed 81,468 rows of backports that
  no pin in the corpus needs, nxv's design notes say "all package development happens in unstable;
  stable branches only backport security fixes", and their releases fail nxv's date-keyed snapshot
  gates — a frozen branch cannot satisfy a floor that ladders by calendar year, and the `nh`
  sentinel is enforced from 2024 for a package that only landed in unstable. Each failure retries
  on backoff and reports the index unhealthy, so the alert flaps.

**Two `go`/`nodejs` pins will resolve to a different attr than NixHub chose** (`go@1.22.1`
to `go` rather than `go_1_22`; `nodejs@24.12.0` to `nodejs` rather than `nodejs_24`) because
Tier A is tried first and the unversioned attr held that exact version at some commit. Same
package, same version, different rev. If matching NixHub's rev exactly matters more than
preferring the canonical attr, order Tier B ahead of Tier A for the known versioned
families.

**Watch the `last_modified` guard.** `update.go:147` refuses an update when
`existing.LastModified > resolved.LastModified`, warning "Resolved version for %s has older
last_modified time. Not updating". Because decision 9 emits `first_commit_date`, a backported
version can carry a date older than the pin it replaces and silently fail to update. Test
this against a `python39`-shaped backport case before rollout.

**Do not build the store-path/NAR computation into the proxy.** It requires a full nixpkgs
eval per (package, version, system) and an eval farm, and cross-system `outPath` eval breaks
on packages with platform-gated asserts or impure eval. Decision 12 handles it client-side
for a fraction of the cost. Note that `resolveV2` (`lock/resolve.go:115`) only populates
`pkg.Systems[sys]` when `len(info.Outputs) != 0`, so a proxy returning no outputs yields a
lockfile with no `systems` key at all — nxv's `platforms` data is therefore only useful for
`devbox search` display.

**Ops runbook.** Schedule the **default incremental** `nxv index` only. Proven behaviour on a
`nxv sync`-bootstrapped DB: it discovers releases from S3, ingests new ones, rebuilds the
attribute cache, bloom filter, and stats cache, and reports `status: healthy` with
`head lag: 7h`.

`--full` is a one-way door and must be blocked in the runbook and in any wrapper script. Run
against a bootstrapped DB it re-queued all 8,505 known releases as pending; having ingested
only the capped 2, the watermark then derived from a 2020 release and reported
`head lag: 56410h`, `status: UNHEALTHY`. Package data survived (1,805,969 rows, 288,291
attrs intact), so this is bookkeeping corruption rather than data loss — but 4,185 of the
re-queued releases are pre-2020 and unreachable without `--backfill-evals`, so the watermark
can never re-settle. A genuine full rebuild must go into a fresh database with
`--backfill-evals` and be swapped in.

**Index characteristics** for capacity planning: ~2.0 GB SQLite on disk, ~220 MB compressed,
1.8M `(attr, version)` rows across 288K attrs, coverage 2016-09-28 onward. The 2,902 pending
releases in a freshly synced index are all pre-2020; 2020-onward coverage is complete. The
`indexer` feature is **not** in nxv's default Cargo features, so the published release binary
cannot build an index — self-indexing requires the `nxv-indexer` flake package or
`cargo build --features indexer`.

**Unverified assumption to spike first.** Everything above rests on incremental ingest
working on a bootstrapped database, which was proven for the ingest write path and the
incremental planner. What was not exercised is a multi-day steady state — confirm that a
nightly incremental run stays healthy across several days before decommissioning any NixHub
fallback.
