# nxvops testdata

## `report/*.json`

One `nxv index --report` body per file, as the health check reads them.

- `healthy.json` — a nightly incremental run's report, `head lag: 7h`.
- `unhealthy.json` — the state `--full` leaves a bootstrapped database in: every known release
  re-queued, the watermark derived from a 2020 release, `status: UNHEALTHY`.
- `stale.json` — healthy, but `head lag: 72h`, so it fails a 24h threshold.
