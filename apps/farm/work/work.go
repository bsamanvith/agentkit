package work

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/calendar"
)

// ErrInvalid is returned for an event that cannot be recorded.
var ErrInvalid = errors.New("work: invalid")

// Event kinds.
const (
	// Done, Skipped and NotNeeded close a task on Day.
	Done      = "done"
	Skipped   = "skipped"
	NotNeeded = "not_needed"
	// Reopened opens a closed task again.
	Reopened = "reopened"
	// Added is a task of the farmer's own, due on Day; its ID is the
	// event's. Removed takes one away.
	Added   = "added"
	Removed = "removed"
)

// Open is the state of a task nobody has closed.
const Open = "open"

// Where a task comes from.
const (
	FromCalendar = "calendar"
	FromFarmer   = "farmer"
)

// Phases of the season.
const (
	PhasePrepare = "prepare"
	PhaseSow     = "sow"
	PhaseGrow    = "grow"
	PhaseHarvest = "harvest"
)

// Other is the kind of a farmer's task that is none of the calendar's.
const Other = "other"

// Grace is how many days after its date an open task is shown as overdue.
const Grace = 7

// WholePlot is the zone of a task that is not about one zone.
const WholePlot = -1

// Actuals is what doing a task took.
type Actuals struct {
	Quantity float64 `json:"quantity,omitempty"`
	Unit     string  `json:"unit,omitempty"` // kg, bags, litres, hours, ...
	CostINR  float64 `json:"cost_inr,omitempty"`
	Workers  int     `json:"workers,omitempty"`
}

// Event is one thing that happened to a task.
type Event struct {
	// ID is made by whoever recorded the event (a phone, the assistant)
	// and is unique: an event with an ID seen before is not recorded
	// again.
	ID   string `json:"id"`
	Farm string `json:"farm"`
	// Task is the task's ID; empty for Added.
	Task string `json:"task,omitempty"`
	Kind string `json:"kind"`
	// Day is when it happened (Done, Skipped, NotNeeded), or when an
	// Added task is due, as YYYY-MM-DD.
	Day string `json:"day,omitempty"`
	// At is when it was recorded; the order events are applied in.
	At      time.Time `json:"at"`
	By      string    `json:"by,omitempty"`
	Note    string    `json:"note,omitempty"`
	Actuals *Actuals  `json:"actuals,omitempty"`
	// Title, TaskKind and Zone describe an Added task.
	Title    string `json:"title,omitempty"`
	TaskKind string `json:"task_kind,omitempty"`
	Zone     *int   `json:"zone,omitempty"`
}

// Task is one piece of work on the roadmap and what became of it.
type Task struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	// Zone is the plan's zone index, or WholePlot.
	Zone   int    `json:"zone"`
	Crop   string `json:"crop,omitempty"`
	Kind   string `json:"kind"`
	Phase  string `json:"phase"`
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
	// Date is when it is due, as YYYY-MM-DD; DAS its days after the
	// zone's sowing and Stage the crop's stage then (calendar tasks).
	Date  string `json:"date"`
	DAS   *int   `json:"das,omitempty"`
	Stage string `json:"stage,omitempty"`

	State   string   `json:"state"`
	Day     string   `json:"day,omitempty"` // when it was closed
	By      string   `json:"by,omitempty"`
	Note    string   `json:"note,omitempty"`
	Actuals *Actuals `json:"actuals,omitempty"`
	// Late is a task done more than Grace days after its date; Overdue an
	// open one past it, for the farmer to say what happened.
	Late    bool `json:"late,omitempty"`
	Overdue bool `json:"overdue,omitempty"`
}

// Zone is one zone of the plan on the roadmap.
type Zone struct {
	Zone     int    `json:"zone"`
	Crop     string `json:"crop"`
	CropName string `json:"crop_name"`
	With     string `json:"with,omitempty"`
	// PlannedSow is the plan's sowing date; Sown the real one, once the
	// farmer has marked the sowing done. Later tasks follow Sown.
	PlannedSow  string `json:"planned_sow"`
	Sown        string `json:"sown,omitempty"`
	HarvestFrom string `json:"harvest_from"`
	HarvestBy   string `json:"harvest_by"`
}

// Counts sums up the roadmap.
type Counts struct {
	Open      int `json:"open"`
	Done      int `json:"done"`
	Skipped   int `json:"skipped"`
	NotNeeded int `json:"not_needed"`
	Overdue   int `json:"overdue"`
}

// Roadmap is a plot's season: its zones and every task in date order.
type Roadmap struct {
	Farm   string   `json:"farm"`
	Today  string   `json:"today"`
	Zones  []Zone   `json:"zones"`
	Tasks  []Task   `json:"tasks"`
	Counts Counts   `json:"counts"`
	Notes  []string `json:"notes,omitempty"`
}

// Task returns the roadmap's task with id.
func (r Roadmap) Task(id string) (Task, bool) {
	i := slices.IndexFunc(r.Tasks, func(t Task) bool { return t.ID == id })
	if i < 0 {
		return Task{}, false
	}
	return r.Tasks[i], true
}

// TaskID is the ID of a calendar task: its zone, the zone's crop, the
// planned sowing date and the calendar rule's key. It does not change when
// the real sowing date does, and does change with a new plan.
func TaskID(zone int, crop, plannedSow, key string) string {
	return fmt.Sprintf("z%d-%s-%s-%s", zone, crop, plannedSow, key)
}

// PhaseOf is the season's phase a task of kind belongs to.
func PhaseOf(kind string) string {
	switch kind {
	case calendar.Prepare:
		return PhasePrepare
	case calendar.Sow:
		return PhaseSow
	case calendar.Reap:
		return PhaseHarvest
	}
	return PhaseGrow
}

// Kinds are the kinds a farmer's task can have.
var Kinds = []string{calendar.Prepare, calendar.Sow, calendar.Weed, calendar.Fertilise, calendar.Irrigate, calendar.Scout, calendar.Protect, calendar.Prune, calendar.Reap, Other}

// Build makes a plot's roadmap from its plan and the events recorded on
// it, as of today. Events about tasks the plan no longer has are ignored.
func Build(lib *agro.Library, farmID string, plan agro.Plan, irrigated bool, events []Event, today time.Time) (Roadmap, error) {
	evs := slices.Clone(events)
	slices.SortStableFunc(evs, func(a, b Event) int {
		return cmp.Or(a.At.Compare(b.At), cmp.Compare(a.ID, b.ID))
	})
	evs = slices.DeleteFunc(evs, func(e Event) bool { return e.Farm != farmID })
	byTask := fold(evs)

	r := Roadmap{Farm: farmID, Today: today.Format(agro.DateLayout), Notes: []string{calendar.Note}}
	for i, z := range plan.Zones {
		planned, err := calendar.ForZone(lib, i, z, irrigated)
		if err != nil {
			return Roadmap{}, err
		}
		zone := Zone{
			Zone: i, Crop: planned.Crop, CropName: planned.CropName, With: planned.With, PlannedSow: z.Sow,
			HarvestFrom: planned.HarvestFrom, HarvestBy: planned.HarvestBy,
		}
		cal := planned
		sowID := TaskID(i, z.Crop, z.Sow, "sow")
		c, err := lib.Crop(z.Crop)
		if err != nil {
			return Roadmap{}, err
		}
		// A perennial's year runs by the calendar month (manure in June,
		// pruning in December), not by days from planting, so its tasks
		// keep their planned dates and keys when the planting moves.
		if st := byTask.get(sowID); st.State == Done && st.Day != z.Sow && !c.Perennial {
			moved := z
			moved.Sow = st.Day
			if cal, err = calendar.ForZone(lib, i, moved, irrigated); err != nil {
				return Roadmap{}, err
			}
			zone.Sown, zone.HarvestFrom, zone.HarvestBy = st.Day, cal.HarvestFrom, cal.HarvestBy
		} else if st.State == Done {
			zone.Sown = st.Day
		}
		r.Zones = append(r.Zones, zone)
		// Work up to and including the sowing keeps its planned dates (it
		// is done by then, and a late sowing shows as late); the rest
		// follows the real sowing date.
		plannedAt := map[string]calendar.Task{}
		for _, k := range planned.Tasks {
			plannedAt[k.Key] = k
		}
		for _, k := range cal.Tasks {
			if p, ok := plannedAt[k.Key]; ok && p.DAS <= 0 {
				k = p
			}
			das := k.DAS
			r.Tasks = append(r.Tasks, Task{
				ID: TaskID(i, z.Crop, z.Sow, k.Key), Source: FromCalendar, Zone: i, Crop: k.Crop,
				Kind: k.Kind, Phase: PhaseOf(k.Kind), Title: k.Title, Detail: k.Detail,
				Date: k.Date, DAS: &das, Stage: k.Stage,
			})
		}
	}
	for _, e := range evs {
		if e.Kind != Added {
			continue
		}
		kind := cmp.Or(e.TaskKind, Other)
		zone := WholePlot
		if e.Zone != nil {
			zone = *e.Zone
		}
		r.Tasks = append(r.Tasks, Task{
			ID: e.ID, Source: FromFarmer, Zone: zone, Kind: kind, Phase: PhaseOf(kind),
			Title: e.Title, Detail: e.Note, Date: e.Day, By: e.By,
		})
	}
	day := func(s string) time.Time { d, _ := time.Parse(agro.DateLayout, s); return d }
	out := r.Tasks[:0]
	for _, t := range r.Tasks {
		st := byTask.get(t.ID)
		if st.removed && t.Source == FromFarmer {
			continue
		}
		t.State, t.Day, t.Note, t.Actuals = st.State, st.Day, st.Note, st.Actuals
		if st.By != "" {
			t.By = st.By
		}
		due := day(t.Date).AddDate(0, 0, Grace)
		switch t.State {
		case Open:
			t.Overdue = today.After(due)
			r.Counts.Open++
			if t.Overdue {
				r.Counts.Overdue++
			}
		case Done:
			t.Late = day(t.Day).After(due)
			r.Counts.Done++
		case Skipped:
			r.Counts.Skipped++
		case NotNeeded:
			r.Counts.NotNeeded++
		}
		out = append(out, t)
	}
	r.Tasks = out
	slices.SortStableFunc(r.Tasks, func(a, b Task) int {
		return cmp.Or(cmp.Compare(a.Date, b.Date), cmp.Compare(a.Zone, b.Zone))
	})
	return r, nil
}

// stateMap is the tasks' states by ID.
type stateMap map[string]state

type state struct {
	State   string
	Day     string
	By      string
	Note    string
	Actuals *Actuals
	removed bool
}

// fold applies the events in order, task by task: the first close wins
// until the task is reopened; a removed task stays removed. A task with no
// events is open.
func fold(evs []Event) stateMap {
	out := stateMap{}
	for _, e := range evs {
		id := e.Task
		if e.Kind == Added {
			id = e.ID
		}
		st, ok := out[id]
		if !ok {
			st = state{State: Open}
		}
		switch e.Kind {
		case Done, Skipped, NotNeeded:
			if st.State == Open {
				st = state{State: e.Kind, Day: e.Day, By: e.By, Note: e.Note, Actuals: e.Actuals, removed: st.removed}
			}
		case Reopened:
			st = state{State: Open, removed: st.removed}
		case Removed:
			st.removed = true
		}
		out[id] = st
	}
	return out
}

// get is a task's state; open when it has no events.
func (m stateMap) get(id string) state {
	if st, ok := m[id]; ok {
		return st
	}
	return state{State: Open}
}
