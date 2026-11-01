// Package osm reads what is around a plot from OpenStreetMap: roads and
// tracks, rivers, canals and drains, ponds and tanks, buildings,
// settlements, power lines, wells and public places, as [Feature]s with
// their geometry.
//
// Two servers can answer, with the same result:
//
//   - [Overpass], the read-only query API meant for this
//     (https://wiki.openstreetmap.org/wiki/Overpass_API). It asks for the
//     tagged features only. Its public instances are run by volunteers and
//     are often busy; give several.
//   - [API], the editing API's map call (/api/0.6/map.json), which returns
//     everything in a small box. The OSM Foundation allows light use with an
//     identifying User-Agent (https://operations.osmfoundation.org/policies/api/);
//     the client asks for boxes of at most [MaxAPIBox] square degrees, one
//     request a second, and caches answers.
//
// [Chain] tries sources in order: Overpass first, the map call when it
// fails. Data © OpenStreetMap contributors, ODbL: show [Attribution] with
// anything drawn from it.
package osm

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/bsamanvith/agentkit/apps/farm/geo"
)

// Attribution is the credit OpenStreetMap's licence asks for.
const Attribution = "© OpenStreetMap contributors (ODbL)"

// Kind is what a feature is, as a farmer would name it.
type Kind string

// The kinds of feature.
const (
	Road       Kind = "road"       // a road a vehicle can use: Class says which (trunk, primary, ..., residential, service)
	Track      Kind = "track"      // a farm or forest track
	Path       Kind = "path"       // a footpath
	Rail       Kind = "rail"       // a railway line
	River      Kind = "river"      // a river
	Stream     Kind = "stream"     // a stream or brook
	Canal      Kind = "canal"      // an irrigation canal
	Drain      Kind = "drain"      // a drain or ditch
	Water      Kind = "water"      // a lake, pond, tank or reservoir: Class says which
	Wetland    Kind = "wetland"    // marsh or swamp
	Building   Kind = "building"   // any building
	Settlement Kind = "settlement" // a village, town or housing area
	Farmland   Kind = "farmland"   // fields, orchards, plantations
	Trees      Kind = "trees"      // forest or woodland
	Industry   Kind = "industry"   // industrial or commercial land
	Power      Kind = "power_line" // an overhead power line
	Well       Kind = "well"       // a water well or water tower
	Facility   Kind = "facility"   // a school, hospital, temple, market, bus stand, ...
)

// Kinds lists every kind.
func Kinds() []Kind {
	return []Kind{Road, Track, Path, Rail, River, Stream, Canal, Drain, Water, Wetland, Building, Settlement, Farmland, Trees, Industry, Power, Well, Facility}
}

// IsWater reports whether k holds or carries water.
func (k Kind) IsWater() bool {
	switch k {
	case River, Stream, Canal, Drain, Water, Wetland, Well:
		return true
	}
	return false
}

// Feature is one thing on the map.
type Feature struct {
	// ID is the OpenStreetMap element: "way/123", "node/45", "relation/6"
	// (a relation's outer ways each become a feature with its ID).
	ID    string `json:"id"`
	Kind  Kind   `json:"kind"`
	Class string `json:"class,omitempty"` // the tag value that made the kind: trunk, canal, pond, school, ...
	Name  string `json:"name,omitempty"`
	Ref   string `json:"ref,omitempty"` // a road's number, such as NH216E
	// Closed: Points bound an area (a pond, a building); otherwise they are
	// a line, or a single point.
	Closed bool        `json:"closed,omitempty"`
	Points []geo.Point `json:"points"`
}

// BBox is a box of latitude and longitude, in degrees.
type BBox struct {
	South float64 `json:"south"`
	West  float64 `json:"west"`
	North float64 `json:"north"`
	East  float64 `json:"east"`
}

// Around returns the box that holds a circle of radius metres around c.
func Around(c geo.Point, radius float64) BBox {
	n, e := geo.Offset(c, 0, radius), geo.Offset(c, 90, radius)
	s, w := geo.Offset(c, 180, radius), geo.Offset(c, 270, radius)
	return BBox{South: s.Lat, West: w.Lng, North: n.Lat, East: e.Lng}
}

// Area is the box's size in square degrees.
func (b BBox) Area() float64 { return (b.North - b.South) * (b.East - b.West) }

// Valid reports whether the box is a real, non-empty box.
func (b BBox) Valid() bool {
	return geo.Point{Lat: b.South, Lng: b.West}.Valid() && geo.Point{Lat: b.North, Lng: b.East}.Valid() &&
		b.North > b.South && b.East > b.West
}

// key rounds the box to about 10 m, for caches.
func (b BBox) key() [4]int64 {
	r := func(x float64) int64 { return int64(math.Round(x * 1e4)) }
	return [4]int64{r(b.South), r(b.West), r(b.North), r(b.East)}
}

// Errors of the sources.
var (
	// ErrBox: the box is not valid, or too big for the source.
	ErrBox = errors.New("osm: box invalid or too big")
	// ErrUnavailable: the server could not answer (down, busy, timed out,
	// an unreadable answer).
	ErrUnavailable = errors.New("osm: the map server could not answer")
)

// Source finds the features in a box. Implementations are safe for
// concurrent use.
type Source interface {
	Features(ctx context.Context, box BBox) ([]Feature, error)
	// Name says where the data comes from, for the farmer.
	Name() string
}

// Chain returns a source that asks each of srcs in turn until one answers.
func Chain(srcs ...Source) Source { return chain(srcs) }

// FeaturesFrom returns src's features in box and the name of the source
// that gave them: for a [Chain], the one that answered.
func FeaturesFrom(ctx context.Context, src Source, box BBox) ([]Feature, string, error) {
	if c, ok := src.(chain); ok {
		return c.featuresFrom(ctx, box)
	}
	fs, err := src.Features(ctx, box)
	return fs, src.Name(), err
}

type chain []Source

func (c chain) Name() string {
	names := make([]string, len(c))
	for i, s := range c {
		names[i] = s.Name()
	}
	return strings.Join(names, ", then ")
}

func (c chain) Features(ctx context.Context, box BBox) ([]Feature, error) {
	fs, _, err := c.featuresFrom(ctx, box)
	return fs, err
}

func (c chain) featuresFrom(ctx context.Context, box BBox) ([]Feature, string, error) {
	var errs []error
	for _, s := range c {
		fs, from, err := FeaturesFrom(ctx, s, box)
		if err == nil {
			return fs, from, nil
		}
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		errs = append(errs, fmt.Errorf("%s: %w", s.Name(), err))
	}
	if len(errs) == 0 {
		return nil, "", fmt.Errorf("%w: no source", ErrUnavailable)
	}
	return nil, "", errors.Join(errs...)
}

// Static is a source of fixed features, for tests and offline use: it
// returns those with a point in the box.
type Static []Feature

// Name implements Source.
func (Static) Name() string { return "fixed map data" }

// Features implements Source.
func (s Static) Features(_ context.Context, box BBox) ([]Feature, error) {
	if !box.Valid() {
		return nil, ErrBox
	}
	var out []Feature
	for _, f := range s {
		if touchesBox(f.Points, box) {
			out = append(out, f)
		}
	}
	return out, nil
}

func touchesBox(ps []geo.Point, b BBox) bool {
	lo := geo.Point{Lat: math.Inf(1), Lng: math.Inf(1)}
	hi := geo.Point{Lat: math.Inf(-1), Lng: math.Inf(-1)}
	for _, p := range ps {
		lo.Lat, lo.Lng = math.Min(lo.Lat, p.Lat), math.Min(lo.Lng, p.Lng)
		hi.Lat, hi.Lng = math.Max(hi.Lat, p.Lat), math.Max(hi.Lng, p.Lng)
	}
	return len(ps) > 0 && lo.Lat <= b.North && hi.Lat >= b.South && lo.Lng <= b.East && hi.Lng >= b.West
}
