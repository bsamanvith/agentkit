---
id: T148
title: Inputs and sprays - a shopping list and a spray schedule from crop, area and method
epic: E25
status: review
depends_on: [T134]
---

# T148: Inputs and sprays - a shopping list and a spray schedule from crop, area and method

## Context

The owner's brief of 2026-11-15: crop cycles at the centre of the farm
app, basics first, everything else optional and additive. Design:
[CYCLES.md](../apps/farm/docs/CYCLES.md), section "Inputs and sprays".

## Scope

- `apps/farm/inputs`: a data file of per-crop nutrition and plant protection, organic and conventional, each value with its source; `Plan` from crop, area, method and sowing date to a shopping list in sold units and a dated schedule.

## Out of scope

- API routes, web and `cmd/farm` wiring (the integration after T145); the agronomist's review.

## Acceptance criteria

- [x] Every crop of the library (39) gets a plan on both methods and both waterings, and every plan starts with the starter-values and follow-the-label notes — test: `inputs_test.TestEveryCropResolves`
- [x] A hectare of rainfed conventional groundnut gets 10:10:45 as DAP, urea (less DAP's N) and MOP, rounded up to a 50 kg DAP bag and a 45 kg urea bag; gypsum at 45 days is bought later — test: `TestGroundnutByHand`
- [x] Quantities scale with the area and round up to sold units (whole packs, kg, 100 g or ml steps, half tonnes), never down — test: `TestScalesWithArea`, `TestSold`
- [x] An organic plan holds no synthetic product: not to buy, not scheduled, not as a remedy; data putting one in the organic route is refused — test: `TestOrganicHasNoSynthetic`, `TestLoadRefuses`
- [x] Curative products are not on the shopping list but under "only if you see", with signs, threshold, the quantity for one spray of the field, water per acre and PHI — test: `TestCurativeOnlyIfSeen`
- [x] No active on the embedded banned and restricted list (with its source and dates) is in the data; loading one is refused — test: `TestNoBannedActive`
- [x] Every value names a source with a URL and a place in it; crops without values of their own are marked default — test: `TestEveryValueHasSource`
- [x] Data with an unknown source, a crop outside the library, splits off 100 % or an unknown calendar key is refused — test: `TestLoadRefuses`
- [x] Schedule dates follow the sowing date with stable keys; each entry's stage is the calendar's, and entries on calendar tasks fall on their days — test: `TestScheduleFollowsSowing`
- [x] No spray is scheduled after the first harvest day less its PHI (it is left out with a note), and every remedy with a PHI gives its last spray day — test: `TestPHI`
- [x] Several fields (an intercrop, or two crops) add up per product before rounding — test: `TestFieldsAddUp`
- [x] Requests with no fields, a bad method, watering, area or date are refused; an unknown crop is unknown — test: `TestRejects`
- [ ] An agronomist's review of the values (out of scope: listed by `Book.Values`)

## Notes

- Design note: [INPUTS.md](../apps/farm/docs/INPUTS.md). Sources were read
  on 2026-11-15; ANGRAU, ICAR, NIPHM and PPQS sites could not be reached,
  so TNAU's 2020 guides and the 2019 CIB&RC list (Odisha's copy) are the
  base, and Wikipedia's copy of the PPQS list is the banned list.
- 34 crops have values of their own; mustard, quinoa, chia, dragon fruit
  and avocado take their kind's feeding (marked default), with no seed rate
  and no pesticide dose. Guava and coconut have no first-year fertiliser
  in the sources: manure only. There is no citrus in the library.
- Everything restricted on any crop, or proposed for a ban (the 2020 draft
  of 27), is kept out entirely: quinalphos, mancozeb, carbendazim, thiram,
  deltamethrin and the like.
- Mutation self-check: rounding down (`TestSold`, `TestScalesWithArea`),
  synthetic products on the organic route (`TestOrganicHasNoSynthetic`),
  dropping the PHI check (`TestPHI`), forgetting DAP's nitrogen
  (`TestGroundnutByHand`), not adding fields up (`TestFieldsAddUp`),
  turning off the banned check (`TestNoBannedActive`), a wrong stage
  (`TestScheduleFollowsSowing`): each caught.

## Joined (branch `wt-join`, 2026-11-15)

T145 to T148 were joined into one path in `wt-join`; status stays
`review` until that branch merges. What the join added for this task:

- `cycle/addon.Inputs` plans each cycle (areas, intercrops by rows,
  method, water from the plot's profile, sowing) and the cycle page shows
  *What to buy* and *Feeding and sprays* with both notes
  (`TestInputsPlanEachCycle`, `cycles.test.tsx`).
- `cycle/addon.Roadmap` gives Season work the schedule: calendar keys
  enrich the calendar's tasks, a feeding within `work.NearFeed` (21) days
  of a calendar feeding becomes it (no twins), the rest are tasks of
  their own; ticks survive a moved sowing and a change of method
  (`TestRoadmapCarriesTheSchedule`, `TestTaskSources`).
- The assistant's `get_inputs` tool gives the plan for "what should I
  buy" (`TestCycleTools` in `farmagent`).
- `Amount` shows 1,000 g or more in kg and 1,000 ml or more in litres.


## Refined (branch `wt-refine`, 2026-11-15)

Looked at as a farmer near Kadapa on 15 November, then reviewed:

- Feed titles say what and why from their doses ("First nitrogen feed:
  urea, 21.7 kg"; preventive steps "Against fall armyworm: …") —
  `TestFeedTitlesSayWhatAndWhy`.
- One number: `inputs.Amount` (whole from 100, one decimal below) is
  used by the schedule, titles and Season work; the web says the buy
  rounded up with the need beside it (`TestOneNumberForAQuantity`).
- Organic never names a synthetic product: the crop calendar's feeding
  text is method-neutral (`TestCalendarNamesNoSyntheticProduct`, caught
  the old "Put urea" text), and an organic cycle's plan, Season work and
  Due list are checked for every synthetic name
  (`TestOrganicNamesNoSyntheticProduct`).
- The feed caution "beside the rows" is no longer put on work before
  sowing (ploughing in manure).

## Review fixes (branch `wt-fix`, 2026-11-15)

- **A done tick moved to another task on a method switch** (blocking).
  A feeding within `work.NearFeed` of a calendar feeding took over the
  calendar task's ID, so organic maize's ticked jeevamrutham showed as
  the conventional urea feed done. It now takes the calendar task's
  place under its own ID: the urea feed is open after the switch and the
  jeevamrutham still done when switching back.
  `TestRoadmapCarriesTheSchedule` checks what a tick is attached to;
  `TestTaskSources` updated.
- **A planted bug in `nearFeed` survived** (blocking): removing
  `taken[i] ||` passed every test. New invariant: every inputs schedule
  entry is on the roadmap exactly once by its ID, both methods, ten crops
  (`TestEveryScheduleEntryOnTheRoadmapOnce`); the plant now fails it.
- **DAP's nitrogen was counted only within its split** (blocking): the
  surplus now comes off later splits' urea, and where DAP for all the
  phosphate would exceed the season's N, SSP gives the rest.
  `TestNutrientsAddUp` (every crop, both waterings, against the data
  read on its own). Per hectare, N/P2O5/K2O delivered, before → after
  (recommended): watermelon irrigated 76.5 → 55/55/55 (55/55/55); ginger
  irrigated 94.6 → 75/50/50 (75/50/50); rice rainfed 59.8 → 50/25/25
  (50/25/25); onion irrigated 108.7 → 100/150/75 (100/150/75); soybean
  irrigated 31.3 → 20/80/40 (20/80/40: DAP 111.1 kg + SSP 180.6 kg).
- `dose()` took per-hectare values as per plant on trees (no data hit
  it): callers now say which; `Prevent.Per` is used ("ha" or "tree") and
  checked on load (`TestPerHectareOnATree`, `TestLoadRefuses`).
- Load refuses data without urea, DAP, SSP, MOP or gypsum, or with their
  nutrient at 0 (`TestLoadRefuses`).
- Banana's biofertilisers (AM fungi 250 g, PSB, Azospirillum and
  Trichoderma 50 g a plant) were re-read in the TNAU Horticulture guide
  2020, p. 8: that is what it says. Kept, each with a re-checked note.
