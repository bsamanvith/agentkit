// Package planting spreads a plot group's plant groups over its plots and
// works out what the spread does.
//
// A distribution names plant groups (fruit trees, vegetables, fodder, ...)
// and where each goes: a block of the land, a band of trees round each
// plot's edge, or rows of trees across the plots with crops in the alleys
// between them. The blocks are cut along each plot (one strip per group by
// its share, strips that alternate across the plot, or whole plots to a
// group), with rows along the long side or north to south.
//
// [Distribute] cuts the plots and measures every piece: its area, rows and
// plant counts, the trees it holds. Then it works out the effects of the
// arrangement on the plot's shape, as [Effect]s:
//
//   - shade: where taller plants keep shorter ones in shade for half the
//     day or more, sampled hour by hour on the shortest day of the year;
//   - edges: how much of a group lies near the boundary, where neighbours'
//     pests, stray cattle and tree roots reach, and pieces too narrow for
//     rows or too small to be worth it;
//   - spacing: how many trees the shape really fits against the ideal;
//   - water: whether the thirstiest group is near the water (a farm pond,
//     a canal or well on the map), and crops of very different needs in one
//     watering;
//   - neighbours: good companions and crops of one family that share
//     pests, next to each other or mixed in rows;
//   - wind: the shelter a border of tall trees gives downwind;
//   - layout: what the farm's pond, shed or path takes from each group.
//
// The rules' numbers are starter values, as in package agro.
package planting

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/land"
)

// ErrInvalid is a distribution that cannot be laid out: no block, unknown
// crops, sizes out of range. The error says what.
var ErrInvalid = errors.New("planting: invalid distribution")

// Place is where a plant group goes on each plot.
type Place string

// The places.
const (
	// Block: a share of the land inside the border, cut by the arrangement.
	Block Place = "block"
	// Border: a band round the inside of each plot's boundary.
	Border Place = "border"
	// Alley: rows of trees across each plot, every EveryM, with the
	// blocks' crops in the alleys between them.
	Alley Place = "alley"
)

// Places lists them.
func Places() []Place { return []Place{Block, Border, Alley} }

// Arrange is how the blocks are cut.
type Arrange string

// The arrangements.
const (
	// Strips: each plot cut into one strip per block group, by share.
	Strips Arrange = "strips"
	// Alternate: strips StripM wide that take turns across each plot.
	Alternate Arrange = "alternate"
	// ByPlot: each plot to one block group, the plots shared out by size
	// to come near the shares.
	ByPlot Arrange = "by_plot"
)

// Arranges lists them.
func Arranges() []Arrange { return []Arrange{Strips, Alternate, ByPlot} }

// Rows is the direction the strips and rows run.
type Rows string

// The directions.
const (
	LongSide   Rows = "long_side"   // along each plot's long side: the longest rows
	NorthSouth Rows = "north_south" // north to south: tall rows shade their neighbours least
	EastWest   Rows = "east_west"   // east to west
)

// PlantGroup is plants grown together in one place of the plots.
type PlantGroup struct {
	// ID is short and unique in the distribution: "fruit", "veg".
	ID   string `json:"id" jsonschema:"a short id, unique in the distribution, such as fruit or veg: lower case letters, digits, - and _"`
	Name string `json:"name" jsonschema:"what the farmer calls it, such as Fruit trees"`
	// Crops are crop ids of the library; the first is the main crop, the
	// rest are grown with it in rows that take turns.
	Crops []string `json:"crops" jsonschema:"one to four crop ids of the library (list_crops), the main one first; more are grown with it in alternating rows"`
	Place Place    `json:"place,omitempty" jsonschema:"block (a share of the land, the default), border (a band round each plot's edge) or alley (rows of trees across the plots, crops between)"`
	// Share weighs a block against the other blocks (default 1).
	Share float64 `json:"share,omitempty" jsonschema:"blocks only: its share of the land inside the border, against the other blocks (they are scaled to add up), default 1"`
	// WidthM is a border's or an alley row's band width.
	WidthM float64 `json:"width_m,omitempty" jsonschema:"border or alley: the band's width in metres, default 3 for a border, 2 for an alley row"`
	// EveryM is how far apart alley rows are, centre to centre.
	EveryM float64 `json:"every_m,omitempty" jsonschema:"alley only: metres between tree rows, centre to centre, default 12"`
}

// Distribution is how a group's plant groups are spread over its plots.
type Distribution struct {
	Plants  []PlantGroup `json:"plants" jsonschema:"the plant groups: at least one block, at most one border and one alley"`
	Arrange Arrange      `json:"arrange,omitempty" jsonschema:"how blocks are cut: strips (one per group by share, default), alternate (strips of strip_m taking turns) or by_plot (whole plots to a group)"`
	// StripM is the width of an alternating strip.
	StripM float64 `json:"strip_m,omitempty" jsonschema:"alternate only: each strip's width in metres, default 12"`
	Rows   Rows    `json:"rows,omitempty" jsonschema:"which way strips and rows run: long_side (default), north_south or east_west"`
}

// Plot is one plot of the group, as the distribution sees it.
type Plot struct {
	ID   string
	Name string
	Ring geo.Ring
	// Survey and Design are nil when the plot has none; the survey tells
	// where water is, the design what the pond, shed and path take.
	Survey *land.Survey
	Design *land.Design
}

// Limits of a distribution, and starter values.
const (
	MaxPlants      = 12
	MaxCrops       = 4
	MaxPlots       = 50
	MaxIDLen       = 40
	MaxNameLen     = 80
	MaxSlices      = 400 // bands and strips in one plot
	DefaultShare   = 1
	DefaultBorderM = 3
	DefaultAlleyM  = 2
	DefaultEveryM  = 12
	DefaultStripM  = 12
	// EdgeM is how far in from a boundary the edge reaches: pests from the
	// neighbours, stray cattle, the border trees' roots.
	EdgeM = 5
)

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// normalise fills in defaults and checks d against lib.
func normalise(lib *agro.Library, d Distribution) (Distribution, error) {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if d.Arrange == "" {
		d.Arrange = Strips
	}
	if !slices.Contains(Arranges(), d.Arrange) {
		bad("arrange %q: want strips, alternate or by_plot", d.Arrange)
	}
	if d.Rows == "" {
		d.Rows = LongSide
	}
	if !slices.Contains([]Rows{LongSide, NorthSouth, EastWest}, d.Rows) {
		bad("rows %q: want long_side, north_south or east_west", d.Rows)
	}
	if d.StripM == 0 {
		d.StripM = DefaultStripM
	}
	if d.StripM < 2 || d.StripM > 200 {
		bad("strip_m %v: between 2 and 200 m", d.StripM)
	}
	if len(d.Plants) == 0 || len(d.Plants) > MaxPlants {
		bad("give 1 to %d plant groups, not %d", MaxPlants, len(d.Plants))
	}
	d.Plants = slices.Clone(d.Plants)
	seen := map[string]bool{}
	count := map[Place]int{}
	for i := range d.Plants {
		g := &d.Plants[i]
		g.Name = strings.TrimSpace(g.Name)
		g.Crops = slices.Clone(g.Crops)
		if !idPattern.MatchString(g.ID) || len(g.ID) > MaxIDLen {
			bad("plant group %d: id %q must be lower case letters, digits, - and _, at most %d", i+1, g.ID, MaxIDLen)
		}
		if seen[g.ID] {
			bad("plant group id %q is repeated", g.ID)
		}
		seen[g.ID] = true
		if g.Name == "" {
			g.Name = g.ID
		}
		if len(g.Name) > MaxNameLen {
			bad("plant group %s: name longer than %d", g.ID, MaxNameLen)
		}
		if len(g.Crops) == 0 || len(g.Crops) > MaxCrops {
			bad("plant group %s: give 1 to %d crops", g.ID, MaxCrops)
		}
		for _, c := range g.Crops {
			if _, err := lib.Crop(c); err != nil {
				bad("plant group %s: no crop %q in the library", g.ID, c)
			}
		}
		if len(slices.Compact(slices.Sorted(slices.Values(g.Crops)))) != len(g.Crops) {
			bad("plant group %s: a crop is repeated", g.ID)
		}
		if g.Place == "" {
			g.Place = Block
		}
		count[g.Place]++
		switch g.Place {
		case Block:
			if g.Share == 0 {
				g.Share = DefaultShare
			}
			if !(g.Share > 0 && g.Share <= 100) {
				bad("plant group %s: share %v must be above 0 and at most 100", g.ID, g.Share)
			}
			if g.WidthM != 0 || g.EveryM != 0 {
				bad("plant group %s: width_m and every_m are for borders and alleys", g.ID)
			}
		case Border, Alley:
			if g.Share != 0 {
				bad("plant group %s: share is for blocks; a %s's size is its width_m", g.ID, g.Place)
			}
			if g.WidthM == 0 {
				g.WidthM = map[Place]float64{Border: DefaultBorderM, Alley: DefaultAlleyM}[g.Place]
			}
			if g.WidthM < 0.5 || g.WidthM > 30 {
				bad("plant group %s: width_m %v must be between 0.5 and 30", g.ID, g.WidthM)
			}
			if g.Place == Alley {
				if g.EveryM == 0 {
					g.EveryM = DefaultEveryM
				}
				if g.EveryM < g.WidthM+2 || g.EveryM > 100 {
					bad("plant group %s: every_m %v must be at least width_m + 2 and at most 100", g.ID, g.EveryM)
				}
			} else if g.EveryM != 0 {
				bad("plant group %s: every_m is for alleys", g.ID)
			}
		default:
			bad("plant group %s: place %q: want block, border or alley", g.ID, g.Place)
		}
	}
	if len(d.Plants) > 0 && count[Block] == 0 {
		bad("at least one plant group must be a block: borders and alleys go round and through the blocks")
	}
	if count[Border] > 1 || count[Alley] > 1 {
		bad("at most one border and one alley group")
	}
	if len(errs) > 0 {
		return Distribution{}, fmt.Errorf("%w: %w", ErrInvalid, errors.Join(errs...))
	}
	return d, nil
}

// Severity of an effect.
type Severity string

// The severities.
const (
	Info    Severity = "info"
	Warning Severity = "warning"
	Error   Severity = "error"
)

// Kind is what an effect is about.
type Kind string

// The kinds.
const (
	KindShade      Kind = "shade"
	KindEdge       Kind = "edge"
	KindSpacing    Kind = "spacing"
	KindWater      Kind = "water"
	KindNeighbours Kind = "neighbours"
	KindWind       Kind = "wind"
	KindLayout     Kind = "layout"
)

// Effect is one consequence of the distribution.
type Effect struct {
	ID       string   `json:"id"`
	Kind     Kind     `json:"kind"`
	Severity Severity `json:"severity"`
	// Plant is the plant group it is about, Other a second one; Plot the
	// plot, when it is about one. Empty for none.
	Plant   string `json:"plant,omitempty"`
	Other   string `json:"other,omitempty"`
	Plot    string `json:"plot,omitempty"`
	Message string `json:"message"`
	// AreaM2 is the area it concerns, when it has one.
	AreaM2 float64 `json:"area_m2,omitempty"`
}

// Region is one piece of a plot given to a plant group.
type Region struct {
	Plant string   `json:"plant"`
	Ring  geo.Ring `json:"ring"`
	// Hole is a border's inner edge: the border is between Ring and Hole.
	Hole geo.Ring `json:"hole,omitempty"`
	// AreaM2 is its area less what the farm's layout (pond, shed, path)
	// takes from it.
	AreaM2 float64 `json:"area_m2"`
	// LengthM and WidthM run along and across its rows; Rows is how many
	// rows of the main crop fit across.
	LengthM float64 `json:"length_m"`
	WidthM  float64 `json:"width_m"`
	Rows    int     `json:"rows"`
	// Trees are where trees go, for a group of tree crops.
	Trees []geo.Point `json:"trees,omitempty"`
}

// PlotLayout is one plot cut up.
type PlotLayout struct {
	Plot string `json:"plot"`
	Name string `json:"name"`
	// RowBearing is the rows' direction, degrees from north, 0 to 180.
	RowBearing float64  `json:"row_bearing"`
	AreaM2     float64  `json:"area_m2"`
	Regions    []Region `json:"regions"`
}

// Count is how many plants of a crop.
type Count struct {
	Crop   string `json:"crop"`
	Plants int    `json:"plants"`
}

// Measure is a plant group's totals over the plots.
type Measure struct {
	Plant string `json:"plant"`
	// AreaM2 and Share (of all the plots' land).
	AreaM2 float64 `json:"area_m2"`
	Share  float64 `json:"share"`
	// Want is the share it asked for (blocks), of the land inside borders
	// and alleys; zero for others.
	Want   float64 `json:"want,omitempty"`
	Plots  int     `json:"plots"`
	Pieces int     `json:"pieces"`
	Counts []Count `json:"counts"`
	// HeightM is its tallest crop's height.
	HeightM float64 `json:"height_m"`
	// WaterMM is its main crop's typical need over a season (a year for a
	// perennial).
	WaterMM float64 `json:"water_mm"`
	// EdgeShare is the share of it within EdgeM of a plot's boundary.
	EdgeShare float64 `json:"edge_share"`
	// ShadedM2 is how much of it is in taller plants' shade for half the
	// day or more on the shortest day.
	ShadedM2 float64 `json:"shaded_m2"`
	// NarrowestM is its narrowest piece, across the rows.
	NarrowestM float64 `json:"narrowest_m"`
}

// Status is a distribution's verdict.
type Status string

// The verdicts.
const (
	Sound       Status = "sound"
	NeedsReview Status = "needs_review"
	Invalid     Status = "invalid"
)

// Result is a distribution laid out and its effects.
type Result struct {
	Status       Status       `json:"status"`
	Distribution Distribution `json:"distribution"`
	Plots        []PlotLayout `json:"plots"`
	Plants       []Measure    `json:"plants"`
	Effects      []Effect     `json:"effects"`
	AreaM2       float64      `json:"area_m2"`
}
