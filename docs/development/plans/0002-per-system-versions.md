# Plan: Per-System Versions Without a Writable Index

> No source RFC. The decisions below come from measurement; each one records the number that
> settled it, because several look arbitrary without it.

A channel release publishes one `packages.json`, for one system, so every `package_versions` row
carries that system's version. Where nixpkgs defines an attribute path differently per platform
the row is wrong everywhere else: at `d12c0dec`, `jdk25` is `openjdk` 25.0.4.1+1 on Linux and
`zulu` 25.0.3 on aarch64-darwin.

Resolution already handles this by asking nix at request time and recording the answer. That
closes the gap for a developer running their own index, and closes it for nobody else: recording
needs a writable index, and the deployment this fork is heading for is **one writable indexer
publishing an artifact, many read-only servers consuming it**. Per-system data has to be in the
published file.

## Already landed

Context for the phases below; not work items.

- `nxv b5a058e` — `system_observations`, the per-attribute evaluator, and
  `GET /api/v1/packages/{attr}/systems`. On-demand evaluation behind `serve --eval-systems`.
- `nxv 293f641` — `--eval-systems` refuses an index it cannot write, instead of failing on the
  first request after the evaluation was already spent.
- `devbox faafb48a` — the proxy takes version, systems and store paths from observations, and
  resolves a version only one system ever had.
- Devbox search and `devbox info` no longer print nixhub.io links when the search host is not the
  one nixhub renders.

## Architectural Decisions

- **The sweep is a pure-evaluation attribute walker, not `nix-env` and not `nix-eval-jobs`.**
  Measured on one tree at `d12c0dec`, per system:

  | approach | time | attrs | keeps aliases | store paths |
  | --- | --- | --- | --- | --- |
  | `nix-env -qaP --json` | 20s | 104,900 | no | no |
  | `nix-eval-jobs` | >25 min (killed at 48,086) | complete | yes | yes |
  | pure-evaluation walker | 18–23s | 112,319 | yes | no |

  `nix-env` deduplicates by derivation and reports each one under a single arbitrary attribute
  path: at that commit it kept `javaPackages.compiler.openjdk25` and dropped `jdk25`, `openjdk25`
  and `jdk` — precisely the paths Devbox resolves. `nix-eval-jobs` keeps them but instantiates
  every derivation, which is the same work that makes `nix-env --out-path` abort the whole run at
  the first uninstantiable package (`aaaaxy`, Linux-only). Evaluating without instantiating keeps
  the aliases and the 20 seconds.

- **The sweep records versions and availability, never store paths.** Instantiation is the entire
  difference between 20 seconds and 75+ minutes. Read-only servers therefore answer with correct
  versions and per-system availability but no binary-cache fast path — no worse than before this
  work, where they had neither. `--eval-systems` still fills store paths for a writable index.

- **Store divergences and absences only, gated by a sweep ledger.** Measured like-for-like across
  112,262 shared attrs: 162 differ (0.14%), 229 exist on Linux and not on Darwin, 57 the reverse.
  So a sweep writes ~391 rows per system per commit, against `package_versions`' 1.8M. The
  `system_sweeps(system, commit_hash)` ledger is what lets absence carry meaning: for a swept pair,
  "no row" *is* the observation that the system agrees with the snapshot. An unswept pair keeps
  today's meaning — unknown, and evaluated on demand where that is allowed.

- **No `SCHEMA_VERSION` bump, for the ledger as for `system_observations`.** Both are recomputable
  by re-sweeping, which puts them on `package_attrs`' "cache, not schema" footing. A published
  index whose `min_version` rose would lock out readers that purely additive tables cannot affect.
  `nxv publish` compresses the whole database, so both tables ship with no publisher change.

- **Sweep the newest commit per refresh.** One minute of CPU for three systems, ~1,200 rows.
  Backfilling older commits is possible at the same rate (~100 commits ≈ 1.5h, ~120k rows) but is
  not part of this plan.

- **Search reads observations; it never evaluates.** A search result lists ~10 versions at ~10
  commits; observing them on demand is 10–17s each. The `evaluate=false` parameter exists so the
  search path cannot accidentally buy that.

## Out of Scope

- **Store paths in bulk.** Needs `nix-eval-jobs` at ~75 min per system per commit. Revisit only if
  the fast path matters enough to read-only tenants to pay it.
- **Observing on demand during search** (the "2b" option): bounded, but makes a search that is
  currently 0.1s take 10–17s on a package's first view.
- **A per-system `version` in `devbox.lock`.** The lockfile has one version field per package, so
  a lock committed from a Mac carries a string that is wrong for a Linux teammate. nixhub has the
  identical wart. Fixing it is a lockfile schema change with no nxv work behind it.
- **Matching nixhub's commit choice.** It pins the single commit it observed; the proxy pins
  `first_commit_hash`. Both are commits at which the version existed, and changing ours would
  invalidate the 139-pin corpus for no correctness gain.
- **Closing the walker's coverage gap beyond what Phase 1 needs.** The walker reaches 112,319 of
  the 149,705 attrs the index carries at that commit, because sets like `emacsPackages` have
  `recurseForDerivations = false` yet still appear in `packages.json`. Unreached attrs stay
  unswept, which degrades to on-demand evaluation rather than to a wrong answer.

## Testing Approach

Unit tests use a stub evaluator and a stub sweep, so no phase needs `nix` or the network to prove
its logic; the tests that do need `nix` are `#[ignore]`d, matching `src/index/eval.rs`.

Two regression gates carry across every phase:

- **The 139-pin corpus** (`internal/searchproxy/testdata/corpus.json`). It runs against a fake
  index with no per-system data, so it is the standing proof that an index without sweeps or
  observations still resolves exactly as it did before any of this existed.
- **A read-only index fixture.** Serving must never require a writable file: a file and directory
  with write permission removed, `journal_mode=delete`, asserted to answer the systems endpoint
  and to return an empty list — not an error, and not a write — for an unswept pair.

nxv phases gate on `ci-local` (fmt-check, clippy, test, all with `--features indexer`); Devbox
phases on `devbox run lint` and `devbox run test`.

---

## Phase 1: The sweep

**Repo**: nxv, behind the `indexer` feature (it is ingestion, and only the indexer needs it).

### What to build

The walker, as a Nix expression evaluated with `nix-instantiate --eval --strict --json`: recurse
the package set, emit `{attr, pname, version, name}` for every derivation, wrap every force in
`tryEval` so one throwing attribute cannot end the sweep. Recursion follows
`recurseForDerivations` with a depth cap.

`nxv systems sweep --system <s>... --commit <rev>` runs it per system and writes the difference
against the snapshot rows at that commit:

- a version that differs from the row → a `system_observations` row;
- an attribute the index carries at that commit but the sweep did not find → a row with a null
  version;
- agreement → nothing.

Then one `system_sweeps` row recording that the pair was swept, with the attribute count, so the
absences above can be told apart from "never looked".

The comparison needs the snapshot version *at that commit*, which is the row whose observation
range covers the commit's date.

### Acceptance criteria

- [ ] `nxv systems sweep --system aarch64-darwin --commit <rev>` records `jdk25` as 25.0.3 while
      the row says 25.0.4.1+1, and records nothing for an attribute the system agrees on
- [ ] The walker output includes `jdk25`, `openjdk25`, `zulu25` and
      `javaPackages.compiler.openjdk25` as separate attribute paths — the aliases `nix-env` drops
- [ ] An attribute that throws when forced is skipped, and the sweep completes
- [ ] A sweep writes a `system_sweeps` row; a sweep that fails part-way writes none
- [ ] Re-sweeping the same (system, commit) is idempotent: same row count, no duplicates
- [ ] Sweeping does not touch `package_versions`
- [ ] Unit tests drive a stub sweep; the test that shells out to `nix` is `#[ignore]`d

---

## Phase 2: Ledger-aware reads

**Repo**: nxv.

### What to build

Teach `GET /api/v1/packages/{attr}/systems` the three states a (system, commit) pair can be in:

- **a recorded observation** → return it;
- **swept, no observation** → synthesize the answer from the snapshot row, because that is what
  the sweep observed;
- **unswept** → evaluate if allowed, otherwise report nothing.

Add `evaluate` (default true) so a caller that needs many commits at once can ask for recorded
data only. Report sweep coverage on `/health` alongside `eval_systems` and `system_observations`,
so a client can tell a swept index from an empty one.

`SystemsParams`' `Default` must be written by hand: a derived one would make `evaluate` false and
silently mean the opposite of the same request over the wire.

### Acceptance criteria

- [ ] A swept pair with no observation returns the snapshot row's version, marked as coming from
      the sweep rather than from an evaluation
- [ ] An unswept pair with `--eval-systems` off returns no row, and writes nothing
- [ ] `evaluate=false` never runs `nix`, even with `--eval-systems` on
- [ ] `?version=` finds a version recorded by a sweep, not only one recorded by an evaluation
- [ ] The read-only fixture answers all of the above from a file it cannot write
- [ ] `/health` reports how many (system, commit) pairs have been swept

---

## Phase 3: Sweep on refresh, and publish

**Repo**: nxv and Devbox.

### What to build

Run the sweep for the newest ingested commit at the end of `nxv index`, for the systems the
operator names, and surface it in the run report. Wire the same through
`devbox search-proxy index refresh` so the estate's 6-hourly job produces a published index that
already carries per-system data.

A sweep failure must not fail the ingest: the index is still correct without it, and an ingest
that rolls back over a sweep problem loses the snapshot data too.

Then the operational documentation: the runbook section on what a sweep costs, what it stores, and
what a read-only tenant does and does not get; `AGENTS.md` on the ledger's meaning.

### Acceptance criteria

- [ ] `nxv index --sweep-systems <s>,<s>` ingests, then sweeps the newest commit
- [ ] A failing sweep leaves the ingest committed and marks the run report, exit code unchanged
- [ ] `nxv publish` ships `system_observations` and `system_sweeps` with no publisher change, and
      `min_version` stays 4
- [ ] An index published after a sweep, served read-only, resolves `jdk25` to 25.0.3 on
      aarch64-darwin with no `--eval-systems` and no writable file
- [ ] A pre-sweep published index still resolves everything it did before, via the 139-pin corpus
- [ ] Runbook covers cost, storage, and the read-only tenant's limits

---

## Phase 4: Per-system `devbox search`

**Repo**: Devbox.

### What to build

`/v2/pkg` currently turns each index row into one release carrying the snapshot's version and
`meta.platforms` — which is how a Mac sees six versions of `jdk25`, none of them the one it would
get. Build releases from observations instead, grouped the way nixhub groups them: one release per
distinct version, carrying only the systems that saw it, dropping systems where the attribute does
not resolve. A row with no per-system data passes through unchanged.

The lookups are `evaluate=false`, so search never evaluates and never blocks.

A partly-truthful table is worse than a consistent one if nothing marks it. Divergence is a
property of the attribute, not of the row, so when any package in the results is known to diverge,
print one line under the table saying so — and print nothing at all for the overwhelming majority
of packages, where every system agrees.

### Acceptance criteria

- [ ] An observed row splits into one release per version, each with only its own systems
- [ ] An unobserved row renders exactly as it does today
- [ ] A system where the attribute does not resolve is absent from the platform column
- [ ] `devbox search jdk25` on aarch64-darwin lists 25.0.3, and `devbox add jdk25@25.0.3` resolves
      the version the search printed
- [ ] Search issues no evaluation: with `--eval-systems` on, a search leaves the observation count
      unchanged
- [ ] The divergence note appears only when a result actually diverges
- [ ] Search against an index with no per-system data is byte-identical to today's output

---

## Phase 5: Review

Deep review across all phases, fix findings, commit.

<!-- workflow
{
  "name": "run-per-system-versions-tdd",
  "description": "Implement plan 0002 (per-system versions available to read-only servers) phase-by-phase with TDD, one commit per phase",
  "gates": {
    "check": "devbox run lint",
    "test": "devbox run test",
    "focused": "devbox run -- go test -race"
  },
  "houseRules": ["CONTRIBUTING.md"],
  "newFilesToCommit": [
    "docs/development/plans/0002-per-system-versions.md"
  ],
  "phases": [
    { "n": 1, "kind": "implementation", "title": "Phase 1", "name": "The sweep", "detail": "nxv: pure-evaluation attribute walker plus `nxv systems sweep`, storing divergences and absences against the snapshot rows, with a system_sweeps ledger. Gates are nxv's ci-local, not devbox's." },
    { "n": 2, "kind": "implementation", "title": "Phase 2", "name": "Ledger-aware reads", "detail": "nxv: the systems endpoint distinguishes recorded, swept-and-agreeing, and unswept; adds evaluate=false; reports sweep coverage on /health. Gates are nxv's ci-local." },
    { "n": 3, "kind": "implementation", "title": "Phase 3", "name": "Sweep on refresh, and publish", "detail": "nxv and devbox: sweep the newest commit at the end of an ingest, wire it through devbox search-proxy index refresh, prove a published index serves per-system data read-only, document it" },
    { "n": 4, "kind": "implementation", "title": "Phase 4", "name": "Per-system devbox search", "detail": "devbox: /v2/pkg builds releases from observations grouped by version and systems, never evaluating, with a divergence note only when a result diverges" },
    { "n": 5, "kind": "review", "title": "Review", "name": "Deep review", "detail": "Deep review of all phases, fix findings, commit" }
  ]
}
-->
