---
id: T145
title: Crop cycles - a suggested line and the farmer's own, linked, three at most
epic: E25
status: review
depends_on: [T141]
---

# T145: Crop cycles - a suggested line and the farmer's own, linked, three at most

## Context

The owner's brief of 2026-11-15: crop cycles at the centre of the farm
app, basics first, everything else optional and additive. Design:
[CYCLES.md](../apps/farm/docs/CYCLES.md), section "Cycles".

## Scope

- `apps/farm/cycle`: the cycle model, the suggested line (linked through harvest, turnaround and rotation), the farmer's own line, the planned cycle, checks of own cycles; a store with a conformance suite, in memory and SQLite.
- Routes, the assistant's tools, `cmd/farm` wiring, the *Cycles* view on a plot (a lazy chunk).

## Out of scope

- Inputs and sprays (T148), sell timing (T147), drawing (T146).

## Acceptance criteria

- [x] Nothing planted, nothing entered: the suggested line is the plan, three cycles, each free from the one before's harvest plus the turnaround, of another family than the crop before - test: `cycle.TestSuggestedLineIsThePlan`
- [x] Among crops that fit alike, the rotation picks another family, a legume after a cereal, and the same family only when nothing else fits - test: `cycle.TestRotationPicksAmongEquals`, `cycle.TestRotationAfterCereal`
- [x] The current cycle's own plan is the plot's saved plan; changing its crop or date recomputes every suggestion after it - test: `cycle.TestChangingACycleRecomputesTheNext`
- [x] The farmer's own later cycles are never changed, only checked: a sowing before the cycle before ends is a warning naming both dates; later suggestions follow the own cycle - test: `cycle.TestOwnCyclesAreCheckedNotChanged`
- [x] Same family twice and a planned date already past are warnings on the farmer's own cycle - test: `cycle.TestOwnCycleWarnings`
- [x] Accepting a suggestion takes it in one call: at the current cycle it is saved as the plot's plan (the roadmap has it), later it is the farmer's own and stays; clearing goes back to the suggestion - test: `cycle.TestAccept`
- [x] A real sowing (the roadmap's Sown tick, or a typed date, also recorded on the roadmap) becomes the date and moves the harvest and the next cycle; a future, malformed or year-old date is refused - test: `cycle.TestActualSowingMovesTheLine`
- [x] A typed sowing with no saved plan saves the suggestion sown that day; a typed date counts only for the plan it was typed for - test: `cycle.TestSownWithoutAPlan`
- [x] At most three cycles; later cycles can be turned off and on, keeping the farmer's own; positions out of range are refused - test: `cycle.TestThreeAtMostAndFutureOff`
- [x] Organic or conventional per cycle (conventional by default); an unknown method, an empty or double change, and clearing the current cycle are refused - test: `cycle.TestMethod`
- [x] An own plan with errors is refused with its report and stores nothing - test: `cycle.TestOwnPlanWithErrors`
- [x] Without a climate nothing is suggested, the farmer's own cycles still show, a note says why, nothing fails - test: `cycle.TestNoClimateNeverBlocks`
- [x] A perennial holds the land: the line stops after it - test: `cycle.TestPerennialEndsTheLine`
- [x] Advancing makes the next planned cycle the saved plan, the current crop the previous one, and moves own cycles up - test: `cycle.TestAdvance`
- [x] Addons ride along on each planned cycle; a failing one is a note, never a failure - test: `cycle.TestAddons`
- [x] A read-only scope reads and changes nothing; an unknown plot is not found - test: `cycle.TestReadOnly`
- [x] Two writes at once both land (a stale record is read again) - test: `cycle.TestConcurrentWritesAllLand`
- [x] Every Store starts empty per plot, keeps every field, bumps the version, refuses a stale or ahead version without changing anything, shares no memory, and lets one of concurrent writers at a version win - test: `cycletest.RunStore` via `cycletest.TestMemory`, `sqlite.TestStore`, `sqlite.TestReopen`
- [x] Routes: read, accept (saved as the plan), own later cycle, clear, sown, settings, advance; 400, 422, 404 and 503 where due - test: `farmapi.TestCycleRoutes`
- [x] Assistant tools: get_cycles reads, set_cycle writes (accept, own crop and date, one area, method, sown) and refuses what it cannot do as invalid input - test: `farmagent.TestCycleTools`
- [x] The Cycles tab shows the current cycle first and takes the suggestion in one tap, records a sowing already done, shows later cycles (one tap away) side by side with the farmer's own and its warnings, goes back to the suggestion, chooses organic, turns later cycles off and shows what the server refused - test: `cycles.test.tsx`
- [x] The tab is a lazy chunk; the first load stays within `FARM_WEB_BUDGET_KB` - check: `web/scripts/check-size.sh` on a `make farm-web` build (161,991 of 162,000 bytes)

## Notes

### Design (design-module)

- **Layer:** `apps/farm/cycle` sits above `planner`, `work`, `farm` and
  `agro` (it imports them; nothing below imports it); `farmapi`,
  `farmagent` and `cmd/farm` import it. `cycle/sqlite` is the only
  importer of the driver (archtest line added); `cycle/cycletest` has
  the in-memory store and `RunStore`.
- **API:** `cycle.New(*planner.Planner, Store, ...Option)` with
  `WithRoadmap(Roadmap)` (`*work.Service` is one) and
  `WithAddons(...Addon)`. `Service.Line`, `Set(pos, Change{Accept, Plan,
  Clear, Method})`, `SetSown(day)`, `SetFuture(on)`, `Advance`. The rules
  alone: `cycle.Compute(Input) (Line, error)`, deterministic.
  `Line.Cycles[i]` is a `Position` with `Suggested`, `Own`, `Planned`
  (own else suggested), `Checks`, `Addons`; a `Cycle` has `Areas` (crop,
  `AreaHa`, sow and its kind, harvest), `Method`, `Origin`, `Season`.
- **Errors:** `ErrInvalid` (400), `ErrConflict` (a store's record changed;
  the service reads again up to three times, then 409), a plan with
  errors is a `*planner.PlanError` (422), `farm.ErrNotFound`,
  `farm.ErrReadOnly` from the plot's scope.
- **What is stored:** one record per plot (`crop_cycles` table in the
  farm's database file): the farmer's own plans at positions 1 and 2, a
  method per position, a typed sowing date with the key of the plan it
  is for, future planning off; with a version for compare-and-set.
  Suggestions are never stored: they are computed on every read, which
  is how a change to an earlier cycle recomputes them. The current own
  cycle is the plot's saved plan (`farm.Farm.Plan`), not a copy.
- **Process:** one process (the farm binary); no remote client.

### Decisions

- Rules: turnaround 15 days; a suggested sowing at most 240 days after
  the land is free (a rainfed plot waits for the rains; 150 left a
  rainfed Deccan plot with no next cycle); rotation adjusts the
  recommender's score (same family -20, a legume after a non-legume +10,
  a cereal after a legume +5) and a day of waiting costs a tenth of a
  point; perennials are never suggested as a cycle and stop the line.
- A suggestion keeps the areas of the cycle before (or the farmer's own
  at the same place, so they compare); the first one covers the plot.
- A cycle is `actual` once any area's sowing is recorded. The roadmap's
  Sown wins over a typed date; a typed date is also recorded on the
  roadmap (reopen then done), so they agree, and is refused there past
  120 days from the plan as the roadmap refuses it.
- Accepting a later suggestion freezes it as the farmer's own: it is
  then checked, not moved, when earlier cycles change (the design's
  "never silent changes").
- `Advance` is explicit (the app never rolls a cycle over by itself);
  the tab offers it from two weeks before the expected harvest.
- The tab shows when the planner is on (the farm binary always wires
  cycles with it); `/api/config` also has `cycles` for other clients.
  The tab entry costs 158 bytes of first load, which leaves 9 bytes.

### Mutations (each planted alone, then restored)

All caught: turnaround dropped (most tests, via the invariants);
suggestion beating the farmer's own (TestAccept and others); the
roadmap's sowing ignored (TestActualSowingMovesTheLine); a typed sowing
applied to any plan (TestSownWithoutAPlan); overlap never warned
(TestOwnCyclesAreCheckedNotChanged); future off ignored
(TestThreeAtMostAndFutureOff); the legume bonus inverted
(TestRotationAfterCereal); the memory and SQLite version checks dropped
(TestMemory, TestStore); the method on the wrong slot (TestMethod);
advance not shifting (TestAdvance); a typed sowing not recorded on the
roadmap (TestActualSowingMovesTheLine). The same-family penalty set to
0 survived the first tests: TestRotationPicksAmongEquals was added and
catches it.

### Not done

- No independent `review` run (no second agent CLI in this worktree's
  session); the gates and the mutations above are all that checked it.
- Plot history (finished cycles) is not kept: `Advance` drops the old
  current cycle except as the profile's previous crop.

### For T147 and T148

- **T148 (inputs):** implement `cycle.Addon` (`Name() string`,
  `For(ctx, farm.Farm, cycle.Cycle) (any, error)`): for each
  `c.Areas[i]` call `inputs.Plan(area.Crop, area.AreaHa, c.Method,
  area.Sow)`; wire it in `cmd/farm` with `cycle.WithAddons(...)` next to
  `cycle.New`. The result is in `Position.Addons["<name>"]`; the web
  reads `addons` in `cycles-data.ts` (typed `unknown` now).
- **T147 (prices):** harvest-month advice is an `Addon` the same way
  (`c.Areas[i].Crop`, `c.Harvest`). Preferring a sowing date by its
  harvest month inside the safe window would go in `Input.suggest`
  (pick among the recommender's `Calendar` dates), not built.

## Joined (branch `wt-join`, 2026-11-15)

T145 to T148 were joined into one path in `wt-join`; status stays
`review` until that branch merges. What the join added for this task:

- The addons are wired in `cmd/farm` (`apps/farm/cycle/addon`: `Inputs`,
  `Timing`); an addon answering nil is left out, a failing one adds a
  note naming it (its `Label`), never an error on the line
  (`TestAddons`).
- *I already sowed on…* with a day more than `work.MaxSowShift` (120)
  days from the saved plan saves the plan again with that day instead of
  failing (`TestActualSowingMovesTheLine`); the roadmap events it records
  carry the day in their IDs, so two records in one instant are two.
- Still open: the cycle-0 plan and the cycle record are two writes, not
  one transaction (a crash between them leaves the record's `SownFor`
  pointing at an older plan, which is then ignored).


## Refined (branch `wt-refine`, 2026-11-15)

Looked at as a farmer near Kadapa on 15 November, then reviewed:

- The suggestion prefers a crop that suits sowing soon: within
  `SoonDays` (42) at `SoonScore` (`agro.Good`), with `ReadyDays` (7) to get
  ready for this cycle when a date that late suits; the reason says what
  it beats ("…would leave the land empty until July"). When nothing does,
  `Position.Rest` says so ("Nothing suits sowing on rainfed land before
  May": the first month anything suits) and the page leads with it, what
  to do meanwhile and what comes next. `Line.Assumed` marks a plot with
  no water and no soil given; the page offers to set both in one tap.
  Tests: `soon_test.go` (the recommender's own calendar as the oracle),
  `cycles.test.tsx`.
- Taking a suggestion no longer shows "we would have suggested" the same
  crop a week earlier (`TestTakenSuggestionIsStillTheSuggestion`); after
  a recorded sowing the hint is gone, and the work from before the
  sowing is `work.Task.Passed` (not overdue, not due, no record made up;
  `TestRecordedSowingPassesTheWorkBefore`). Land preparation before a
  sowing still ahead is not overdue (`TestWorkBeforeASowingAheadIsNotOverdue`).
- Mutations checked: the old `own == nil` ready rule, no soon pass.
- Still open: the rest month is the first month anything suits, which can
  be before the suggested crop's (May, for finger millet in July).

## Review fixes (branch `wt-fix`, 2026-11-15)

An independent review of the cycle work found, and this branch fixed
(each with the failing test first):

- **Advance could skip a cycle on a save conflict** (blocking). `update`
  retried the whole edit after `ErrConflict`, but Advance, SetSown and
  Set at 0 had already written the farm (profile, plan, roadmap): the
  retry advanced again (maize → chickpea → sorghum), and a 409 left the
  plan replaced. Edits now only store the farm writes in the record
  (`Record.Pending`, with the plan they were made against and the slots
  before); the farm is written after the record's Put wins. A crash
  between is finished by the next write or read, dropped if the plan was
  changed since, and the slots go back if the farm refuses it.
  `TestConflictsApplyOnceOrNotAtAll` (conflict once; always: nothing
  changed, 409), `TestAWriteCutShortIsFinished`; the store suite keeps
  `Pending`.
- **A done tick moved to another task on a method switch** (blocking):
  see T148; `work.merge` now keeps the inputs feeding's own ID.
- **Advance had no server-side guard**: refused before
  `cycle.AdvanceDays` (14) before the harvest; `Line.AdvanceFrom` gives
  the page the same day; the page asks in place before moving on and
  shows the step with later cycles off (`TestAdvanceOnlyNearHarvest`,
  `TestCycleRoutes`, `cycles.test.tsx` "moving on").
- **A far sowing dropped the ticks**: saving the plan again renames the
  tasks; the farmer's closes are carried to the same tasks of the new
  plan with event IDs made from what they record
  (`TestFarSowingKeepsTheTicks`).
- **A crop missing from the plot's list failed the line (400)**: the
  cycle shows with a `crop.unknown` warning and no harvest, nothing after
  it (`TestUnknownCropIsAWarning`).
- **`set_cycle` with `sown` ignored the other fields**: refused now
  (`TestCycleTools`).
- **Freehand accepted a self-crossing loop**: refused at once with a
  plain message (`draw.test.ts`, `editor.test.tsx`).
- Mutations checked: retry without the pending step (the conflict tests
  fail), healing off in Line and the From check off
  (`TestAWriteCutShortIsFinished` fails), the window check off
  (`TestAdvanceOnlyNearHarvest` fails).
