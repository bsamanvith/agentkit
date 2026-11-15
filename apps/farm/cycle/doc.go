// Package cycle is a plot's crop cycles: one crop season on one plot (what
// grows in each area, when it is sown and when it comes off), up to three
// in a line, the current one and the next two.
//
// Each plot has two lines side by side. The suggested line is computed by
// rules, never stored: from the plot's land and water profile and its
// climate, each cycle starts after the one before is harvested plus a
// turnaround to prepare the land, and is chosen with the cycle before as
// its last crop (not the same family twice, a pulse after a cereal), with
// the crop planning recommender (package agro). The farmer's own line is
// what they set: the current cycle's own plan is the plot's saved plan
// (farm.Farm.Plan, so the season roadmap, weather advice and prices keep
// working unchanged); later own cycles are kept in a [Store]. The planned
// cycle at each position is the farmer's own where there is one, else the
// suggestion, and the next position is worked out from the planned one, so
// changing a crop or a date recomputes every suggestion after it. The
// farmer's own later cycles are never changed by the app, only checked
// against the cycle before, with warnings ([Check]).
//
// A real sowing date beats the planned one: the Sown tick of the season
// roadmap (package work), or a date the farmer types ([Service.SetSown]),
// becomes the current cycle's date and moves what follows.
//
// Use [Service]: [Service.Line] reads a plot's line, [Service.Set] accepts
// a suggestion, sets an own plan or a method, [Service.SetSown],
// [Service.SetFuture] and [Service.Advance] do the rest. [Compute] is the
// rules alone. Other features hang off a planned cycle through an
// [Addon] (a shopping list, a spray schedule, harvest-month advice): its
// result rides along in the line, and a failing one never fails it.
//
// Errors: [ErrInvalid] for a request that cannot be done (a position past
// [MaxCycles], an unknown method, nothing to accept), a plan with errors is
// a *planner.PlanError (which matches agro.ErrInvalid), farm.ErrNotFound
// and farm.ErrReadOnly come from the plot's scope, and a Store returns
// [ErrConflict] when a record changed under a write.
//
// Design: apps/farm/docs/CYCLES.md, section "Cycles".
package cycle
