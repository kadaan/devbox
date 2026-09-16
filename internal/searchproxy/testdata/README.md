# searchproxy testdata

## `corpus.json`

The acceptance suite: every versioned pin in the estate, one entry per distinct
`name@constraint`, extracted from the 199 `devbox.lock` files under `/source/work`. 146 pins,
141 distinct versions, 139 exact, 4 prefix, 3 `latest`.

Each entry records what NixHub answered for that pin, taken from the lockfile it was found in:

| field | source |
| --- | --- |
| `name` | the part of the lockfile key before `@` |
| `constraint` | the part after `@` — an exact version, a `N`/`N.N` prefix, or `latest` |
| `version` | the entry's `version` |
| `attr` | the attribute path in the entry's `resolved` |
| `rev` | the nixpkgs revision in the entry's `resolved` |
| `last_modified` | the entry's `last_modified`, normalized to UTC |

Where two lockfiles disagreed about the rev for one pin, the first in path order wins.

## `nxv/packages.json`

Keyed `<attribute_path>`, one verbatim `/api/v1/packages/{attr}` response body per key: every
row `versions.json` holds for that attr, in the order that file holds them.

One key is hand-written for the discovery endpoints: `platform-legacy`, whose `platforms` are
spelled `arm64-macOS` and `x86-64-Linux` rather than as Nix systems, so `devbox search` is
asserted to render them as `aarch64-darwin` and `x86_64-linux`.

## `nxv/search.json`

Keyed `<query>`, one verbatim `/api/v1/search?q={query}` response body per key. Each row is
copied from `nxv/packages.json`, so any `attribute_path` a search returns is also a key there
and can be fed straight back into `/v2/pkg`. The fake nxv server answers an unlisted query with
an empty result set.

## `nxv/health.json`

The verbatim `/api/v1/health` body. Its `index_commit` is the index head every head-presence
test resolves against.

## `nxv/versions.json`

Keyed `<attribute_path>/<version>`, one verbatim `/api/v1/packages/{attr}/versions/{version}`
response body per key. The fake nxv server in the tests serves this map and 404s on a miss.

The bodies are **derived from `corpus.json`, not recorded from a live nxv**: `first_commit_hash`
and `first_commit_date` carry the pin's own NixHub rev and date, and `last_commit_hash` /
`last_commit_date` are synthetic and deliberately different, so a proxy that pins the last
commit instead of the first fails the corpus test. Replacing this file with a real recording
does not change what the tests assert.

Four keys are hand-written rather than derived, so the file reflects what the real index holds:

- `jdk25/25+36` and `javaPackages.compiler.temurin-bin.jdk-25/25.0.0` — nixpkgs' `jdk25` calls
  the build `25+36`, and `25.0.0` lives only under the temurin path. `jdk25/25.0.0`, which the
  `jdk@25.0.0` pin would need, is therefore absent.
- `go/1.22.1` and `nodejs/24.12.0` — the unversioned attrs held those exact versions at a
  different commit than the versioned attrs the estate pinned.

A key whose `last_commit_hash` equals `nxv/health.json`'s `index_commit` is the version at
index head, which is how the `latest` and prefix pins are decided: `awscli2/2.35.11`,
`jq/1.8.2`, `zip/3.0`, `nodejs/24.12.0`, `python312/3.12.11`, `python313/3.13.3`,
`python314/3.14.3`, `go-bindata/4.0.2`, `zig/0.14.0` and `zig/0.14.0-pre`.

Two further keys are hand-written for constraint resolution:

- `go-bindata/20151023-a0ff256` — the 2016 build Nix's `compareVersions` ranks above `4.0.2`.
  It is not at head, so head-presence picks `4.0.2`.
- `zig/0.14.0-pre` — a prerelease at head alongside a stable release also at head.

`jdk8`'s three `8uNNN` keys are deliberately all away from head, so `jdk8@latest` is decided by
`compareVersions` rather than by head-presence.

There is deliberately no `python3/3.9.9` row. `python39` runs 3.9.0-3.9.4 then jumps to
3.9.13, and ingesting `nixos-21.11` across the window when 3.9.9 was current added no
`python3` or `python39` row at all, so `python@3.9.9` is an expected failure rather than a
pin the fixtures should answer.

The `python` family entries (`python`, `python3`, `python311`-`python315`, plus the
non-family `python-launcher`, `python3Minimal` and `python314FreeThreading`) are recordings
that exercise `latest` resolution across a family: `python` is the retired Python 2 line
whose newest row is 2.7.18 and is not at head, and `python315` carries a release candidate,
so both of the wrong answers are reachable if the selection rules regress.
