---
id: T141
title: Season roadmap - a plot's tasks from its plan, ticked off by the farmer, offline-ready
epic: E25
status: review
depends_on: [T139]
---

# T141: season roadmap

## Context

The owner asked (2026-11-10) for a roadmap of what to do and when once the
crops are locked in, with a task framework that later sources (insurance,
schemes, an FPO's campaigns) plug into, and "not everything AI". The plan
was reviewed by two independent reviewers; their main change is built
here: calendar tasks are computed with stable IDs and only events are
stored, append-only, so offline phones and several family members work.
Design: [ROADMAP.md](../apps/farm/docs/ROADMAP.md). Pilot: Andhra Pradesh
(Kadapa, Anantapur), kharif 2027.

## Scope

- `apps/farm/calendar`: a stable `Key` per rule.
- `apps/farm/work`: `Build` (roadmap from plan and events), `Service`
  (`Roadmap`, `Record`, `Due`), `Store` with `worktest.RunStore`, an
  in-memory store, `work/sqlite`.
- `apps/farm/watch`: the weekly `crop_tasks` digest from the roadmap.
- `apps/farm/farmapi`: `GET /api/farms/{id}/roadmap`,
  `POST /api/farms/{id}/roadmap/events`, `roadmap` in `/api/config`.
- `apps/farm/farmagent`: `get_roadmap`, `record_work`.
- `cmd/farm` wiring; the *Season work* tab with an offline queue.

## Out of scope

- Anchors other than sowing (stage, fixed date, event), weather moving
  tasks, insurance and scheme sources, owner IDs on events (accounts,
  T144), the ledger from actuals, voice.

## Acceptance criteria

- [x] Calendar keys are unique per zone and do not change with the sowing date - test: `calendar.TestKeysUniqueAndStable`
- [x] The roadmap has every calendar task, open, in date order, by phase - test: `work.TestRoadmapFromPlan`
- [x] Marking the sowing done moves later tasks and keeps their IDs; a late sowing shows late - test: `work.TestSowingDateMovesLaterTasks`
- [x] Resending events records nothing new and never rewrites what was recorded - test: `work.TestResendChangesNothing`
- [x] The first close wins until reopened - test: `work.TestFirstCloseWinsUntilReopened`
- [x] The app never closes a task: past its date it is overdue; done that late it is late - test: `work.TestOverdueStaysOpen`
- [x] Farmer tasks can be added, done (in the same batch) and removed; calendar tasks cannot be removed - test: `work.TestFarmerTasks`
- [x] A batch with any invalid event records nothing - test: `work.TestInvalidRecordsNothing`
- [x] A new plan starts clean - test: `work.TestNewPlanStartsClean`
- [x] Due lists open tasks in a stretch; a plot with no plan has none - test: `work.TestDue`
- [x] Every Store records an ID once (also concurrently), keeps every field, returns per farm in order - test: `worktest.RunStore` via `worktest.TestMemory`, `sqlite.TestStore`, `sqlite.TestReopen`
- [x] The weekly digest leaves out done work and includes the farmer's tasks - test: `watch.TestWeeklyTasksFollowTheRoadmap`
- [x] Routes: read, record, resend adds nothing, bad events 400, 503 without the service - test: `farmapi.TestRoadmapRoutes`
- [x] Assistant tools: get_roadmap reads, record_work writes and refuses unknown tasks - test: `farmagent.TestRoadmapTools`
- [x] Overdue and late start the day after the 7-day grace - test: `work.TestGraceBoundary`
- [x] A sowing up to 120 days from the plan is taken, 121 refused - test: `work.TestSowingWindow`
- [x] An event stamped in the future is stamped now, so the first recorded wins - test: `work.TestFutureStampIsNow`
- [x] A refused event is named (index and ID); an added task cannot take a task's ID; a removed task stays removed - test: `work.TestRefusalsAndRemoval`, `farmapi.TestRoadmapRoutes`
- [x] A perennial's tasks keep their keys and events when the planting moves across a year end - test: `work.TestPerennialPlantingAcrossYearEnd`
- [x] Event IDs are unique per farm, not across farms - test: `worktest.RunStore` ("an event ID is per farm")
- [x] A retried record_work add does not add the task twice - test: `farmagent.TestRoadmapTools`
- [x] The tab lists the season, records the sowing day, keeps changes on the phone offline, sends the queue in chunks of 200, keeps it on 408/425/429, and drops only the change the server names - test: `roadmap.test.tsx`

## Notes

- Decisions: tasks up to sowing keep planned dates after a late sowing
  (they are done by then, and the sowing shows late); a sowing more than
  120 days from the plan is refused (save a new plan); `Grace` is 7 days
  for every kind for now; IDs `[A-Za-z0-9_.:-]{1,64}`.
- Perennials: their tasks run by calendar month, so a moved planting
  records the real date but does not recompute their calendar (the keys
  count years from the planned date and would shift across a year end).
- Review (independent reviewer, 2026-11-10) found: the archtest allowlist
  entry, one refused event wiping the phone's queue, event IDs global
  across farms, perennial keys shifting, untested boundaries. All fixed.
- Mutations (each caught, then restored): `today.After(due)` to
  `!today.Before(due)` (TestGraceBoundary); dropping the future-`At` clamp
  (TestFutureStampIsNow); `*e.Zone < WholePlot` to `< 0`
  (TestGraceBoundary); recomputing perennials
  (TestPerennialPlantingAcrossYearEnd); `p.DAS <= 0` to `< -1`
  (TestSowingDateMovesLaterTasks); 120 to 150 days (TestSowingWindow).
- The *Season work* tab is a lazy chunk; the first load does not grow.
- For T142-T144: events carry `by` now; when accounts land, add the
  account and farm IDs to `Event` and check membership in `Record`. The
  message lanes can send due and overdue tasks from `work.Service.Due`.
