package cycle_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/cycle"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/planner"
	"github.com/bsamanvith/agentkit/apps/farm/work"
)

var ctx = context.Background()

// line reads the plot's line and checks what holds for every line.
func (w *world) line(t *testing.T) cycle.Line {
	t.Helper()
	l, err := w.cycles.Line(ctx, w.plot)
	if err != nil {
		t.Fatal(err)
	}
	invariants(t, l)
	return l
}

// invariants: at most three cycles, in order; the planned one is the
// farmer's own where there is one, else the suggestion; each cycle after
// the first starts no earlier than the one before comes off plus the
// turnaround; dates parse and harvests follow sowings.
func invariants(t *testing.T, l cycle.Line) {
	t.Helper()
	if len(l.Cycles) > cycle.MaxCycles || (!l.Future && len(l.Cycles) > 1) {
		t.Errorf("%d cycles (future %v)", len(l.Cycles), l.Future)
	}
	var prev *cycle.Cycle
	for i, p := range l.Cycles {
		if p.Position != i {
			t.Errorf("cycle %d has position %d", i, p.Position)
		}
		want := p.Suggested
		if p.Own != nil {
			want = p.Own
		}
		if p.Planned != want {
			t.Errorf("cycle %d: planned is not own-else-suggested", i)
		}
		if p.Suggested != nil {
			if p.Suggested.Origin != cycle.Suggested {
				t.Errorf("cycle %d: suggestion's origin %s", i, p.Suggested.Origin)
			}
			if prev != nil && p.Suggested.Sow < plus(prev.Harvest, cycle.Turnaround) {
				t.Errorf("cycle %d: suggested sowing %s before the cycle before ends (%s) plus the turnaround", i, p.Suggested.Sow, prev.Harvest)
			}
			if p.Suggested.Sow < p.From {
				t.Errorf("cycle %d: suggested sowing %s before the land is free (%s)", i, p.Suggested.Sow, p.From)
			}
		}
		if p.Own != nil && p.Own.Origin != cycle.Farmer {
			t.Errorf("cycle %d: own's origin %s", i, p.Own.Origin)
		}
		if prev != nil && p.From < plus(prev.Harvest, cycle.Turnaround) && p.From != l.Today {
			t.Errorf("cycle %d: free from %s, before %s plus the turnaround", i, p.From, prev.Harvest)
		}
		if c := p.Planned; c != nil {
			if len(c.Areas) != len(c.Plan.Zones) || c.Harvest <= c.Sow {
				t.Errorf("cycle %d: %d areas for %d zones, sow %s, harvest %s", i, len(c.Areas), len(c.Plan.Zones), c.Sow, c.Harvest)
			}
		}
		prev = p.Planned
	}
}

func plus(day string, n int) string {
	d, _ := time.Parse(agro.DateLayout, day)
	return d.AddDate(0, 0, n).Format(agro.DateLayout)
}

func (w *world) savePlan(t *testing.T, crop, sow string) {
	t.Helper()
	f, _ := w.svc.Get(ctx, w.plot)
	if _, err := w.plan.SavePlan(ctx, w.plot, agro.Plan{Zones: []agro.Zone{{Crop: crop, Sow: sow, Ring: f.Boundary.Ring()}}}); err != nil {
		t.Fatal(err)
	}
}

func family(t *testing.T, w *world, crop string) string {
	t.Helper()
	c, err := w.plan.Library().Crop(crop)
	if err != nil {
		t.Fatal(err)
	}
	return c.Family
}

// Nothing planted, nothing entered: the suggested line is the plan, three
// cycles linked through harvest and turnaround, rotated by family.
func TestSuggestedLineIsThePlan(t *testing.T) {
	t.Parallel()
	w := setup(t)
	l := w.line(t)
	if len(l.Cycles) != cycle.MaxCycles || !l.Future {
		t.Fatalf("%d cycles; want %d", len(l.Cycles), cycle.MaxCycles)
	}
	prevFamily := family(t, w, "maize") // the profile's previous crop
	for i, p := range l.Cycles {
		if p.Own != nil || p.Planned == nil || p.Planned.Origin != cycle.Suggested {
			t.Fatalf("cycle %d: %+v; want the suggestion planned", i, p)
		}
		if f := family(t, w, p.Planned.MainCrop()); f == prevFamily {
			t.Errorf("cycle %d: %s is %s again", i, p.Planned.MainCrop(), f)
		}
		prevFamily = family(t, w, p.Planned.MainCrop())
		if len(p.Planned.Reasons) == 0 || p.Planned.Score < agro.Fair {
			t.Errorf("cycle %d: score %d, reasons %v", i, p.Planned.Score, p.Planned.Reasons)
		}
	}
	if l.Cycles[0].From != l.Today {
		t.Errorf("the first cycle is free from %s; want today %s", l.Cycles[0].From, l.Today)
	}
	if want := plus(l.Cycles[0].Planned.Harvest, cycle.Turnaround); l.Cycles[1].From != want {
		t.Errorf("cycle 2 free from %s; want %s (harvest plus turnaround)", l.Cycles[1].From, want)
	}
}

// The rotation: after a cereal the suggestion prefers a legume; after the
// same family it marks the crop down. Compute is deterministic.
func TestRotationAfterCereal(t *testing.T) {
	t.Parallel()
	w := setup(t)
	w.savePlan(t, "sorghum", "2026-06-20")
	l := w.line(t)
	next := l.Cycles[1].Planned
	if next == nil || family(t, w, next.MainCrop()) != "Fabaceae" {
		t.Fatalf("after sorghum: %+v; want a legume", next)
	}
	found := false
	for _, r := range next.Reasons {
		found = found || r.ID == "rotation.pulse"
	}
	if !found {
		t.Errorf("reasons %v; want rotation.pulse", next.Reasons)
	}
	again := w.line(t)
	if !reflect.DeepEqual(l, again) {
		t.Error("two reads of the same plot differ")
	}
}

// The current cycle is the saved plan; changing it recomputes every
// suggestion after it.
func TestChangingACycleRecomputesTheNext(t *testing.T) {
	t.Parallel()
	w := setup(t)
	w.savePlan(t, "green_gram", "2026-06-20")
	short := w.line(t)
	if o := short.Cycles[0].Own; o == nil || o.MainCrop() != "green_gram" || o.Origin != cycle.Farmer {
		t.Fatalf("cycle 1 = %+v; want the saved plan as the farmer's own", short.Cycles[0])
	}
	w.savePlan(t, "cotton", "2026-06-20")
	long := w.line(t)
	if long.Cycles[1].From <= short.Cycles[1].From {
		t.Errorf("after cotton cycle 2 is free from %s; after green gram %s: want later", long.Cycles[1].From, short.Cycles[1].From)
	}
	if want := plus(long.Cycles[0].Planned.Harvest, cycle.Turnaround); long.Cycles[1].From != want {
		t.Errorf("cycle 2 free from %s; want %s", long.Cycles[1].From, want)
	}
}

// The farmer's own later cycles are never changed, only checked: a longer
// crop before an own sowing gives a warning that names both dates.
func TestOwnCyclesAreCheckedNotChanged(t *testing.T) {
	t.Parallel()
	w := setup(t)
	w.savePlan(t, "green_gram", "2026-06-20")
	own := agro.Plan{Zones: []agro.Zone{{Crop: "groundnut", Sow: "2026-10-01"}}}
	l, err := w.cycles.Set(ctx, w.plot, 1, cycle.Change{Plan: &own})
	if err != nil {
		t.Fatal(err)
	}
	invariants(t, l)
	before := l.Cycles[1].Own
	if before == nil || before.Sow != "2026-10-01" || len(before.Plan.Zones[0].Ring) == 0 {
		t.Fatalf("own cycle 2 = %+v; want groundnut on 1 October over the whole plot", before)
	}
	for _, c := range l.Cycles[1].Checks {
		if c.ID == "cycle.overlap" {
			t.Errorf("unexpected overlap: %v", c)
		}
	}
	w.savePlan(t, "cotton", "2026-06-20") // ends in December
	l = w.line(t)
	after := l.Cycles[1].Own
	if after == nil || after.Sow != before.Sow || after.MainCrop() != "groundnut" || l.Cycles[1].Planned != after {
		t.Fatalf("own cycle 2 after = %+v; want it unchanged and planned", after)
	}
	var overlap *cycle.Check
	for _, c := range l.Cycles[1].Checks {
		if c.ID == "cycle.overlap" {
			overlap = &c
		}
	}
	if overlap == nil || overlap.Severity != agro.Warning {
		t.Fatalf("checks %v; want a cycle.overlap warning", l.Cycles[1].Checks)
	}
	ends, _ := time.Parse(agro.DateLayout, l.Cycles[0].Planned.Harvest)
	if want := fmt.Sprintf("Cotton now ends %s; your Groundnut sowing of 1 October 2026 is before that.", ends.Format("2 January 2006")); overlap.Message != want {
		t.Errorf("message %q; want %q", overlap.Message, want)
	}
	// The suggestion after it follows the own cycle, not the suggestion.
	if want := plus(after.Harvest, cycle.Turnaround); l.Cycles[2].From != want {
		t.Errorf("cycle 3 free from %s; want %s", l.Cycles[2].From, want)
	}
}

// Same family twice and a past date are warnings on the farmer's own.
func TestOwnCycleWarnings(t *testing.T) {
	t.Parallel()
	w := setup(t)
	w.savePlan(t, "maize", "2026-06-20")
	l, err := w.cycles.Set(ctx, w.plot, 1, cycle.Change{Plan: &agro.Plan{Zones: []agro.Zone{{Crop: "wheat", Sow: "2026-11-20"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if !has(l.Cycles[1].Checks, "rotation.same") {
		t.Errorf("wheat after maize: %v; want rotation.same", l.Cycles[1].Checks)
	}
	w.clock.Advance(time.Date(2026, 12, 1, 0, 30, 0, 0, time.UTC).Sub(w.clock.Now()))
	if l = w.line(t); !has(l.Cycles[1].Checks, "cycle.past") {
		t.Errorf("a planned sowing now past: %v; want cycle.past", l.Cycles[1].Checks)
	}
}

func has(cs []cycle.Check, id string) bool {
	for _, c := range cs {
		if c.ID == id {
			return true
		}
	}
	return false
}

// Accept takes the suggestion in one call: at the first position it is
// saved as the plot's plan, so the season roadmap has it; later it is the
// farmer's own and stays when earlier cycles change.
func TestAccept(t *testing.T) {
	t.Parallel()
	w := setup(t)
	sugg := w.line(t).Cycles[0].Suggested
	l, err := w.cycles.Set(ctx, w.plot, 0, cycle.Change{Accept: true})
	if err != nil {
		t.Fatal(err)
	}
	invariants(t, l)
	f, _ := w.svc.Get(ctx, w.plot)
	if f.Plan == nil || f.Plan.Zones[0].Crop != sugg.MainCrop() || f.Plan.Zones[0].Sow != sugg.Sow {
		t.Fatalf("saved plan %+v; want the suggestion %s on %s", f.Plan, sugg.MainCrop(), sugg.Sow)
	}
	if _, err := w.work.Roadmap(ctx, w.plot); err != nil {
		t.Errorf("roadmap after accepting: %v", err)
	}
	next := l.Cycles[1].Suggested
	if l, err = w.cycles.Set(ctx, w.plot, 1, cycle.Change{Accept: true}); err != nil {
		t.Fatal(err)
	}
	if o := l.Cycles[1].Own; o == nil || o.MainCrop() != next.MainCrop() || o.Sow != next.Sow {
		t.Fatalf("own cycle 2 = %+v; want the accepted suggestion", o)
	}
	w.savePlan(t, "cotton", "2026-06-20")
	if o := w.line(t).Cycles[1].Own; o == nil || o.Sow != next.Sow || o.MainCrop() != next.MainCrop() {
		t.Errorf("accepted cycle changed with the cycle before: %+v", o)
	}
	if l, err = w.cycles.Set(ctx, w.plot, 1, cycle.Change{Clear: true}); err != nil || l.Cycles[1].Own != nil || l.Cycles[1].Planned != l.Cycles[1].Suggested {
		t.Errorf("clear = %+v, %v; want the suggestion planned again", l.Cycles[1], err)
	}
}

// An actual sowing beats the planned one: the roadmap's Sown tick, or a
// typed date, becomes the date and moves what follows.
func TestActualSowingMovesTheLine(t *testing.T) {
	t.Parallel()
	w := setup(t)
	w.savePlan(t, "maize", "2026-06-20")
	planned := w.line(t)
	w.clock.Advance(time.Date(2026, 7, 15, 0, 30, 0, 0, time.UTC).Sub(w.clock.Now()))
	sow := work.TaskID(0, "maize", "2026-06-20", "sow")
	if _, _, err := w.work.Record(ctx, w.plot, []work.Event{{ID: "e1", Task: sow, Kind: work.Done, Day: "2026-07-10"}}); err != nil {
		t.Fatal(err)
	}
	l := w.line(t)
	c := l.Cycles[0].Planned
	if c.Sow != "2026-07-10" || c.SowKind != cycle.Actual || c.Areas[0].SowKind != cycle.Actual {
		t.Fatalf("cycle 1 = sow %s (%s); want the recorded 10 July, actual", c.Sow, c.SowKind)
	}
	if c.Harvest != plus(planned.Cycles[0].Planned.Harvest, 20) {
		t.Errorf("harvest %s; want 20 days after the planned %s", c.Harvest, planned.Cycles[0].Planned.Harvest)
	}
	if want := plus(c.Harvest, cycle.Turnaround); l.Cycles[1].From != want {
		t.Errorf("cycle 2 free from %s; want %s", l.Cycles[1].From, want)
	}

	// A typed date: recorded on the roadmap too, and the line follows.
	if l, err := w.cycles.SetSown(ctx, w.plot, "2026-07-05"); err != nil || l.Cycles[0].Planned.Sow != "2026-07-05" {
		t.Fatalf("SetSown = %+v, %v", l.Cycles, err)
	}
	r, _ := w.work.Roadmap(ctx, w.plot)
	if r.Zones[0].Sown != "2026-07-05" {
		t.Errorf("roadmap sown %q; want the typed 5 July", r.Zones[0].Sown)
	}
	// A sowing far from the saved plan's: the plan is saved again, sown
	// that day, rather than refused.
	w.savePlan(t, "maize", "2026-12-01")
	if l, err := w.cycles.SetSown(ctx, w.plot, "2026-07-12"); err != nil || l.Cycles[0].Planned.Sow != "2026-07-12" ||
		l.Cycles[0].Planned.SowKind != cycle.Actual || l.Cycles[0].Planned.MainCrop() != "maize" {
		t.Fatalf("far SetSown = %+v, %v; want maize sown 12 July", l.Cycles, err)
	}
	if r, _ := w.work.Roadmap(ctx, w.plot); r.Zones[0].Sown != "2026-07-12" || r.Zones[0].PlannedSow != "2026-07-12" {
		t.Errorf("roadmap after a far sowing = sown %q, planned %q; want both 12 July", r.Zones[0].Sown, r.Zones[0].PlannedSow)
	}
	for _, bad := range []string{"2026-07-17", "5 July", "2025-07-01"} {
		if _, err := w.cycles.SetSown(ctx, w.plot, bad); !errors.Is(err, cycle.ErrInvalid) {
			t.Errorf("SetSown(%s) = %v; want ErrInvalid", bad, err)
		}
	}
}

// A typed sowing with no saved plan saves the suggestion, sown that day;
// without the roadmap, the typed date alone counts, and only for its plan.
func TestSownWithoutAPlan(t *testing.T) {
	t.Parallel()
	w := setup(t)
	bare := cycle.New(w.plan, w.store) // no roadmap
	sugg := w.line(t).Cycles[0].Suggested
	l, err := bare.SetSown(ctx, w.plot, "2026-05-28")
	if err != nil {
		t.Fatal(err)
	}
	c := l.Cycles[0].Own
	if c == nil || c.MainCrop() != sugg.MainCrop() || c.Sow != "2026-05-28" || c.SowKind != cycle.Actual {
		t.Fatalf("cycle 1 = %+v; want the suggested %s, sown 28 May", c, sugg.MainCrop())
	}
	w.savePlan(t, "cotton", "2026-06-20")
	if c := w.line(t).Cycles[0].Own; c.Sow != "2026-06-20" || c.SowKind != cycle.Planned {
		t.Errorf("a new plan: sow %s (%s); want its own planned date", c.Sow, c.SowKind)
	}
}

// At most three cycles; later ones can be turned off, which keeps the
// farmer's own ones for when they are turned on again.
func TestThreeAtMostAndFutureOff(t *testing.T) {
	t.Parallel()
	w := setup(t)
	for _, pos := range []int{-1, cycle.MaxCycles} {
		if _, err := w.cycles.Set(ctx, w.plot, pos, cycle.Change{Accept: true}); !errors.Is(err, cycle.ErrInvalid) {
			t.Errorf("Set at %d = %v; want ErrInvalid", pos, err)
		}
	}
	if _, err := w.cycles.Set(ctx, w.plot, 2, cycle.Change{Accept: true}); err != nil {
		t.Fatal(err)
	}
	l, err := w.cycles.SetFuture(ctx, w.plot, false)
	if err != nil {
		t.Fatal(err)
	}
	invariants(t, l)
	if len(l.Cycles) != 1 || l.Future {
		t.Fatalf("future off: %d cycles", len(l.Cycles))
	}
	if _, err := w.cycles.Set(ctx, w.plot, 1, cycle.Change{Accept: true}); !errors.Is(err, cycle.ErrInvalid) {
		t.Errorf("Set at 1 with future off = %v; want ErrInvalid", err)
	}
	if l, err = w.cycles.SetFuture(ctx, w.plot, true); err != nil || len(l.Cycles) != 3 || l.Cycles[2].Own == nil {
		t.Errorf("future on again: %+v, %v; want the own cycle 3 back", l.Cycles, err)
	}
}

// The method is chosen per cycle; anything but organic or conventional,
// and changes that say nothing or too much, are refused.
func TestMethod(t *testing.T) {
	t.Parallel()
	w := setup(t)
	if l := w.line(t); l.Cycles[1].Planned.Method != cycle.Conventional {
		t.Errorf("default method %s", l.Cycles[1].Planned.Method)
	}
	l, err := w.cycles.Set(ctx, w.plot, 1, cycle.Change{Method: cycle.Organic})
	if err != nil || l.Cycles[1].Planned.Method != cycle.Organic || l.Cycles[0].Planned.Method != cycle.Conventional || l.Cycles[1].Own != nil {
		t.Errorf("organic at 2 = %+v, %v", l.Cycles, err)
	}
	for _, ch := range []cycle.Change{{Method: "natural"}, {}, {Accept: true, Clear: true}} {
		if _, err := w.cycles.Set(ctx, w.plot, 1, ch); !errors.Is(err, cycle.ErrInvalid) {
			t.Errorf("Set(%+v) = %v; want ErrInvalid", ch, err)
		}
	}
	if _, err := w.cycles.Set(ctx, w.plot, 0, cycle.Change{Clear: true}); !errors.Is(err, cycle.ErrInvalid) {
		t.Errorf("clearing the current cycle = %v; want ErrInvalid", err)
	}
}

// An own plan with errors is refused with its report, at any position.
func TestOwnPlanWithErrors(t *testing.T) {
	t.Parallel()
	w := setup(t)
	for pos := range 2 {
		_, err := w.cycles.Set(ctx, w.plot, pos, cycle.Change{Plan: &agro.Plan{Zones: []agro.Zone{{Crop: "maize", Sow: "someday"}}}})
		var bad *planner.PlanError
		if !errors.As(err, &bad) || !errors.Is(err, agro.ErrInvalid) {
			t.Errorf("position %d: %v; want a PlanError", pos, err)
		}
	}
	if r, _ := w.store.Get(ctx, w.plot); r.Version != 0 {
		t.Errorf("a refused change stored %+v", r)
	}
}

// Without a climate nothing is suggested, the farmer's own cycles still
// show (with an empty place after them to set the next), and the line
// says why; nothing fails.
func TestNoClimateNeverBlocks(t *testing.T) {
	t.Parallel()
	w := setup(t)
	w.savePlan(t, "maize", "2026-06-20")
	w.climate.Err = agro.ErrNoClimate
	l := w.line(t)
	if len(l.Cycles) != 2 || l.Cycles[1].Planned != nil || l.Cycles[0].Own == nil || l.Cycles[0].Suggested != nil || len(l.Notes) == 0 || l.Notes[0].ID != "climate.none" {
		t.Errorf("no climate: %+v", l)
	}
}

// A perennial holds the land: the line stops after it.
func TestPerennialEndsTheLine(t *testing.T) {
	t.Parallel()
	w := setup(t)
	w.savePlan(t, "mango", "2026-07-01")
	l := w.line(t)
	if len(l.Cycles) != 1 || !l.Cycles[0].Planned.Perennial || l.Notes[len(l.Notes)-1].ID != "cycle.perennial" {
		t.Errorf("after mango: %d cycles, notes %v", len(l.Cycles), l.Notes)
	}
}

// Advance moves the line on: the next planned cycle becomes the saved
// plan, the current crop the previous one, own cycles move up.
func TestAdvance(t *testing.T) {
	t.Parallel()
	w := setup(t)
	if _, err := w.cycles.Advance(ctx, w.plot); !errors.Is(err, cycle.ErrInvalid) {
		t.Errorf("advance with no plan = %v; want ErrInvalid", err)
	}
	w.savePlan(t, "maize", "2026-06-20")
	if _, err := w.cycles.Set(ctx, w.plot, 2, cycle.Change{Accept: true, Method: cycle.Organic}); err != nil {
		t.Fatal(err)
	}
	before := w.line(t)
	w.clock.Advance(time.Date(2026, 10, 1, 0, 30, 0, 0, time.UTC).Sub(w.clock.Now()))
	l, err := w.cycles.Advance(ctx, w.plot)
	if err != nil {
		t.Fatal(err)
	}
	invariants(t, l)
	f, _ := w.svc.Get(ctx, w.plot)
	if f.Plan.Zones[0].Crop != before.Cycles[1].Planned.MainCrop() || f.Profile.PreviousCrop != "maize" {
		t.Errorf("saved plan %s, previous crop %s; want %s after maize", f.Plan.Zones[0].Crop, f.Profile.PreviousCrop, before.Cycles[1].Planned.MainCrop())
	}
	if o := l.Cycles[1].Own; o == nil || o.Sow != before.Cycles[2].Own.Sow || o.Method != cycle.Organic {
		t.Errorf("own cycle 3 did not move up: %+v", l.Cycles[1])
	}
}

type addon struct {
	name    string
	err     error
	nothing bool
}

func (a addon) Name() string { return a.name }
func (a addon) For(_ context.Context, _ farm.Farm, c cycle.Cycle) (any, error) {
	if a.err != nil {
		return nil, a.err
	}
	if a.nothing {
		return nil, nil
	}
	return map[string]any{"crop": c.MainCrop(), "ha": c.Areas[0].AreaHa, "method": c.Method}, nil
}

// Addons ride along on each planned cycle; a failing one is a note, never
// a failure; one with nothing to say is left out without a note.
func TestAddons(t *testing.T) {
	t.Parallel()
	w := setup(t, cycle.WithAddons(addon{name: "inputs"}, addon{name: "prices", err: errors.New("down")}, addon{name: "quiet", nothing: true}))
	l := w.line(t)
	for i, p := range l.Cycles {
		got, _ := json.Marshal(p.Addons["inputs"])
		want, _ := json.Marshal(map[string]any{"crop": p.Planned.MainCrop(), "ha": p.Planned.Areas[0].AreaHa, "method": p.Planned.Method})
		_, quiet := p.Addons["quiet"]
		if string(got) != string(want) || p.Addons["prices"] != nil || quiet {
			t.Errorf("cycle %d addons %v", i, p.Addons)
		}
	}
	if !hasNote(l.Notes, "addon.prices") || hasNote(l.Notes, "addon.quiet") {
		t.Errorf("notes %v; want addon.prices alone", l.Notes)
	}
}

func hasNote(ns []agro.Note, id string) bool {
	for _, n := range ns {
		if n.ID == id {
			return true
		}
	}
	return false
}

// A worker's read-only scope can read the line and change nothing.
func TestReadOnly(t *testing.T) {
	t.Parallel()
	w := setup(t)
	ro := farm.WithReadOnly(ctx)
	if _, err := w.cycles.Line(ro, w.plot); err != nil {
		t.Errorf("Line read-only = %v", err)
	}
	for name, f := range map[string]func() error{
		"set":     func() error { _, err := w.cycles.Set(ro, w.plot, 1, cycle.Change{Accept: true}); return err },
		"sown":    func() error { _, err := w.cycles.SetSown(ro, w.plot, "2026-05-30"); return err },
		"future":  func() error { _, err := w.cycles.SetFuture(ro, w.plot, false); return err },
		"advance": func() error { _, err := w.cycles.Advance(ro, w.plot); return err },
	} {
		if err := f(); !errors.Is(err, farm.ErrReadOnly) {
			t.Errorf("%s read-only = %v; want ErrReadOnly", name, err)
		}
	}
	if _, err := w.cycles.Line(ctx, "nope"); !errors.Is(err, farm.ErrNotFound) {
		t.Errorf("unknown plot = %v", err)
	}
}

// A write that loses a race reads again and applies the change once.
func TestConcurrentWritesAllLand(t *testing.T) {
	t.Parallel()
	w := setup(t)
	done := make(chan error, 2)
	go func() { _, err := w.cycles.Set(ctx, w.plot, 1, cycle.Change{Method: cycle.Organic}); done <- err }()
	go func() { _, err := w.cycles.Set(ctx, w.plot, 2, cycle.Change{Method: cycle.Organic}); done <- err }()
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	r, _ := w.store.Get(ctx, w.plot)
	if r.Slots[1].Method != cycle.Organic || r.Slots[2].Method != cycle.Organic {
		t.Errorf("slots %+v; want both writes", r.Slots)
	}
}

// A save that loses to another writer is done again from what is stored,
// so the change lands once: advancing maize → chickpea makes chickpea
// the plan, never the cycle after it. One that never wins is a conflict
// and changes nothing: not the saved plan, the profile, the record or
// the roadmap.
func TestConflictsApplyOnceOrNotAtAll(t *testing.T) {
	t.Parallel()
	ready := func(t *testing.T) (*world, cycle.Line) {
		w := setup(t)
		w.savePlan(t, "maize", "2026-06-20")
		if _, err := w.cycles.Set(ctx, w.plot, 1, cycle.Change{Accept: true}); err != nil {
			t.Fatal(err)
		}
		before := w.line(t)
		w.clock.Advance(time.Date(2026, 10, 1, 0, 30, 0, 0, time.UTC).Sub(w.clock.Now()))
		return w, before
	}
	t.Run("advance, conflict once", func(t *testing.T) {
		t.Parallel()
		w, before := ready(t)
		w.store.n.Store(1)
		l, err := w.cycles.Advance(ctx, w.plot)
		if err != nil {
			t.Fatal(err)
		}
		next := before.Cycles[1].Planned.MainCrop()
		f, _ := w.svc.Get(ctx, w.plot)
		if f.Plan.Zones[0].Crop != next || f.Profile.PreviousCrop != "maize" || l.Cycles[0].Planned.MainCrop() != next {
			t.Errorf("saved %s after %s, line starts %s; want %s after maize", f.Plan.Zones[0].Crop, f.Profile.PreviousCrop, l.Cycles[0].Planned.MainCrop(), next)
		}
	})
	type state struct {
		Farm    farm.Farm
		Record  cycle.Record
		Roadmap work.Roadmap
	}
	snap := func(t *testing.T, w *world) state {
		t.Helper()
		f, err := w.svc.Get(ctx, w.plot)
		if err != nil {
			t.Fatal(err)
		}
		r, _ := w.store.Get(ctx, w.plot)
		rm, err := w.work.Roadmap(ctx, w.plot)
		if err != nil {
			t.Fatal(err)
		}
		return state{f, r, rm}
	}
	for name, call := range map[string]func(w *world) error{
		"advance": func(w *world) error { _, err := w.cycles.Advance(ctx, w.plot); return err },
		"far sowing": func(w *world) error {
			_, err := w.cycles.SetSown(ctx, w.plot, "2026-01-15")
			return err
		},
		"near sowing": func(w *world) error {
			_, err := w.cycles.SetSown(ctx, w.plot, "2026-06-25")
			return err
		},
		"own plan at 0": func(w *world) error {
			f, _ := w.svc.Get(ctx, w.plot)
			_, err := w.cycles.Set(ctx, w.plot, 0, cycle.Change{Plan: &agro.Plan{Zones: []agro.Zone{{Crop: "cotton", Sow: "2026-10-05", Ring: f.Boundary.Ring()}}}, Method: cycle.Organic})
			return err
		},
	} {
		t.Run(name+", always in conflict", func(t *testing.T) {
			t.Parallel()
			w, _ := ready(t)
			was := snap(t, w)
			w.store.n.Store(-1)
			if err := call(w); !errors.Is(err, cycle.ErrConflict) {
				t.Fatalf("= %v; want ErrConflict", err)
			}
			w.store.n.Store(0)
			now := snap(t, w)
			for what, same := range map[string]bool{
				"saved plan": reflect.DeepEqual(now.Farm.Plan, was.Farm.Plan),
				"profile":    reflect.DeepEqual(now.Farm.Profile, was.Farm.Profile),
				"record":     reflect.DeepEqual(now.Record, was.Record),
				"roadmap":    reflect.DeepEqual(now.Roadmap, was.Roadmap),
			} {
				if !same {
					t.Errorf("a conflict changed the %s", what)
				}
			}
		})
	}
}

// A write cut short after storing the record (a crash before the farm
// was changed) is finished by the next read: over the plan it was made
// against it is made; over a plan changed since, it is dropped.
func TestAWriteCutShortIsFinished(t *testing.T) {
	t.Parallel()
	cut := func(t *testing.T, w *world) {
		t.Helper()
		// An advance whose record was stored and whose farm writes never
		// ran, as a crash between them leaves it.
		f, _ := w.svc.Get(ctx, w.plot)
		next := w.line(t).Cycles[1].Planned.Plan
		rec, _ := w.store.Get(ctx, w.plot)
		was := rec.Slots
		rec.Slots = [cycle.MaxCycles]cycle.Slot{{Method: rec.Slots[1].Method}, rec.Slots[2], {}}
		sum := sha(t, f.Plan)
		rec.Pending = &cycle.Pending{From: sum, PreviousCrop: "maize", Plan: &next, Was: was}
		if _, err := w.store.Put(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("finished", func(t *testing.T) {
		t.Parallel()
		w := setup(t)
		w.savePlan(t, "maize", "2026-06-20")
		if _, err := w.cycles.Set(ctx, w.plot, 1, cycle.Change{Accept: true}); err != nil {
			t.Fatal(err)
		}
		next := w.line(t).Cycles[1].Planned.MainCrop()
		cut(t, w)
		l := w.line(t)
		f, _ := w.svc.Get(ctx, w.plot)
		r, _ := w.store.Get(ctx, w.plot)
		if f.Plan.Zones[0].Crop != next || f.Profile.PreviousCrop != "maize" || r.Pending != nil || l.Cycles[0].Own.MainCrop() != next {
			t.Errorf("after the read: saved %s after %s, pending %v; want %s after maize, nothing pending", f.Plan.Zones[0].Crop, f.Profile.PreviousCrop, r.Pending, next)
		}
	})
	t.Run("overtaken", func(t *testing.T) {
		t.Parallel()
		w := setup(t)
		w.savePlan(t, "maize", "2026-06-20")
		cut(t, w)
		w.savePlan(t, "cotton", "2026-06-25") // the season plan, since
		w.line(t)
		f, _ := w.svc.Get(ctx, w.plot)
		r, _ := w.store.Get(ctx, w.plot)
		if f.Plan.Zones[0].Crop != "cotton" || r.Pending != nil {
			t.Errorf("saved %s, pending %v; want the newer cotton, nothing pending", f.Plan.Zones[0].Crop, r.Pending)
		}
	})
}

// sha is the plan's hash as cycle.Pending.From has it.
func sha(t *testing.T, p *agro.Plan) string {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// A cycle whose crop the plot's crop list no longer has (its region pack
// changed, or the crop was taken out) is shown with a warning, never a
// failed line; dates that need the crop are left out, and nothing is
// planned after a cycle with no harvest date.
func TestUnknownCropIsAWarning(t *testing.T) {
	t.Parallel()
	w := setup(t)
	w.savePlan(t, "maize", "2026-06-20")
	rec, _ := w.store.Get(ctx, w.plot)
	f, _ := w.svc.Get(ctx, w.plot)
	rec.Slots[1].Plan = &agro.Plan{Zones: []agro.Zone{{Crop: "kale", Sow: "2026-11-01", Ring: f.Boundary.Ring()}}}
	if _, err := w.store.Put(ctx, rec); err != nil {
		t.Fatal(err)
	}
	l, err := w.cycles.Line(ctx, w.plot)
	if err != nil {
		t.Fatalf("line with an unknown crop at 2: %v", err)
	}
	if o := l.Cycles[1].Own; o == nil || o.MainCrop() != "kale" || !has(l.Cycles[1].Checks, "crop.unknown") || len(l.Cycles) != 2 {
		t.Errorf("cycle 2 = %+v, checks %v, %d cycles; want kale with the warning, and nothing after it", o, l.Cycles[1].Checks, len(l.Cycles))
	}
	// The saved plan itself.
	if _, err := w.svc.SetPlan(ctx, w.plot, &agro.Plan{Zones: []agro.Zone{{Crop: "kale", Sow: "2026-06-20", Ring: f.Boundary.Ring()}}}, nil); err != nil {
		t.Fatal(err)
	}
	l, err = w.cycles.Line(ctx, w.plot)
	if err != nil || len(l.Cycles) == 0 || !has(l.Cycles[0].Checks, "crop.unknown") {
		t.Errorf("unknown crop in the saved plan: %+v, %v; want it with a warning", l.Cycles, err)
	}
}

// The line moves on only once the current crop is about to come off
// (AdvanceDays before its harvest, the day the line says): earlier, the
// step, which cannot be undone, is refused and changes nothing. With the
// later cycles turned off it moves on to our suggestion.
func TestAdvanceOnlyNearHarvest(t *testing.T) {
	t.Parallel()
	w := setup(t)
	w.savePlan(t, "maize", "2026-06-20")
	l := w.line(t)
	from := plus(l.Cycles[0].Planned.Harvest, -cycle.AdvanceDays)
	if l.AdvanceFrom != from {
		t.Errorf("advance from %q; want %s", l.AdvanceFrom, from)
	}
	w.clock.Advance(time.Date(2026, 8, 1, 0, 30, 0, 0, time.UTC).Sub(w.clock.Now()))
	if _, err := w.cycles.Advance(ctx, w.plot); !errors.Is(err, cycle.ErrInvalid) {
		t.Errorf("advance on 1 August (maize comes off %s) = %v; want ErrInvalid", l.Cycles[0].Planned.Harvest, err)
	}
	if f, _ := w.svc.Get(ctx, w.plot); f.Plan.Zones[0].Crop != "maize" {
		t.Errorf("a refused advance saved %s", f.Plan.Zones[0].Crop)
	}
	if _, err := w.cycles.SetFuture(ctx, w.plot, false); err != nil {
		t.Fatal(err)
	}
	d, _ := time.Parse(agro.DateLayout, from)
	w.clock.Advance(d.Add(30 * time.Minute).Sub(w.clock.Now()))
	if _, err := w.cycles.Advance(ctx, w.plot); err != nil {
		t.Fatalf("advance on %s with the later cycles off: %v", from, err)
	}
	if f, _ := w.svc.Get(ctx, w.plot); f.Plan.Zones[0].Crop == "maize" || f.Profile.PreviousCrop != "maize" {
		t.Errorf("after advancing: saved %s after %s", f.Plan.Zones[0].Crop, f.Profile.PreviousCrop)
	}
}

// A sowing far from the plan saves the plan again, which renames its
// tasks (their IDs carry the planned sowing): what the farmer had ticked
// moves to the same tasks of the new plan, never silently dropped.
func TestFarSowingKeepsTheTicks(t *testing.T) {
	t.Parallel()
	w := setup(t)
	w.clock.Advance(time.Date(2026, 7, 15, 0, 30, 0, 0, time.UTC).Sub(w.clock.Now()))
	w.savePlan(t, "maize", "2026-12-01")
	r, err := w.work.Roadmap(ctx, w.plot)
	if err != nil {
		t.Fatal(err)
	}
	prefix := work.TaskID(0, "maize", "2026-12-01", "")
	var before, after work.Task
	for _, k := range r.Tasks {
		if k.Source != work.FromCalendar || k.Kind == "sow" {
			continue
		}
		if *k.DAS < 0 && before.ID == "" {
			before = k
		}
		if *k.DAS > 0 && after.ID == "" {
			after = k
		}
	}
	evs := []work.Event{
		{ID: "t1", Task: before.ID, Kind: work.Done, Day: "2026-07-01", Note: "ploughed twice"},
		{ID: "t2", Task: after.ID, Kind: work.Skipped, Day: "2026-07-10"},
	}
	if _, _, err := w.work.Record(ctx, w.plot, evs); err != nil {
		t.Fatal(err)
	}
	if _, err := w.cycles.SetSown(ctx, w.plot, "2026-07-12"); err != nil {
		t.Fatal(err)
	}
	r, err = w.work.Roadmap(ctx, w.plot)
	if err != nil {
		t.Fatal(err)
	}
	moved := work.TaskID(0, "maize", "2026-07-12", "")
	for _, c := range []struct {
		was        work.Task
		state, day string
	}{{before, work.Done, "2026-07-01"}, {after, work.Skipped, "2026-07-10"}} {
		id := moved + c.was.ID[len(prefix):]
		k, ok := r.Task(id)
		if !ok || k.State != c.state || k.Day != c.day {
			t.Errorf("%s after the plan was saved again: %+v, %v; want %s on %s", id, k, ok, c.state, c.day)
		}
	}
	if k, _ := r.Task(moved + before.ID[len(prefix):]); k.Note != "ploughed twice" {
		t.Errorf("note %q; want it kept", k.Note)
	}
	// Recording the same sowing again changes nothing.
	n := r.Counts
	if _, err := w.cycles.SetSown(ctx, w.plot, "2026-07-12"); err != nil {
		t.Fatal(err)
	}
	if r, _ := w.work.Roadmap(ctx, w.plot); r.Counts != n {
		t.Errorf("counts %+v after the same sowing again; want %+v", r.Counts, n)
	}
}
