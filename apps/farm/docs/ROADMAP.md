# Farm: the season roadmap

> T141, 2026-11-10. The plan behind it, with the insurance helper, the
> message and sign-in lanes and the pilot (Andhra Pradesh: Kadapa and
> Anantapur, kharif 2027): the owner's roadmap doc, reviewed by two
> independent reviewers. What farmers can do: [FEATURES.md](FEATURES.md#22-season-roadmap).

Once a farmer saves a season plan, each plot has a **roadmap**: every task
of the plan's crop calendar from preparing the land to the harvest, which
the farmer ticks off (done, skipped, not needed) with the day it happened
and what it took, plus tasks of their own. It is rules and dates, no model.

## How it works

- **Tasks are computed, not stored.** `work.Build` runs the crop calendar
  (`apps/farm/calendar`) for the saved plan every time. Each calendar rule
  has a `Key` ("weed-1", "nitrogen-2", "harvest-by", "y2-fertilise-1"), and
  a task's ID is `z<zone>-<crop>-<planned sowing date>-<key>`: the same
  whatever the real sowing date, different for a new plan.
- **What is stored is events** (`work.Event`): done, skipped, not needed,
  reopened, added (a farmer's own task, whose ID is the event's), removed.
  Append-only, with an ID made by whoever records it (the phone, the
  assistant), unique per plot, so a resend records nothing new. An event
  stamped in the future is stamped now. `work.Store` has a
  conformance suite (`worktest.RunStore`), an in-memory store and SQLite
  (`work/sqlite`, table `work_events` in the farm's database file).
- **The real sowing date moves the season.** Marking the sowing task done on
  a day recomputes that zone's calendar from it; tasks after sowing move,
  tasks up to sowing keep their planned dates (a late sowing shows late).
  A sowing more than 120 days from the plan is refused: save a new plan.
  A perennial's tasks run by calendar month, so they keep their dates.
- **Who wins.** Events apply in the order recorded; the first close of a
  task wins until it is reopened. Every event records who (`by`).
- **Nothing is closed by the app.** An open task more than `work.Grace` (7)
  days past its date is *overdue*, for the farmer to say what happened; a
  task done that late is *late*. Days can be backdated, not set in the
  future.
- **A batch is all or nothing**, checked before anything is written; events
  already recorded are skipped before the check, so a phone resending its
  queue after the plan changed is not stuck.

## Where it shows

- **API:** `GET /api/farms/{id}/roadmap`, `POST /api/farms/{id}/roadmap/events`
  (`{"events": [...]}`, up to 500; answers how many were new and the
  roadmap). `/api/config` has `roadmap`.
- **The app:** the plot's *Season work* tab (`web/apps/farm/src/roadmap.tsx`,
  a lazy chunk): what is due in the next two weeks and overdue, or the
  whole season, by phase; Done (Sown for the sowing) with the day, workers
  and cost; Skip with why; Undo; *My own task*. Changes go into a queue on
  the phone first (`roadmap-data.ts`) and are sent when there is a
  connection, in chunks of 200. When the server refuses a change it names
  it (`event_id`), and the phone drops only that one and sends the rest;
  a busy server (408, 425, 429) is tried again later.
- **The assistant:** `get_roadmap` and `record_work` (it says back what it
  will record and records only once the farmer agrees).
- **The weekly digest** (`crop_tasks`) comes from the roadmap: done work is
  left out, the farmer's own tasks are in, dates follow the real sowing.

## How other task sources plug in

A new kind of task (insurance cut-offs and the 72-hour loss report, scheme
and loan dates, an FPO's campaign, a dairy book) is either computed from
data like the calendar's (a function the roadmap calls, composed in
`work.Build`), or recorded as events like the farmer's own. Both kinds
share the event store, the states and the screens. Weather adjustments (move
a spray to a dry day) are a later step: they will be their own events, so
the calendar and the weather never fight over a date.

## Later

Anchors other than the sowing (an observed stage, a fixed cut-off, 72 hours
after an event); weather moving tasks; a farm ID and owner on events once
accounts land (T144); the evening voice diary filling events; the ledger
from actuals; reviewed crop calendars from ANGRAU's almanac for the pilot.
