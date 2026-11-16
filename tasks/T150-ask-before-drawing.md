---
id: T150
title: Ask before drawing - what to sow at a village with no plot yet
epic: E25
status: review
depends_on: [T146, T149]
---

# T150: Ask before drawing - what to sow at a village with no plot yet

## Context

The owner's ask of 2026-11-16: a farmer who has not drawn any plot yet
can ask "what should I sow, and when, in <village>" (for today, or for a
date they give) and get the same rules-first answer a saved plot gets.
Until now `planner.Recommend` and `recommend_crops` needed a saved farm.
It follows "Basics first" ([CYCLES.md](../apps/farm/docs/CYCLES.md#basics-first-owner-2026-11-15)):
optional, and never in the way of drawing.

## Scope

- `planner.RecommendAt(ctx, At, Ask)`: a point, its place (country,
  state, district; looked up with `planner.WithLocator` when not given)
  and an optional profile, on the same code path as `Recommend` (one
  internal `recommend` for both). The region pack of the place applies
  (the district's sowing windows). Water or soil not given are assumed
  as the cycles assume for a plot with no profile (rain only, soil not
  scored) and returned as named assumptions (`water.rain_only`,
  `soil.unknown`). A place that cannot be looked up gets the base rules
  and a note, never a failure.
- The assistant's `recommend_for_place` (read-only): a place name
  (found with the surveyor's place search) or lat and lng, `around`,
  `crops`, `limit`, `water`, `soil`, `drip`; the answer of
  `recommend_crops` plus the place found, its region layers and the
  assumptions. Offered with the planning tools; the system prompt and
  the `plan-season` skill send a farmer with no plot to it and one with
  a plot to `recommend_crops`.
- `POST /api/recommend` (`farmagent.PlaceAsk`), behind sign-in like every
  API route but `/api/config` and `/api/units`; 404 for an unknown name,
  502 when the geocoder fails, 503 without a planner or (for a name) the
  map.
- Web: in the first run, after the village is found, an optional *What
  can I sow here now?* with the top three (crop, sowing window, harvest)
  and the assumption line, in its own lazy chunk (`sowhere.tsx`).
- `cmd/farm` hands the plots' Nominatim locator to the planner.

## Out of scope

- Asking the water and soil in the first run (the assistant and the API
  take them; the first-run card states the assumption).
- Remembering the answer for the plot drawn after it.
- Rebuilding `apps/farm/webdist/dist` (the integrator rebuilds it).

## Acceptance criteria

- [x] A point in YSR Kadapa gets the district's rainfed groundnut window (sown in June or July, "inside the local sowing window"); the same point placed in another district does not; a place given by the caller is used without a lookup — test: `planner_test.TestRecommendAtUsesTheDistrictsWindows`
- [x] With no profile, a place gets exactly the result a saved plot with no profile there gets, for today and for a date — test: `planner_test.TestRecommendAtIsWhatAPlotThereGets`
- [x] A date the farmer gives moves the answer into a range around it — test: `planner_test.TestRecommendAtAroundADate`
- [x] What the farmer did not say (water, soil) is returned as named assumptions, and nothing else is — test: `planner_test.TestRecommendAtNamesItsAssumptions`
- [x] An irrigated profile changes the result: Kadapa's groundnut in late November is inside the irrigated rabi window on a borewell and scores more than on rain — test: `planner_test.TestRecommendAtIrrigatedChangesTheAnswer`
- [x] A place lookup that fails gives the base rules and a note; a bad position, soil, water, date, limit or crop is invalid; no climate is `agro.ErrNoClimate` — test: `planner_test.TestRecommendAtFailures`
- [x] `recommend_for_place` finds a village by name, answers with the planner's own numbers for that point, its region layers and assumptions; by position with a water source only the soil is assumed — test: `farmagent_test.TestRecommendForPlaceByName`
- [x] The tool refuses no place, an unknown place and a bad soil as invalid input, needs the geocoder only for names — test: `farmagent_test.TestRecommendForPlaceFailures`
- [x] The assistant has the tool, read-only, beside `recommend_crops` — test: `farmagent_test.TestRecommendForPlaceIsOffered`
- [x] `POST /api/recommend` answers a village with no plot and saves nothing; by position with water and soil nothing is assumed — test: `farmapi_test.TestRecommendForAPlace`
- [x] It answers 400, 404, 502 and 503 as above — test: `farmapi_test.TestRecommendForAPlaceFailures`
- [x] It needs a session like other read routes — test: `farmauth_test.TestRecommendForAPlaceNeedsSignIn`
- [x] The first run offers *What can I sow here now?* after the village, shows three crops with sowing window and harvest and the assumption line, makes no plot, and drawing goes on; when it fails drawing still goes on; it is not offered without crop planning — test: `firstrun.test.tsx` ("before drawing, can say what to sow…", "never blocks drawing…", "does not offer…")
- [x] The farm web first load stays under its budget without raising it — `web/scripts/check-size.sh` on a scratch build: 159,935 bytes of 162,000
- [ ] The assistant uses `recommend_for_place` for "no plot, what to sow in a village" on a real model — eval: `farmagent` (`-tags eval`) scenario "no plot, what to sow in a village"; not run here (no model key in this session)

## Notes

- One code path: `Recommend` and `RecommendAt` both build a `spot`
  (point, profile, slope, library) and call `recommend`;
  `TestRecommendAtIsWhatAPlotThereGets` pins that a place and a plot
  there answer alike.
- One picker: `cmd/farm` passes the verifier's Nominatim locator to
  `planner.WithLocator`; no new client, no switch.
- Mutation self-check (each planted alone, `-count=1`), all caught:
  RecommendAt ignoring the looked-up place
  (`TestRecommendAtUsesTheDistrictsWindows`); no assumptions returned
  (`TestRecommendAtNamesItsAssumptions`); the profile not passed to the
  recommender (`TestRecommendAtIrrigatedChangesTheAnswer`); `around`
  dropped (`TestRecommendAtAroundADate`); the tool's default limit
  dropped (`TestRecommendForPlaceFailures`); ErrNoPlace not mapped to
  404 (`TestRecommendForAPlaceFailures`); the web card not cut to three
  (`firstrun.test.tsx`).
- The farm web build gains a `sowhere` chunk (about 1 KB gzipped, lazy);
  the first load moved by 3 bytes.
