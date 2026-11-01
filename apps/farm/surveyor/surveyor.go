// Package surveyor joins the farm store to the map: it surveys a farm's
// plot (package land, on OpenStreetMap features and elevation data), saves
// the survey with the farm, lays out and checks the farm's fixed parts
// (gate, path, shed, pond, border), and answers map questions: which plot a
// point is in, how far a place is, where a named place is.
package surveyor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/elevation"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/land"
	"github.com/bsamanvith/agentkit/apps/farm/nominatim"
	"github.com/bsamanvith/agentkit/apps/farm/osm"
	"github.com/bsamanvith/agentkit/pkg/clock"
)

// ErrNoGeocoder is a place search without a geocoder configured.
var ErrNoGeocoder = errors.New("surveyor: place search is not configured")

// DesignError is a design with errors: it is not saved. Report says why.
type DesignError struct{ Report land.DesignReport }

func (e *DesignError) Error() string {
	for _, c := range e.Report.Checks {
		if c.Severity == land.Error {
			return "surveyor: the design has errors: " + c.Message
		}
	}
	return "surveyor: the design has errors"
}

// Is makes errors.Is(err, farm.ErrInvalid) true.
func (e *DesignError) Is(target error) bool { return target == farm.ErrInvalid }

// Searcher finds places by name; *nominatim.Client is one.
type Searcher interface {
	Search(ctx context.Context, query string, near *geo.Point, country string, limit int) ([]nominatim.Place, error)
}

// Surveyor surveys farms and shapes them. It is safe for concurrent use.
type Surveyor struct {
	farms    *farm.Service
	features osm.Source
	heights  elevation.Source
	search   Searcher
	country  string
	clock    clock.Clock
	timeout  time.Duration
	log      *slog.Logger
}

// Option configures [New].
type Option func(*Surveyor)

// WithFeatures sets where the map's features come from (default: none, a
// survey of the shape and slope only).
func WithFeatures(s osm.Source) Option { return func(x *Surveyor) { x.features = s } }

// WithHeights sets where the ground's heights come from (default: none).
func WithHeights(s elevation.Source) Option { return func(x *Surveyor) { x.heights = s } }

// WithSearch sets the place search, limited to country (ISO code, "" for
// any).
func WithSearch(s Searcher, country string) Option {
	return func(x *Surveyor) { x.search, x.country = s, country }
}

// WithClock sets the clock that dates surveys.
func WithClock(c clock.Clock) Option { return func(x *Surveyor) { x.clock = c } }

// WithLogger logs why a lookup failed (default: no logs).
func WithLogger(l *slog.Logger) Option { return func(x *Surveyor) { x.log = l } }

// WithTimeout bounds each lookup of a survey (default 45 s).
func WithTimeout(d time.Duration) Option { return func(x *Surveyor) { x.timeout = d } }

// New returns a surveyor over farms.
func New(farms *farm.Service, opts ...Option) *Surveyor {
	s := &Surveyor{farms: farms, clock: clock.Real(), timeout: 45 * time.Second, log: slog.New(slog.DiscardHandler)}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Farms returns the farm service.
func (s *Surveyor) Farms() *farm.Service { return s.farms }

// SurveyRing surveys a plot boundary without saving anything. The map and
// the heights are looked up at once; when one fails, the survey says so in
// Missing and goes on without it.
func (s *Surveyor) SurveyRing(ctx context.Context, r geo.Ring) (land.Survey, error) {
	r = r.Open()
	if _, err := land.Describe(r); err != nil {
		return land.Survey{}, fmt.Errorf("%w: %w", farm.ErrInvalid, err)
	}
	in := land.Inputs{Now: s.clock.Now()}
	type fres struct {
		fs   []osm.Feature
		from string
		err  error
	}
	fc := make(chan fres, 1)
	if s.features != nil {
		go func() {
			ctx, cancel := context.WithTimeout(ctx, s.timeout)
			defer cancel()
			fs, from, err := osm.FeaturesFrom(ctx, s.features, osm.Around(geo.Centroid(r), land.Radius(r)))
			fc <- fres{fs, from, err}
		}()
	}
	if s.heights != nil {
		hctx, cancel := context.WithTimeout(ctx, s.timeout)
		hs, err := s.heights.Elevations(hctx, land.SamplePoints(r))
		cancel()
		if err != nil {
			s.log.WarnContext(ctx, "surveyor: heights", "err", err)
			in.HeightsErr = errors.New("the elevation service could not answer")
		} else {
			in.Heights, in.HeightsFrom, in.ResolutionM = hs, s.heights.Name(), elevation.ResolutionM
		}
	}
	if s.features != nil {
		res := <-fc
		if res.err != nil {
			if ctx.Err() != nil {
				return land.Survey{}, ctx.Err()
			}
			s.log.WarnContext(ctx, "surveyor: map features", "err", res.err)
			in.FeaturesErr = errors.New("the map server could not answer; try again later")
		} else {
			in.Features, in.FeaturesFrom = res.fs, res.from
		}
	} else {
		in.FeaturesErr = errors.New("no map server is configured")
	}
	return land.Analyse(r, in)
}

// Survey returns a farm's survey: the saved one, unless refresh or there
// is none, when it surveys the plot again and saves the result (a survey
// whose map lookup failed is returned but not saved, so the next call
// tries again).
func (s *Surveyor) Survey(ctx context.Context, id string, refresh bool) (land.Survey, error) {
	f, err := s.farms.Get(ctx, id)
	if err != nil {
		return land.Survey{}, err
	}
	if f.Survey != nil && !refresh {
		return *f.Survey, nil
	}
	sv, err := s.SurveyRing(ctx, f.Boundary.Ring())
	if err != nil {
		return land.Survey{}, err
	}
	if len(sv.Missing) == 0 {
		if _, err := s.farms.SetSurvey(ctx, id, &sv); err != nil {
			return land.Survey{}, err
		}
	}
	return sv, nil
}

// Suggest lays out the fixed parts w asks for on a farm, from its survey,
// and checks them against the farm's saved plan. It saves nothing.
func (s *Surveyor) Suggest(ctx context.Context, id string, w land.Want) (land.Design, land.DesignReport, error) {
	f, sv, err := s.farmAndSurvey(ctx, id)
	if err != nil {
		return land.Design{}, land.DesignReport{}, err
	}
	d, _, err := land.Suggest(f.Boundary.Ring(), sv, w)
	if err != nil {
		return land.Design{}, land.DesignReport{}, fmt.Errorf("%w: %w", farm.ErrInvalid, err)
	}
	return d, land.CheckDesign(f.Boundary.Ring(), sv, d, zones(f)), nil
}

// CheckDesign checks a design on a farm. It saves nothing.
func (s *Surveyor) CheckDesign(ctx context.Context, id string, d land.Design) (land.DesignReport, error) {
	f, sv, err := s.farmAndSurvey(ctx, id)
	if err != nil {
		return land.DesignReport{}, err
	}
	return land.CheckDesign(f.Boundary.Ring(), sv, d, zones(f)), nil
}

// SaveDesign checks a design and saves it with its report; a design with
// errors is a [*DesignError] and is not saved.
func (s *Surveyor) SaveDesign(ctx context.Context, id string, d land.Design) (farm.Farm, error) {
	rep, err := s.CheckDesign(ctx, id, d)
	if err != nil {
		return farm.Farm{}, err
	}
	if rep.Status == land.DesignInvalid {
		return farm.Farm{}, &DesignError{Report: rep}
	}
	return s.farms.SetDesign(ctx, id, &d, &rep)
}

// ClearDesign removes a farm's design.
func (s *Surveyor) ClearDesign(ctx context.Context, id string) (farm.Farm, error) {
	return s.farms.SetDesign(ctx, id, nil, nil)
}

// farmAndSurvey returns a farm and its survey: the saved one, or a fresh
// one when there is none (nil if the plot cannot be surveyed).
func (s *Surveyor) farmAndSurvey(ctx context.Context, id string) (farm.Farm, *land.Survey, error) {
	f, err := s.farms.Get(ctx, id)
	if err != nil {
		return farm.Farm{}, nil, err
	}
	if f.Survey != nil {
		return f, f.Survey, nil
	}
	sv, err := s.Survey(ctx, id, false)
	if err != nil {
		if errors.Is(err, farm.ErrInvalid) {
			return f, nil, nil
		}
		return farm.Farm{}, nil, err
	}
	return f, &sv, nil
}

func zones(f farm.Farm) []land.Zone {
	if f.Plan == nil {
		return nil
	}
	out := make([]land.Zone, 0, len(f.Plan.Zones))
	for _, z := range f.Plan.Zones {
		out = append(out, land.Zone{Name: z.Crop, Ring: z.Ring})
	}
	return out
}

// Fix is where a point is relative to one plot.
type Fix struct {
	Farm string `json:"farm"`
	Name string `json:"name"`
	// Inside: the point is in the plot. DistanceM is to the plot's
	// boundary (from inside or outside).
	Inside    bool    `json:"inside"`
	DistanceM float64 `json:"distance_m"`
	// Direction is the compass point from the plot to the point, when
	// outside.
	Direction string `json:"direction,omitempty"`
	// Side is the plot's side nearest the point (side i runs from corner
	// i), with the compass point it faces.
	Side  int    `json:"side"`
	Faces string `json:"faces,omitempty"`
}

// Located is a point placed among the farmer's plots.
type Located struct {
	Point geo.Point `json:"point"`
	// Plots, nearest first.
	Plots []Fix `json:"plots"`
}

// Locate places a point among the farmer's plots: which one it is in, and
// how far and which way it is from each.
func (s *Surveyor) Locate(ctx context.Context, p geo.Point) (Located, error) {
	if !p.Valid() {
		return Located{}, fmt.Errorf("%w: %v is not a position", farm.ErrInvalid, p)
	}
	fs, err := s.farms.List(ctx)
	if err != nil {
		return Located{}, err
	}
	out := Located{Point: p, Plots: []Fix{}}
	for _, f := range fs {
		r := f.Boundary.Ring().Open()
		if len(r) < 3 {
			continue
		}
		c := geo.ClosestTo(r, []geo.Point{p}, false)
		fx := Fix{Farm: f.ID, Name: f.Name, Inside: c.Inside, DistanceM: math.Round(c.BoundaryM*10) / 10, Side: c.Edge}
		if !c.Inside {
			fx.Direction = geo.Compass(geo.Bearing(c.OnRing, p))
		}
		if sh, err := land.Describe(r); err == nil && c.Edge >= 0 && c.Edge < len(sh.Edges) {
			fx.Faces = sh.Edges[c.Edge].Faces
		}
		out.Plots = append(out.Plots, fx)
	}
	slices.SortStableFunc(out.Plots, func(a, b Fix) int {
		da, db := a.DistanceM, b.DistanceM
		if a.Inside {
			da = -1
		}
		if b.Inside {
			db = -1
		}
		switch {
		case da < db:
			return -1
		case da > db:
			return 1
		}
		return 0
	})
	return out, nil
}

// Found is a named place and how far it is from a farm.
type Found struct {
	nominatim.Place
	// DistanceM and Direction are from the farm's plot, when one was
	// given: the straight line, not the road.
	DistanceM float64 `json:"distance_m,omitempty"`
	Direction string  `json:"direction,omitempty"`
}

// FindPlace searches for a place by name, near a farm when farmID is not
// empty, with each result's straight-line distance from it.
func (s *Surveyor) FindPlace(ctx context.Context, query, farmID string, limit int) ([]Found, error) {
	if s.search == nil {
		return nil, ErrNoGeocoder
	}
	var ring geo.Ring
	var near *geo.Point
	if farmID != "" {
		f, err := s.farms.Get(ctx, farmID)
		if err != nil {
			return nil, err
		}
		ring = f.Boundary.Ring().Open()
		if len(ring) >= 3 {
			c := geo.Centroid(ring)
			near = &c
		}
	}
	ps, err := s.search.Search(ctx, query, near, s.country, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Found, len(ps))
	for i, p := range ps {
		out[i].Place = p
		if len(ring) >= 3 {
			c := geo.ClosestTo(ring, []geo.Point{p.Point}, false)
			out[i].DistanceM = math.Round(c.DistanceM)
			if !c.Inside {
				out[i].Direction = geo.Compass(geo.Bearing(c.OnRing, p.Point))
			}
		}
	}
	return out, nil
}
