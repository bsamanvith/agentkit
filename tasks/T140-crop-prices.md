---
id: T140
title: Crop prices: mandi prices from several providers, a price history, estimates, the Prices tab
epic: E25
status: review
depends_on: [T134]
---

# T140: crop prices

## Context

The owner (2026-11-08): "we need price too. price estimates, and then
also actual price integration at various places in the state. Focus on
Karnataka, AP, Telangana and Tamil Nadu first. If there is a single
source that's better. Ensure that we can have multiple providers and
multiple ways to fetch the data, cause this space is expected to be
fragmented, we keep changing providers. For weather and for this news
and for price and for knowledge too." And: free sources and free tiers
first; flag anything not free. Design: [PRICES.md](../apps/farm/docs/PRICES.md).

## Scope

- `apps/farm/feeds`: the provider layer for outside data (`Provider`,
  `Chain`, `Paused`, `Cached`, `Pick`, HTTP helpers), no farm in it.
- `apps/farm/prices`: quotes, queries with loose name matching, commodity
  names from crop ids and local words, providers `DataGov` (Agmarknet on
  data.gov.in, all India) and `Files` (CSV exports), `History` (memory;
  SQLite in `pricesqlite`), `Recorded`/`Saved`, MSP kharif 2026-27 and
  rabi 2027-28, `EstimatePrice`, `Service` (today with widening and the
  rest of the state, history, estimate, import).
- `apps/farm/farmagent`: tools `mandi_prices`, `price_estimate`
  (toolset `farm-market`), skill `sell-or-hold`.
- `apps/farm/farmapi`: `/api/prices`, `/api/prices/history`,
  `/api/prices/estimate`, `/api/prices/sources`.
- `apps/farm/cmd/farm`: `-prices`, `-price-files`, `FARM_DATAGOV_KEY`,
  `<db>-prices.db`.
- Web: the Prices tab (`prices.tsx`, a lazy chunk).

## Out of scope

- A daily refresh and price alerts (on T139's automation engine), mandi
  distances and transport cost, CEDA's API, state boards, scraping e-NAM.

## Acceptance criteria

- [x] A chain takes the first answer, passes over no-data and unavailable providers, says no data only when all did, stops on a cancelled context; a rate-limited provider is not asked until Retry-After (or an hour); answers are cached for their TTL, errors not, the oldest evicted; providers are picked by a comma list, unconfigured ones skipped, unknown names rejected; HTTP failures are sorted for a chain — test: `feeds_test.TestChain*`, `TestPaused*`, `TestCached*`, `TestPick*`, `TestGetBody*`
- [x] data.gov.in is asked by state and commodity with the key, district and dates filtered here (Rangareddy matches Rangareddi), numbers as strings or numbers, bad rows skipped; no key is "not configured"; the key never appears in an error — test: `prices_test.TestDataGov*`
- [x] Price files in each source's format are read by header, the state taken from the folder, bad rows counted, changes seen without a restart — test: `prices_test.TestFilesReadEachSourcesExportAndSeeChanges`
- [x] Both histories keep one quote a day per market, variety and grade and match like a query — test: `prices_test.TestHistoriesKeepOneQuoteADayAndMatchLikeAQuery`
- [x] The estimate moves the recent level by the seasonal swing, widens with months ahead, falls back to the state, lowers its confidence for thin or old history, shows the MSP without history, and scales revenue — test: `prices_test.TestEstimate*`
- [x] The service falls back to saved prices when the live provider is down, widens a district to the state, lists the rest of the state's markets, and imports files — test: `prices_test.TestService*`, `TestTodayAddsTheRestOfTheStatesMarkets`
- [x] The tools find the farm's state, answer from the tools, and the assistant has the price rules and the skill — test: `farmagent_test.TestPriceTools*`, `TestAssistantWithPrices*`
- [x] The routes answer, 404 with a farmer's message when there are no prices, 400 for a bad query, 503 when not configured — test: `farmapi_test.TestPriceRoutes`
- [x] The Prices tab shows markets, the chart, the estimate and the MSP, and asks again when the month or yield changes — test: `web/apps/farm/src/prices.test.tsx`
- [x] The app on price files, desktop and phone, and Claude answering in Telugu with the tools — manual run in a cloud session (PR description; screenshots in the project files, farm/pr*.png)
- [ ] `datagov` against the real API with a key, from a connection data.gov.in accepts — needs the owner's free key; cloud sessions are refused
