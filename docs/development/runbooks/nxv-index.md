# Runbook: the nxv index behind `devbox search-proxy`

`devbox search-proxy serve` answers Devbox's search API from an nxv version index. This runbook
covers keeping that index fresh.

`serve` starts and supervises its own nxv, built with nix, so nxv need not be installed; the child
is shut down when serve exits. Pass `--index <url>` to read from an nxv that is already running
instead, and `--nxv-db` to name the index database.

## Per-system versions

An index row's version comes from a channel snapshot, and a snapshot is one system's evaluation.
Where nixpkgs defines an attribute path differently per platform, that row describes the wrong
derivation for every other system: at `d12c0de`, `jdk25` is `openjdk` 25.0.4.1+1 on Linux and
`zulu` 25.0.3 on aarch64-darwin.

`serve` asks nxv what each system actually evaluates, so a resolve reports the version this machine
will get, lists only the systems that agree on it, and carries their store paths. nxv evaluates a
commit it has not seen before -- around 40s for the four systems at one commit, then recorded and
free. `--eval-systems=false` turns that off, and resolves fall back to the row's version, which is
the pre-per-system behaviour.

It needs a writable index (nxv records what it evaluates) and `nix` on the machine. When reading
from an nxv started elsewhere with `--index`, that server needs its own `--eval-systems`; a resolve
against one without it falls back to the row's version and logs the reason.

## Bootstrap, and starting over

    devbox search-proxy index sync --nxv-db <path to the index> [--force]

This downloads a prebuilt index and writes it over the local one. It is both the bootstrap and the
way to discard an index and start again: it took ~20s and produced 1,807,104 rows across 288,302
attributes, against the hours a local build costs.

It is also the fix for an index whose ledger has gone wrong -- a `--full` run on a bootstrapped
database, or channels ingested that should not have been. A fresh sync resolved all 144 of the 146
corpus pins that are resolvable at all, the same as an index carrying 80,409 extra rows of stable
backports, so starting over costs nothing the estate uses.

Follow it with `refresh` to pick up anything published since the artifact was built.

`--manifest-url` points sync at a manifest you host yourself, with `--public-key` for its
minisign key. See "Keeping a recovery copy" below.

A synced index still has the pre-2020 nix-env era unsettled (~2,902 releases). Settle it with
`refresh -- --backfill-evals`, which needs `nix` and takes 1.5-3h; the report calls those releases
"unreachable without --backfill-evals" until then.

Building an index locally instead needs the `indexer` Cargo feature, which the published nxv
release binary does not carry. `refresh` handles that itself: with no `--nxv` it builds the `nxv-indexer` flake package
with nix, pinned to a revision, so no local nxv install is needed. The first build compiles nxv
from source and takes a while; nix keeps the result. Pass `--nxv <path>` to use an executable you
already have, or `--nxv-installable` to build a different one.

## Refresh

    devbox search-proxy index refresh --nxv-db <path to the index> \
        -- --report <path to the report>

Everything after `--` is passed verbatim to nxv's `index` subcommand. The database is named with
`--nxv-db` rather than passed through, because nxv takes `--db-path` before the subcommand and
rejects it after. The command runs the **default incremental**
`nxv index` over the channels in `internal/nxvops/refresh.go`: nxv's own `nixpkgs-unstable`
(the historical spine back to 2016) and `nixos-unstable-small` (the currency channel).

Run it on a schedule. The refresh cadence is the index's head lag, and lag is what this service
exists to control.

## `--full` is forbidden on a bootstrapped database

`refresh` exits non-zero when handed `--full`, and runs nothing.

`--backfill-evals` is **not** refused, despite being slow: it only adds the pre-2020 releases to
the worklist that are otherwise filtered out, and re-plans nothing.

Run against a `nxv sync`-bootstrapped database, `--full` re-queued all 8,505 known releases as
pending. Having ingested only the capped few, the watermark then derived from a 2020 release and
the index reported `head lag: 56410h`, `status: UNHEALTHY`. Package data survived — this is
bookkeeping corruption, not data loss — but 4,185 of the re-queued releases are pre-2020 and
unreachable without `--backfill-evals`, so the watermark can never settle again.

A genuine rebuild goes into a **fresh** database with `--backfill-evals` (~1.5-3h) and is swapped
in once it reports healthy.

## Health

    devbox search-proxy index health --report <path to the report> --max-lag 24h

It reads the report the refresh wrote and exits non-zero when the report's `healthy` is not true
or its `head_lag_hours` is beyond `--max-lag`. A healthy nightly incremental run reports
`"head_lag_hours": 7`.

It reads a report rather than producing one because `nxv index` ingests: a health check that ran
it would mutate the index it is meant to observe. Have the refresh write the report by passing
`--report <path>` through to nxv, as above.

Alert on this exit code. `"healthy": false` after a refresh means the watermark stopped settling,
which is the `--full` failure above unless the database is fresh.

## Only the unstable channels are ingested

The `nixos-*` stable branches are not ingested, and re-adding them is a mistake that has already
been made once.

nxv's own design notes say why: *"all package development happens in unstable; stable branches
only backport security fixes"*. Its documentation accepts any `nixos-*` channel name but never
recommends one, and every documented example uses an unstable channel.

Measurement agreed. Ingesting 136 `nixos-21.11` and 181 `nixos-22.05` releases added 81,468 rows
of backports and **did not** produce the `python39` 3.9.5-3.9.12 versions they were added for --
`python39` still runs 3.9.4 (2021-04) then 3.9.13 (2022-06), so `python@3.9.9` stayed
unresolvable.

They also break the health signal, because nxv's snapshot gates key on a release's *date* against
unstable's growth curve and a frozen branch cannot satisfy them:

- `package count N below the absolute floor M for <year>` — `absolute_floor` in nxv's
  `src/index/monitor.rs` ladders by calendar year (90k for 2023, 130k for 2026) while a stable
  branch stays at its branch-point size. `nixos-22.05` releases dated 2023 carry 86,040 attrs.
- `sentinel package 'nh' missing from snapshot` — the `nh` sentinel is enforced from 2024-01-15
  because `nh` entered nixpkgs in 2023-12. It entered *unstable* and was never backported to the
  23.11 branch, which kept releasing into 2024. Only 26 of that branch's 112 releases ingest.

Neither is configurable: nxv's sentinel list only accepts additions and the floors are a hardcoded
match. That failed 93 releases, and a failed release retries on exponential backoff up to five
attempts, reporting the index UNHEALTHY each time -- so the alert flaps rather than failing once.
Active branches keep aging past the floors, so new failures keep appearing.

Rows already ingested from stable channels are left in place; they are ordinary version rows and
harmless. The failed release records stay too, but the planner filters the worklist to the
requested channels, so they are never retried once the channels are gone.

## Building from scratch

    devbox search-proxy index build --nxv-db <path to a new index>

This owes nothing to a published artifact, and costs hours. Most of that is discovery, not
ingest: on an empty database every release directory is new and needs its own S3 metadata fetch,
and a measured run enumerated 5,240 of ~11,458 releases in about 20 minutes before ingesting
anything. The ~4,200 ingests follow.

Add `--backfill-evals` for the pre-2020 nix-env era, which needs `nix` and takes 1.5-3h more.

`build` refuses to run against an existing index rather than deleting one: against a populated
database it would silently be an ordinary incremental refresh instead of the rebuild that was
asked for. Remove the index yourself, or build somewhere else.

## Keeping a recovery copy

`sync` only knows about nxv's published index until it is told otherwise, so a proxy that has
diverged -- a longer history, different channels -- has nothing to recover from. Publishing the
index somewhere durable closes that:

    devbox search-proxy index publish --nxv-db <index> --output ./dist \
        --url-prefix https://<bucket>.s3.amazonaws.com/nxv -- --sign --secret-key <key>

That writes the compressed index, the bloom filter and a manifest. Upload the directory, and a
replacement proxy is a sync away rather than a rebuild:

    devbox search-proxy index sync \
        --manifest-url https://<bucket>.s3.amazonaws.com/nxv/manifest.json \
        --public-key <key from nxv keygen>

Signing is worth the extra step: this artifact becomes the index every resolution reads, so an
unsigned manifest on a writable bucket is a supply-chain hole. `nxv keygen` makes the keypair.
