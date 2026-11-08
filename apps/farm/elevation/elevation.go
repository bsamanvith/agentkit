// Package elevation looks up the height of the ground at points, for the
// slope of a plot: which corner is lowest (where water collects, where a
// farm pond goes) and how much the land falls.
//
// Two free services answer, one the backup of the other ([Fallback]):
//
//   - [OpenMeteo] asks Open-Meteo's elevation API
//     (https://open-meteo.com/en/docs/elevation-api), which reads the
//     Copernicus DEM GLO-90: heights on a 90 m grid, to about a metre or two.
//   - [OpenTopoData] asks Open Topo Data's public API
//     (https://www.opentopodata.org), by default its SRTM 30 m dataset
//     (NASA's Shuttle Radar Topography Mission, 30 m grid, 60° N to 56° S).
//
// A small plot can sit inside one or two cells, so a fall of less than a
// couple of metres is "flat at this resolution", not a measured slope.
// Heights from two models differ by a few metres, so one survey takes all
// its heights from one source: a fallback never mixes them.
//
// Each client remembers the heights it was given in a [Cache], which can be
// kept in a file across restarts, and stops asking a service that said it
// is over its rate limit until the limit has passed.
package elevation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/bsamanvith/agentkit/apps/farm/geo"
)

// ErrUnavailable is a lookup the server could not answer.
var ErrUnavailable = errors.New("elevation: the server could not answer")

// ErrRateLimited is a service that said it is over its rate limit (HTTP
// 429); it is an [ErrUnavailable].
var ErrRateLimited = fmt.Errorf("%w: over its rate limit", ErrUnavailable)

// Heights is an answer: the heights in metres above sea level, in the order
// of the points asked for, and where they come from.
type Heights struct {
	Metres []float64
	// From names the elevation model and the service, for the farmer.
	From string
	// ResolutionM is the model's grid in metres.
	ResolutionM float64
}

// Source gives the ground's height at points. Implementations are safe for
// concurrent use.
type Source interface {
	Elevations(ctx context.Context, ps []geo.Point) (Heights, error)
	// Name says where the heights come from.
	Name() string
}

// Func is a Source from a function, for tests and offline use. Its
// heights are said to be on a 90 m grid.
type Func func(geo.Point) float64

// Elevations implements Source.
func (f Func) Elevations(_ context.Context, ps []geo.Point) (Heights, error) {
	out := make([]float64, len(ps))
	for i, p := range ps {
		out[i] = f(p)
	}
	return Heights{Metres: out, From: f.Name(), ResolutionM: 90}, nil
}

// Name implements Source.
func (Func) Name() string { return "fixed heights" }

// Fallback asks each source in turn and returns the first full answer, so
// one survey's heights all come from one model. log (nil for none) hears
// why a source was passed over.
func Fallback(log *slog.Logger, srcs ...Source) Source {
	if len(srcs) == 1 {
		return srcs[0]
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &fallback{srcs: srcs, log: log}
}

type fallback struct {
	srcs []Source
	log  *slog.Logger
}

// Name implements Source.
func (f *fallback) Name() string {
	names := make([]string, len(f.srcs))
	for i, s := range f.srcs {
		names[i] = s.Name()
	}
	return strings.Join(names, ", then ")
}

// Elevations implements Source.
func (f *fallback) Elevations(ctx context.Context, ps []geo.Point) (Heights, error) {
	var errs []error
	for i, s := range f.srcs {
		h, err := s.Elevations(ctx, ps)
		if err == nil {
			return h, nil
		}
		if ctx.Err() != nil {
			return Heights{}, err
		}
		errs = append(errs, fmt.Errorf("%s: %w", s.Name(), err))
		if i+1 < len(f.srcs) {
			f.log.WarnContext(ctx, "elevation: trying the next source", "source", s.Name(), "next", f.srcs[i+1].Name(), "err", err)
		}
	}
	return Heights{}, errors.Join(errs...)
}
