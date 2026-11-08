---
id: T138
title: A backup elevation source for the land survey, and a cache kept on disk
epic: E25
status: review
depends_on: [T135]
---

# T138: a backup elevation source

## Context

While testing T137 (2026-11-07) Open-Meteo's elevation API answered 429
(its free daily limit), so surveys had no slope. The owner asked
(2026-11-08) for a free backup that is used automatically, and for
caching that saves calls.

## Scope

- `apps/farm/elevation`: a `Client` shared by two services, Open-Meteo
  (Copernicus GLO-90) and Open Topo Data (SRTM 30 m by default, at most
  one request a second); `Fallback` takes the first full answer so one
  survey's heights come from one model; answers say their model and grid
  (`Heights`); a 429 pauses that service (Retry-After, else an hour)
  instead of being retried; `Cache` remembers heights per service and
  point, in memory or in a file, bounded.
- `apps/farm/surveyor`: the survey's grid comes from the answer.
- `apps/farm/cmd/farm`: `-elevation-backup` (`FARM_ELEVATION_BACKUP`),
  the cache in `<db>-heights.json`.

## Out of scope

- Self-hosting Open Topo Data, keyed services (GPXZ, OpenTopography),
  blending two models.

## Acceptance criteria

- [x] Each service batches 100 points, retries once on a network error or 5xx, not on a 4xx; Open Topo Data waits a second between requests and fails on a null height — test: `elevation_test.TestOpenMeteo`, `TestOpenTopoData`, `TestRetriesOnce`, `TestFailures`
- [x] A 429 pauses the service for Retry-After or an hour, then it is asked again — test: `elevation_test.TestRateLimitPauses`
- [x] Open-Meteo over its limit: the backup answers, its model and 30 m grid are reported, and the next survey does not ask Open-Meteo; both down is an error naming both; a cancelled survey stops — test: `elevation_test.TestFallback`
- [x] Heights survive a restart in the cache file, kept apart per service; a broken file starts empty; the cache is bounded — test: `elevation_test.TestCacheFile`, `TestCacheIsBounded`
- [x] Against the real services, and the app with Open-Meteo answering 429: the survey's slope comes from SRTM 30 m via Open Topo Data — manual run in a cloud session (PR description)
