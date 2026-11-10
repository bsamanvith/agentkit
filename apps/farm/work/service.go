package work

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/calendar"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
)

// Store keeps events. Implementations are safe for concurrent use and pass
// worktest.RunStore.
type Store interface {
	// Append records the events whose IDs it has not seen on their farm
	// and returns how many it recorded; an event with a known ID is
	// skipped, whatever it says. IDs are unique per farm, not across farms.
	Append(ctx context.Context, events ...Event) (int, error)
	// Events returns a farm's events in the order they were recorded (At,
	// then ID).
	Events(ctx context.Context, farmID string) ([]Event, error)
}

// Planner is what the roadmap needs of the planner.
type Planner interface {
	Farms() *farm.Service
	Library() *agro.Library
	Today() time.Time
}

// Service builds roadmaps and records what the farmer did. It is safe for
// concurrent use.
type Service struct {
	plan  Planner
	store Store
	now   func() time.Time
}

// New returns a Service over the planner's plots and the store's events.
func New(p Planner, s Store) *Service { return &Service{plan: p, store: s, now: time.Now} }

// SetNow sets the clock events are stamped with (for tests).
func (s *Service) SetNow(now func() time.Time) { s.now = now }

// Roadmap returns a plot's roadmap; ErrInvalid if it has no saved plan.
func (s *Service) Roadmap(ctx context.Context, farmID string) (Roadmap, error) {
	f, err := s.plan.Farms().Get(ctx, farmID)
	if err != nil {
		return Roadmap{}, err
	}
	evs, err := s.store.Events(ctx, farmID)
	if err != nil {
		return Roadmap{}, err
	}
	return s.build(f, evs)
}

func (s *Service) build(f farm.Farm, evs []Event) (Roadmap, error) {
	if f.Plan == nil || len(f.Plan.Zones) == 0 {
		return Roadmap{}, fmt.Errorf("%w: plot %s has no saved crop plan", ErrInvalid, f.ID)
	}
	irrigated := f.Profile != nil && len(f.Profile.Water) > 0
	return Build(s.plan.Library(), f.ID, *f.Plan, irrigated, evs, s.plan.Today())
}

// Due returns a plot's open tasks dated from from to to, both included;
// none for a plot with no plan.
func (s *Service) Due(ctx context.Context, farmID string, from, to time.Time) ([]Task, error) {
	r, err := s.Roadmap(ctx, farmID)
	if err != nil {
		f, ferr := s.plan.Farms().Get(ctx, farmID)
		if ferr == nil && (f.Plan == nil || len(f.Plan.Zones) == 0) {
			return nil, nil
		}
		return nil, err
	}
	f, t := from.Format(agro.DateLayout), to.Format(agro.DateLayout)
	return slices.DeleteFunc(r.Tasks, func(k Task) bool { return k.State != Open || k.Date < f || k.Date > t }), nil
}

// Record checks and records events on a plot and returns how many were new
// and the roadmap after them. Events whose IDs were recorded before are
// skipped without being checked again, so a phone can resend what it
// queued. If any new event is invalid, none is recorded.
func (s *Service) Record(ctx context.Context, farmID string, evs []Event) (int, Roadmap, error) {
	f, err := s.plan.Farms().Get(ctx, farmID)
	if err != nil {
		return 0, Roadmap{}, err
	}
	have, err := s.store.Events(ctx, farmID)
	if err != nil {
		return 0, Roadmap{}, err
	}
	r, err := s.build(f, have)
	if err != nil {
		return 0, Roadmap{}, err
	}
	seen := map[string]bool{}
	for _, e := range have {
		seen[e.ID] = true
	}
	now := s.now().UTC()
	var add []Event
	added := map[string]bool{} // farmer tasks added in this batch
	for i, e := range evs {
		if seen[e.ID] {
			continue
		}
		e.Farm = farmID
		if e.At.IsZero() || e.At.After(now) {
			e.At = now
		}
		if err := s.check(e, f, r, added); err != nil {
			return 0, Roadmap{}, &EventError{Index: i, ID: e.ID, Err: err}
		}
		if e.Kind == Added {
			e.Task = ""
			added[e.ID] = true
		}
		seen[e.ID] = true
		add = append(add, e)
	}
	n := 0
	if len(add) > 0 {
		if n, err = s.store.Append(ctx, add...); err != nil {
			return 0, Roadmap{}, err
		}
	}
	all, err := s.store.Events(ctx, farmID)
	if err != nil {
		return n, Roadmap{}, err
	}
	r, err = s.build(f, all)
	return n, r, err
}

// EventError is an event Record refused, by its place in the batch and its
// ID, so that a phone can drop that one change and resend the rest. It is
// ErrInvalid.
type EventError struct {
	Index int
	ID    string
	Err   error
}

func (e *EventError) Error() string {
	return fmt.Sprintf("%v: event %d (%s): %v", ErrInvalid, e.Index, e.ID, e.Err)
}

// Unwrap returns ErrInvalid and the reason.
func (e *EventError) Unwrap() []error { return []error{ErrInvalid, e.Err} }

var idRE = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)

func short(field, s string, max int) error {
	if utf8.RuneCountInString(s) > max {
		return fmt.Errorf("%s is longer than %d characters", field, max)
	}
	return nil
}

func (s *Service) check(e Event, f farm.Farm, r Roadmap, added map[string]bool) error {
	if !idRE.MatchString(e.ID) {
		return fmt.Errorf("id must be 1 to 64 letters, digits or _ . : -")
	}
	for _, c := range []struct {
		field, s string
		max      int
	}{{"by", e.By, 80}, {"note", e.Note, 500}, {"title", e.Title, 120}} {
		if err := short(c.field, c.s, c.max); err != nil {
			return err
		}
	}
	if a := e.Actuals; a != nil {
		if a.Quantity < 0 || a.CostINR < 0 || a.Workers < 0 || a.Workers > 10000 {
			return fmt.Errorf("actuals must not be negative")
		}
		if err := short("unit", a.Unit, 20); err != nil {
			return err
		}
	}
	today := s.plan.Today()
	day, derr := time.Parse(agro.DateLayout, e.Day)
	switch e.Kind {
	case Added:
		if _, taken := r.Task(e.ID); taken || added[e.ID] {
			return fmt.Errorf("id %s is already a task's", e.ID)
		}
		if e.Task != "" && e.Task != e.ID {
			return fmt.Errorf("an added task's id is the event's: leave task out")
		}
		if e.Title == "" {
			return fmt.Errorf("an added task needs a title")
		}
		if derr != nil {
			return fmt.Errorf("an added task needs its day, YYYY-MM-DD")
		}
		if day.Before(today.AddDate(-1, 0, 0)) || day.After(today.AddDate(2, 0, 0)) {
			return fmt.Errorf("day %s is more than a year ago or two years ahead", e.Day)
		}
		if e.TaskKind != "" && !slices.Contains(Kinds, e.TaskKind) {
			return fmt.Errorf("task_kind %q is not one of %v", e.TaskKind, Kinds)
		}
		if e.Zone != nil && (*e.Zone < WholePlot || *e.Zone >= len(f.Plan.Zones)) {
			return fmt.Errorf("zone %d is not a zone of the plan", *e.Zone)
		}
		return nil
	case Done, Skipped, NotNeeded, Reopened, Removed:
	default:
		return fmt.Errorf("kind %q is not one of done, skipped, not_needed, reopened, added, removed", e.Kind)
	}
	t, ok := r.Task(e.Task)
	if !ok && !added[e.Task] {
		return fmt.Errorf("task %q is not on the plot's roadmap", e.Task)
	}
	if e.Kind == Removed {
		if ok && t.Source != FromFarmer {
			return fmt.Errorf("only the farmer's own tasks can be removed; mark a calendar task not needed instead")
		}
		return nil
	}
	if e.Kind == Reopened {
		return nil
	}
	if derr != nil {
		return fmt.Errorf("day must be the date it happened, YYYY-MM-DD")
	}
	if day.After(today.AddDate(0, 0, 1)) {
		return fmt.Errorf("day %s is in the future", e.Day)
	}
	if ok && t.Kind == calendar.Sow && t.Source == FromCalendar && e.Kind == Done {
		planned, _ := time.Parse(agro.DateLayout, r.Zones[t.Zone].PlannedSow)
		if d := day.Sub(planned).Hours() / 24; d < -120 || d > 120 {
			return fmt.Errorf("sowing on %s is more than 120 days from the planned %s: save a new plan instead", e.Day, r.Zones[t.Zone].PlannedSow)
		}
	}
	return nil
}
