// Package work is a plot's season roadmap: the tasks of its saved crop
// plan from preparing the land to the harvest, with what the farmer did
// about each, and their own tasks beside them.
//
// Tasks are not stored. Calendar tasks are recomputed from the plan
// (package calendar) every time they are read, each with a stable ID made
// of the zone, the crop, the planned sowing date and the calendar rule's
// key. What is stored is what happened, as events: done, skipped, not
// needed, reopened, a task the farmer added or removed. Events are
// append-only and carry an ID made by whoever recorded them (the phone,
// offline), so sending the same event twice changes nothing and a phone
// can queue events without a connection and send them later.
//
// Marking the sowing task done on a day is the real sowing date: the
// zone's calendar is recomputed from it, so every later task moves with
// it, while the task IDs (made from the planned date) stay the same and
// keep their events. A task is never closed by the app itself: past its
// date and a grace period it is shown as overdue, for the farmer to say
// what happened, and they can record it on an earlier day.
//
// When two people record the same task, the first event wins and later
// ones are kept but do not change it; reopening the task clears it.
//
// See apps/farm/docs/ROADMAP.md.
package work
