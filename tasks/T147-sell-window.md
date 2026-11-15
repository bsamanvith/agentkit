---
id: T147
title: Seasonal price index, harvest-month advice and its backtest
epic: E25
status: review
depends_on: [T140]
---

# T147: Seasonal price index, harvest-month advice and its backtest

## Context

The owner's brief of 2026-11-15: crop cycles at the centre of the farm
app, basics first, everything else optional and additive. Design:
[CYCLES.md](../apps/farm/docs/CYCLES.md), section "When to harvest and sell".

## Scope

- `apps/farm/prices`: seasonal index per crop and market from the history, harvest-month advice inside a sowing window, sell-or-hold with storage cost, a walk-forward backtest against naive baselines, and a gate that hides advice that does not beat them.
- A loader for CEDA and India Data Portal files; `make farm-price-backtest`.

## Out of scope

- API routes, web and `cmd/farm` wiring (the integration after T145).

## Acceptance criteria

Each item is one observable behavior and maps to one test (all in
`apps/farm/prices/timing_test.go` unless named; every price in them is
synthetic, from `prices/synthetic`).

- [x] A seasonal index from several years of a known season gives each month's usual price against the year within 0.04, with its years, quartiles and confidence (`TestSeasonalIndexRecoversAKnownSeason`).
- [x] One wild year and a two-month gap move no month's index by more than 0.03: medians, gaps filled for the moving average only (`TestSeasonalIndexShrugsOffOneWildYearAndFillsShortGaps`).
- [x] Too little history is refused with `ErrTooLittleHistory` (under 9 months of the year with 2 years each, or months never traded) and two years is marked "low" (`TestSeasonalIndexRefusesThinHistoryAndMarksItLow`).
- [x] An index with a cutoff uses nothing from the cutoff on (`TestSeasonalIndexUsesNothingFromOrAfterItsCutoff`).
- [x] The harvest-month advice shifts the sowing or variety only when the harvest month's gain, after the yield risk, clears the margin and the index's spread; otherwise it says to sow at the best agronomic date; options are ranked with reasons (`TestAdviseSowingShiftsOnlyWhenTheHarvestMonthPaysForTheYieldRisk`).
- [x] Sell or hold advises holding a storable crop to a month only when it pays net of loss in store, storage cost and interest; onion's losses, a dear store and a perishable crop give "sell now" (`TestAdviseSellHoldsOnlyWhenItPaysNetOfStorage`).
- [x] The walk-forward backtest scores each year against selling at harvest and "same as last year" (mean gain, hit rate, MAPE) and passes a real season and fails no season and a turned-round season (`TestBacktestPassesASeasonThatHoldsAndFailsOneThatDoesNot`).
- [x] Each year's advice in the backtest depends only on prices before its decision month (`TestBacktestAdvisesEachYearOnlyFromTheYearsBefore`).
- [x] The gate needs N years and a gain over both baselines (`TestGateNeedsEnoughYearsAndBothBaselinesBeaten`), and over 40 synthetic histories it shows seasonal advice often and no-season advice rarely (`TestGateShowsASeasonOftenAndNoSeasonRarely`).
- [x] `AdviseTiming` (crop, place, sowing window or sowing day, today) gives sowing and selling advice only behind a passed backtest, widens to the state, and otherwise gives no advice with the reason (thin history, no season, a perishable crop); bad queries are `ErrInvalid` (`TestAdviseTimingGivesAdviceOrNothingWithReasons`).
- [x] India Data Portal (both layouts) and CEDA Agmarknet CSVs are read by `ScanFiles` (any size, streamed) and by `Files` (`TestScanFilesReadsIndiaDataPortalAndCEDAFiles`).
- [x] `make farm-price-backtest` prints the table on files or on synthetic series (`apps/farm/cmd/farm-price-backtest`, `TestBacktestTableOnSyntheticSeriesAndOnFiles`).
- [ ] Validated on long real history (10+ years per crop and market): open. Run on India Data Portal's 2021-2026 files (5 years, so 1 to 3 test years); CEDA's 2000-2023 files were not reachable from here.

## Notes

- **Why `review`:** every testable criterion passes; `make check` fails only on `apps/farm/watch` `TestNoticesBecomeMessages`, a time bomb in the base commit (its notices are dated 2026-11-10 and expire on the real clock), not touched here and reported; and real-data validation is thin (see PRICES.md, "What the backtest showed"): 41 of 385 crop, area and rule combinations in AP, Telangana and Karnataka had 3 or more years to test, 4 passed (groundnut hold in Telangana, the tomato sowing rule in three Karnataka districts, all on 3 years). Nothing passed in Andhra Pradesh.
- **Decisions.** Ratio to a 2x12 centred moving average with medians over years (the Indian mandi studies' method, made robust); a decision engine shared by sowing and selling (options = months after the reference, each with a yield share and months in store; advise the best gain that clears 3% plus half the months' combined spread); the backtest builds each year's index from months before the decision month only; "same as last year" = the option that paid best the year before. The gate needs only a mean gain against last year's choice (it often picks the same month), and no more losses than wins against selling at harvest. Default N = 3 years because the real files hold 5; raise it to 5 once longer history is in (synthetic false-alarm rate 2.5% to 12.5% over 12 tested years).
- **Starter values with sources** in `storage.go` and PRICES.md "Evidence": loss a month by class, ₹15 a quintal a month, 1% interest, onion 8% a month, yield risk 2% a week and 3% for a variety change (the last two assumptions, not from a source).
- **No dependency added**; `prices` does not import `agro` (crop days and the window come in as values). The command imports `agro` for crop days; its sowing windows are typical southern ones written in the command, not from the recommender.
- **Data.** India Data Portal's Agmarknet files (ODC-By) were streamed and filtered to AP, Telangana and Karnataka for nine crops in the session; nothing real is committed. The fixtures in `prices/testdata/synthetic-bulk` are synthetic; the CEDA header is inferred from its README's description.
- **Mutations planted** (one at a time, `-count=1`, each restored): midrange instead of median over years (caught by `...RecoversAKnownSeason`, `...ShrugsOffOneWildYear...`); ratio filed under the wrong month (`...RecoversAKnownSeason` and three more); filled gap months counted as ratios (`...ShrugsOffOneWildYearAndFillsShortGaps`, after adding its years check: it survived at first); cutoff ignored in the index's filter (`...UsesNothingFromOrAfterItsCutoff`); margin ignored in `decide` (`...ShiftsOnlyWhen...`, `TestGateShowsASeasonOftenAndNoSeasonRarely`); storage loss dropped (`...HoldsOnlyWhenItPays...`); variety risk dropped (`...ShiftsOnlyWhen...`); naive baseline taken from this year (`...AdvisesEachYearOnlyFromTheYearsBefore` and three more); backtest index built from all years (`...PassesASeasonThatHolds...`); gate ignoring the naive baseline (`TestGateNeedsEnoughYears...`, `...PassesASeasonThatHolds...`); perishables held 3 months (three tests); no widening stop at the nearest area (`TestAdviseTimingGivesAdviceOrNothingWithReasons`). The moving average without its half weights at the ends survived: it is an equivalent mutant after the index is rescaled (a 13-month plain average), so no test was added.

### For T145 (the cycle page) and the integrator

- One call: `prices.AdviseTiming(ctx, history, prices.TimingQuery{Crop, State, District, Market, DaysMin, DaysMax, Window: prices.SowingWindow{From, To, Best}, Sown, Today})` returns `TimingAdvice{Commodity, Area, Index, Sowing *SowingAdvice, Selling *SellAdvice, SowingTest, SellingTest, Reasons}`; `Sowing`/`Selling` nil means no timing advice (show `Reasons`). Days come from `agro.Crop.Days`, the window from the recommender's `Window`.
- The sell-or-hold skill can be given `Selling` and `Index`; the API needs a route and the web client a card. FEATURES.md is untouched until then.
