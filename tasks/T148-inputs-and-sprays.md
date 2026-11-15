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
