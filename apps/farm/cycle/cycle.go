package cycle

import (
	"context"
	"errors"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
)

// Errors callers branch on.
var (
	// ErrInvalid: the request cannot be done (a position past MaxCycles,
	// an unknown method, nothing to accept, a sowing date in the future).
	ErrInvalid = errors.New("cycle: invalid")
	// ErrConflict: a Store's record changed since it was read.
	ErrConflict = errors.New("cycle: conflict")
)

// The line's limits and rules.
const (
	// MaxCycles is the most cycles in a line: the current one and the next
	// two.
	MaxCycles = 3
	// Turnaround is the days after a harvest to prepare the land before
	// the next cycle can be sown.
	Turnaround = 15
	// MaxWait is how many days after the land is free a suggested sowing
	// may be: a rainfed plot waits through the dry months for the rains.
	MaxWait = 240
)

// Method is how a cycle is grown.
type Method string

// The methods. A cycle with none set is conventional.
const (
	Conventional Method = "conventional"
	Organic      Method = "organic"
)

// Origin says whose a cycle is.
type Origin string

// The origins.
const (
	Suggested Origin = "suggested"
	Farmer    Origin = "farmer"
)

// DateKind says whether a sowing date is a plan or what happened.
type DateKind string

// The kinds of sowing date.
const (
	Planned DateKind = "planned"
	Actual  DateKind = "actual"
)

// Area is one area of a cycle (a zone of its plan) with its crop and dates.
type Area struct {
	Zone     int     `json:"zone"`
	Crop     string  `json:"crop"`
	CropName string  `json:"crop_name"`
	With     string  `json:"with,omitempty"`
	AreaM2   float64 `json:"area_m2"`
	AreaHa   float64 `json:"area_ha"`
	// Sow is the sowing date, planned or (SowKind actual) real; Harvest
	// the expected harvest, from the crop's days (the later of an
	// intercrop's two).
	Sow     string   `json:"sow"`
	SowKind DateKind `json:"sow_kind"`
	Harvest string   `json:"harvest"`
}

// Cycle is one crop season on the plot.
type Cycle struct {
	Origin Origin `json:"origin"`
	// Season is the season of its first sowing: kharif, rabi or zaid.
	Season string    `json:"season"`
	Method Method    `json:"method"`
	Plan   agro.Plan `json:"plan"`
	Areas  []Area    `json:"areas"`
	// Sow is the first area's sowing, Harvest the last area's harvest.
	// SowKind is actual once any area's sowing is recorded.
	Sow     string   `json:"sow"`
	SowKind DateKind `json:"sow_kind"`
	Harvest string   `json:"harvest"`
	// Perennial is a cycle with a perennial crop: it holds the land, so
	// no cycle follows it.
	Perennial bool `json:"perennial,omitempty"`
	// Score is a suggestion's score (0 to 100, the recommender's with the
	// rotation's), Reasons and Risks why; empty for the farmer's own.
	Score   int         `json:"score,omitempty"`
	Reasons []agro.Note `json:"reasons,omitempty"`
	Risks   []agro.Note `json:"risks,omitempty"`
}

// MainCrop returns the crop of the cycle's largest area.
func (c Cycle) MainCrop() string {
	best := -1
	for i, a := range c.Areas {
		if best < 0 || a.AreaM2 > c.Areas[best].AreaM2 {
			best = i
		}
	}
	if best < 0 {
		return ""
	}
	return c.Areas[best].Crop
}

// Check is one finding about the farmer's own cycle against the cycle
// before it: never a change, only a word.
type Check struct {
	ID       string        `json:"id"`
	Severity agro.Severity `json:"severity"`
	// Area is the area's index, -1 for the cycle as a whole.
	Area    int    `json:"area"`
	Message string `json:"message"`
}

// Position is one place in the line: what is suggested there, what the
// farmer set, and which is planned.
type Position struct {
	Position int `json:"position"`
	// From is the first day the land is free for it: today for the first
	// (or its own sowing, when that was earlier), else the cycle before's
	// harvest plus Turnaround (never before today).
	From      string `json:"from"`
	Suggested *Cycle `json:"suggested,omitempty"`
	Own       *Cycle `json:"own,omitempty"`
	// Planned is Own where there is one, else Suggested; nil when neither.
	Planned *Cycle  `json:"planned,omitempty"`
	Checks  []Check `json:"checks,omitempty"`
	// Addons are what other features computed from the planned cycle,
	// by the addon's name.
	Addons map[string]any `json:"addons,omitempty"`
}

// Line is a plot's cycles, in order.
type Line struct {
	Farm  string `json:"farm"`
	Today string `json:"today"`
	// Future is false when the farmer turned the later cycles off.
	Future bool       `json:"future"`
	Cycles []Position `json:"cycles"`
	// Notes are about the line as a whole: no climate, an orchard that
	// holds the land, an addon that could not answer.
	Notes []agro.Note `json:"notes,omitempty"`
}

// Slot is what the farmer set at one position.
type Slot struct {
	// Plan is the farmer's own plan at positions 1 and 2; always nil at
	// 0, whose own plan is the plot's saved plan.
	Plan *agro.Plan `json:"plan,omitempty"`
	// Method is the method chosen; empty for conventional.
	Method Method `json:"method,omitempty"`
	// Sown is a sowing date the farmer typed for the current cycle
	// (position 0 only), for the saved plan whose key (PlanKey) is SownFor.
	Sown    string `json:"sown,omitempty"`
	SownFor string `json:"sown_for,omitempty"`
}

// Record is what is stored for a plot.
type Record struct {
	Farm string `json:"farm"`
	// Version is the stored version: 0 for a plot with none stored. Put
	// stores a record only over the version it was read at.
	Version int64 `json:"version"`
	// FutureOff turns the later cycles off: the line is the current
	// cycle alone. Own later cycles are kept, not shown.
	FutureOff bool            `json:"future_off,omitempty"`
	Slots     [MaxCycles]Slot `json:"slots"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// Store keeps plots' records. Implementations are safe for concurrent use
// and pass cycletest.RunStore.
type Store interface {
	// Get returns a plot's record; a plot with none stored gets an empty
	// one (Version 0) with its Farm set.
	Get(ctx context.Context, farmID string) (Record, error)
	// Put stores r if the stored version is r.Version (0: none stored),
	// and returns it as stored, with the next version; ErrConflict if the
	// stored version is another. The returned record shares no memory
	// with r.
	Put(ctx context.Context, r Record) (Record, error)
}

// Addon adds to each planned cycle what another feature computes from it:
// a shopping list and spray schedule from its crops, areas and method
// (package inputs), harvest-month advice from prices. Its result is in
// Position.Addons under its Name (none when it returns nil); when it
// fails, the line goes without it and says so in a note, which names it by
// its Label when it has a `Label() string` method.
type Addon interface {
	Name() string
	For(ctx context.Context, f farm.Farm, c Cycle) (any, error)
}
