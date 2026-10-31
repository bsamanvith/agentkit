package farm

import (
	"context"
	"errors"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
)

// Errors callers branch on.
var (
	// ErrNotFound: no farm has the ID.
	ErrNotFound = errors.New("farm: not found")
	// ErrInvalid: the request is malformed (an empty name, an unknown
	// unit). A boundary that fails verification is an [*InvalidError],
	// which also matches ErrInvalid.
	ErrInvalid = errors.New("farm: invalid")
)

// InvalidError is a boundary that failed verification: Report says why.
type InvalidError struct{ Report Report }

func (e *InvalidError) Error() string {
	for _, c := range e.Report.Checks {
		if c.Severity == SeverityError {
			return "farm: invalid boundary: " + c.Message
		}
	}
	return "farm: invalid boundary"
}

// Is makes errors.Is(err, ErrInvalid) true.
func (e *InvalidError) Is(target error) bool { return target == ErrInvalid }

// Source is how a boundary was captured.
type Source string

// The ways a farmer gives a boundary.
const (
	SourceMap   Source = "map"   // tapped on a map
	SourceGPS   Source = "gps"   // walked, a GPS fix at each corner
	SourceTyped Source = "typed" // typed or pasted coordinates
)

// Corner is one corner of a boundary. AccuracyM is the GPS fix's accuracy
// radius in metres (95%), zero when not known.
type Corner struct {
	geo.Point
	AccuracyM float64 `json:"accuracy_m,omitempty"`
}

// Boundary is a plot's outline: its corners in order, either way round.
type Boundary struct {
	Corners []Corner `json:"corners"`
	Source  Source   `json:"source,omitempty"`
}

// Ring returns the boundary's corners as a ring.
func (b Boundary) Ring() geo.Ring {
	r := make(geo.Ring, len(b.Corners))
	for i, c := range b.Corners {
		r[i] = c.Point
	}
	return r
}

// Farm is one plot of land.
type Farm struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Declared is the size the farmer believes the plot is, from their
	// papers or memory; nil if they do not know.
	Declared *Area    `json:"declared,omitempty"`
	Boundary Boundary `json:"boundary"`
	// Report is the boundary's verification, as of its last change.
	Report Report `json:"report"`
	// Profile is what the farmer told us about the land and water; nil
	// until they do.
	Profile *agro.Profile `json:"profile,omitempty"`
	// Plan is the season's plan, checked when it was saved, with its
	// report; nil until there is one.
	Plan       *agro.Plan       `json:"plan,omitempty"`
	PlanReport *agro.PlanReport `json:"plan_report,omitempty"`
	CreatedAt  time.Time        `json:"created_at"`
	UpdatedAt  time.Time        `json:"updated_at"`
}

// Store keeps farms. Implementations are safe for concurrent use and pass
// farmtest.RunStore.
type Store interface {
	// Create saves a new farm; ErrInvalid if its ID is taken.
	Create(ctx context.Context, f Farm) error
	// Get returns a farm; ErrNotFound if there is none.
	Get(ctx context.Context, id string) (Farm, error)
	// List returns every farm, oldest first.
	List(ctx context.Context) ([]Farm, error)
	// Update replaces a farm; ErrNotFound if there is none.
	Update(ctx context.Context, f Farm) error
	// Delete removes a farm; ErrNotFound if there is none.
	Delete(ctx context.Context, id string) error
}
