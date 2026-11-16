---
id: T151
title: Crop insurance on season work - cut-offs, the 72-hour loss rule, alerts, the assistant and the cycle
epic: E25
status: review
depends_on: [T141, T149]
---

# T151: Crop insurance on season work - cut-offs, the 72-hour loss rule, alerts, the assistant and the cycle

## Context

The owner's ask of 2026-11-16: crop insurance must reach the farmer.
Since T149 a region pack holds schemes (PMFBY's guidelines in the base,
with the 72-hour loss notice; Andhra Pradesh's kharif 2026 notice in the
AP overlay), but nothing showed them and nothing alerted on them.
Design: [REGIONS.md](../apps/farm/docs/REGIONS.md),
[ROADMAP.md](../apps/farm/docs/ROADMAP.md#how-other-task-sources-plug-in).

## Scope

- `apps/farm/schemes`: for a plot's pack and its cycle (crops, season,
  first sowing) and today, which schemes apply: covered (the source names
  the crop), may cover (the source names no crops: the state notifies
  them), or not named; the season's enrolment (or sowing) cut-off with
  the days left, passed, no date, or stale (a notice for an earlier
  season: last year's dates, not a live deadline); the loss-notice rule.
  Pure rules, every item with its source, layer and read date.
- `region.Scheme` gets `season` and `year` (the one season a notice is
  for), checked at load; the AP notice says kharif 2026.
- Season work: `addon.Schemes` is a `work.TaskSource` (one "Enrol for
  crop insurance by <date>" task per covering scheme, ID
  `scheme-<id>-<season>-<year>`; a standing "If the crop is damaged:
  report within 72 hours" entry) and a cycle addon (`schemes` on each
  planned cycle). `work.Task` gets `Deadline` (passed, never overdue,
  once its day is gone) and `Standing` (information: never due, overdue,
  counted or closed).
- Alerts: rule `insurance_cutoffs`, built-in automation
  `weekly-insurance` (Mondays): a cut-off within 14 days whose enrolment
  is not ticked is one warning in the inbox, and by the message lanes;
  `automation.Notice.Window` keeps it from being sent again.
- Assistant: read-only `get_schemes`; the `manage-crop` skill answers
  insurance questions from it only.
- API `GET /api/farms/{id}/schemes`; web: *Insurance* under the current
  cycle in the lazy cycle-extras chunk, closed by default; the season
  work tab shows a passed last date as passed and the standing entry
  without buttons.

## Out of scope

- New scheme facts (dates, premiums, more crops): only what the region
  data holds. Updating the data is the `farm-data-updates` skill's job.
- Claims, documents and other schemes of feature 20.
- Rebuilding `apps/farm/webdist/dist` (the integrator rebuilds it).

## Acceptance criteria

- [x] The season's PMFBY cut-off is open weeks ahead, soon from 14 days to the last day, passed after it (with no days left), in the right year for a rabi sowing in January; zaid has no date — test: `schemes_test.TestCutoffBeforeSoonAndPassed`
- [x] Outside AP only the base PMFBY applies, from the base layer, as "may cover" — test: `schemes_test.TestBasePMFBYOnlyOutsideAP`
- [x] In AP the kharif 2026 notice covers the crops it names (from the AP layer) and says groundnut is not named — test: `schemes_test.TestAPNoticeCoversNamedCrops`
- [x] A kharif 2026 notice read for kharif 2027 is "last year's dates, not yet announced for this season", never a deadline; it does not apply to rabi or an earlier year — test: `schemes_test.TestStaleNoticeIsNotALiveDeadline`, `TestOverlaySchemes`
- [x] Every item has its source, read date and layer, and says the read date; every answer has the confirm line — test: the `answer` helper in `schemes_test`
- [x] A scheme year without a season, an unknown season, or a cut-off of another season fails the load with the file and key — test: `region_test.TestInvalidOverlayFailsWithFileAndKey`; the AP notice is kharif 2026 — `TestEmbeddedAndhraPradeshOverlays`
- [x] The roadmap has one enrolment task per covering scheme with a stable ID and a standing loss entry (not counted, cannot be closed); a tick survives a moved sowing — test: `addon_test.TestInsuranceOnTheRoadmap`
- [x] Past its cut-off an open enrolment task is passed, not overdue, and not due; the standing entry is never due — test: `addon_test.TestPassedCutoffIsNotOverdue`
- [x] No task when no scheme covers the crop — test: `addon_test.TestNoTaskWhenNoSchemeCovers`
- [x] Each planned cycle carries its schemes; the current cycle's answer follows the saved plan — test: `addon_test.TestSchemesOnTheCycle`
- [x] A cut-off within 14 days and not ticked is one warning, not five weeks or 15 days ahead, not again a week later, never for a plot that ticked it — test: `watch_test.TestInsuranceCutoffWarnsOnce`
- [x] `get_schemes` only reads, gives exactly the computed answer, and the prompt sends insurance questions to it, never memory — test: `farmagent_test.TestGetSchemes`
- [x] `GET /api/farms/{id}/schemes`: the answer, none without a plan, 404, 503 without the service — test: `farmapi_test.TestSchemesRoute`; another farm's member gets 404 — `farmauth_test.TestEachFarmSeesOnlyItsOwnPlots`
- [x] The web *Insurance* section is closed by default and shows the scheme, cover, last date and days left, the 72-hour rule, the source with its read date, last year's dates as such and "confirm before you rely on this" — test: `cycles.test.tsx` "keeps crop insurance one tap away"; season work shows a passed date as passed and the standing entry with nothing to tick — `roadmap.test.tsx`
- [x] Existing tests pass; the farm web first load stays under its budget

## Notes

- PMFBY's base scheme names no crops (the guidelines leave them to each
  state's notification), so it "may cover" every crop and gives the
  enrolment task wherever its season has a cut-off (kharif 15 July, rabi
  15 December, the latest normal dates; a district's may be earlier, and
  the cut-off note says so). The AP notice names five of its 22 crops, so
  a crop it does not name is "not named: ask", not "not covered".
- The season of a cycle is its first sowing's (real once recorded), by
  the pack's rules; a season over the new year (rabi) belongs to the year
  it starts in. The enrolment task's ID carries the season and year, not
  the sowing date, so a moved sowing keeps its tick.
- The alert's key holds 16 days (`Notice.Window`), past the 14-day
  window, so the weekly run warns once per cut-off; the engine's 6-day
  dedupe stays for every other notice.
- Farm web first load: 159,925 bytes (budget 162,000), measured on a
  build into a scratch folder; the committed dist (not rebuilt here)
  reads 159,932. New UI is in the lazy cycle-extras and roadmap chunks.
- Not done: the web roadmap test and section use fixture JSON, not a
  live server; no browser check was run.
- Mutation self-check (each planted alone, `-count=1`), all caught:
  soon at `< 14` instead of `<= 14` (`TestCutoffBeforeSoonAndPassed`);
  a notice one year old not stale (`TestStaleNoticeIsNotALiveDeadline`,
  `TestOverlaySchemes`); rabi in January in its sowing year
  (`TestCutoffBeforeSoonAndPassed`); no passed state for a deadline
  (`TestPassedCutoffIsNotOverdue`); no notice window
  (`TestInsuranceCutoffWarnsOnce`); a task for a scheme that does not
  cover (`TestNoTaskWhenNoSchemeCovers`); the sowing date in the enrol
  ID (`TestInsuranceOnTheRoadmap`); the standing entry due
  (`TestPassedCutoffIsNotOverdue`); a ticked enrolment still warned
  (`TestInsuranceCutoffWarnsOnce`); the layer always base
  (`TestAPNoticeCoversNamedCrops`, `TestOverlaySchemes`); no read date
  (the `answer` helper).
