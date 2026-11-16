package farmagent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/cycle"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/planner"
	"github.com/bsamanvith/agentkit/apps/farm/surveyor"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// RecommendForPlaceTool recommends crops for a place with no plot.
const RecommendForPlaceTool = "recommend_for_place"

// PlaceSystem is added to the system prompt with recommend_for_place.
const PlaceSystem = `
- A farmer who has not drawn a plot yet, or asks about a village or place rather than one of their plots ("what can I sow in Pulivendula now?"), gets recommend_for_place with the place's name (or a position), never a guess. For a saved plot use recommend_crops. Say the assumptions it returns in a few plain words ("assuming rain only, no irrigation") and offer to redo it with their water and soil. For the open question ("what can I sow now") lead with its headline: what can go in within the next weeks (soon), or that nothing can and what the next good sowing is and when (options); mention trees only as a separate, long-term choice, in a line.`

// ErrNoPlace is a place name the geocoder does not know; ErrPlaceSearch a
// geocoder that could not answer.
var (
	ErrNoPlace     = errors.New("farmagent: no place by that name")
	ErrPlaceSearch = errors.New("farmagent: the place search failed")
)

// PlaceFinder finds places by name; *surveyor.Surveyor is one.
type PlaceFinder interface {
	FindPlace(ctx context.Context, query, farmID string, limit int) ([]surveyor.Found, error)
}

// PlaceAsk asks what to sow at a place with no plot: the place by name or
// position, when, and what the farmer said of the land and water. It is
// the body of POST /api/recommend and the input of recommend_for_place.
type PlaceAsk struct {
	Place  string   `json:"place,omitempty" jsonschema:"the village or town, with its district or state if known: 'Pulivendula, Kadapa'; or give lat and lng"`
	Lat    *float64 `json:"lat,omitempty" jsonschema:"latitude in decimal degrees, instead of place"`
	Lng    *float64 `json:"lng,omitempty" jsonschema:"longitude in decimal degrees, instead of place"`
	Around string   `json:"around,omitempty" jsonschema:"roughly when the farmer wants to sow, as YYYY-MM-DD; leave out for the next good time"`
	Spread int      `json:"spread,omitempty" jsonschema:"how many days either side of around count (default 45)"`
	Crops  []string `json:"crops,omitempty" jsonschema:"only these crop ids, when the farmer asks about or compares particular crops"`
	Limit  int      `json:"limit,omitempty" jsonschema:"how many options at most (default 5)"`
	// The land and water, when the farmer said; left out they are assumed
	// (rain only, soil not known) and the answer says so.
	Soil  string       `json:"soil,omitempty" jsonschema:"the soil id, from list_soils, when the farmer knows it"`
	Water []agro.Water `json:"water,omitempty" jsonschema:"the irrigation sources (canal, river, tank, borewell, open_well, farm_pond), each reliable or not; leave out for rain only"`
	Drip  bool         `json:"drip,omitempty" jsonschema:"the land is under drip or sprinkler"`
	experimentalIn
}

// PlaceAnswer is what to sow at a place with no plot.
type PlaceAnswer struct {
	// Place is the place found, as the geocoder names it, or the position.
	Place    string         `json:"place"`
	Point    geo.Point      `json:"point"`
	Location *farm.Location `json:"location,omitempty"`
	// Regions are the region layers whose rules held, base first.
	Regions []string `json:"regions"`
	// Assumed is what the answer assumed because the farmer did not say.
	Assumed []agro.Note   `json:"assumed,omitempty"`
	Today   string        `json:"today"`
	From    string        `json:"from,omitempty"`
	To      string        `json:"to,omitempty"`
	Soil    string        `json:"soil,omitempty"`
	Options []agro.Option `json:"options"`
	Notes   []agro.Note   `json:"notes,omitempty"`
	// For the open question (no crops named, no date given) the answer
	// is laid out the way the farmer asks it: Soon are the field crops
	// that suit sowing within SoonDays, best first; Options are then the
	// field crops at their best sowing time, whenever that is; Trees the
	// tree and orchard crops, which are a different decision; Headline
	// says in a line whether anything can go in now and what comes next.
	// Crops new to the country are left out, as in the app's own plans.
	Soon     []agro.Option `json:"soon,omitempty"`
	Trees    []agro.Option `json:"trees,omitempty"`
	Headline string        `json:"headline,omitempty"`
}

// SoonDays is how far ahead "can I sow now" looks, as the cycles do.
const SoonDays = cycle.SoonDays

// RecommendForPlace finds the place (by name with find, or at the
// position given), and recommends crops there by the rules a saved plot
// there gets ([planner.Planner.RecommendAt]). A name the geocoder does not
// know is ErrNoPlace, a geocoder that fails ErrPlaceSearch; a name without
// find is surveyor.ErrNoGeocoder.
func RecommendForPlace(ctx context.Context, p *planner.Planner, find PlaceFinder, in PlaceAsk) (PlaceAnswer, error) {
	var out PlaceAnswer
	switch name := strings.TrimSpace(in.Place); {
	case in.Lat != nil && in.Lng != nil:
		out.Point = geo.Point{Lat: *in.Lat, Lng: *in.Lng}
		out.Place = fmt.Sprintf("%.5f, %.5f", out.Point.Lat, out.Point.Lng)
	case name != "":
		if find == nil {
			return PlaceAnswer{}, surveyor.ErrNoGeocoder
		}
		found, err := find.FindPlace(ctx, name, "", 1)
		switch {
		case err == nil:
		case errors.Is(err, surveyor.ErrNoGeocoder), ctx.Err() != nil:
			return PlaceAnswer{}, err
		default:
			return PlaceAnswer{}, fmt.Errorf("%w: %w", ErrPlaceSearch, err)
		}
		if len(found) == 0 {
			return PlaceAnswer{}, fmt.Errorf("%w: %q", ErrNoPlace, name)
		}
		out.Point, out.Place = found[0].Point, found[0].Address
		if out.Place == "" {
			out.Place = found[0].Name
		}
	default:
		return PlaceAnswer{}, fmt.Errorf("%w: give the place's name, or lat and lng", agro.ErrInvalid)
	}
	at := planner.At{Point: out.Point}
	if in.Soil != "" || len(in.Water) > 0 || in.Drip {
		at.Profile = &agro.Profile{Soil: in.Soil, Water: in.Water, Drip: in.Drip}
	}
	if in.Limit == 0 && !in.Experimental {
		in.Limit = 5
	}
	open := len(in.Crops) == 0 && in.Around == "" && !in.Experimental
	ask := planner.Ask{Around: in.Around, Spread: in.Spread, Crops: in.Crops, Limit: in.Limit, Experimental: in.Experimental, Kinds: in.Kinds, Search: in.Search}
	if open {
		ask.Limit = 200 // every crop: they are sorted into field crops and trees below
	}
	r, err := p.RecommendAt(ctx, at, ask)
	if err != nil {
		return PlaceAnswer{}, err
	}
	if open {
		field, trees := sortOut(p, r.Options)
		r.Options = head(field, in.Limit)
		out.Trees = head(trees, 2)
		today, err := time.Parse(agro.DateLayout, r.Today)
		if err != nil {
			return PlaceAnswer{}, err
		}
		half := SoonDays / 2
		sr, err := p.RecommendAt(ctx, at, planner.Ask{Around: today.AddDate(0, 0, half).Format(agro.DateLayout), Spread: half, Limit: 200})
		if err != nil {
			return PlaceAnswer{}, err
		}
		soon, _ := sortOut(p, sr.Options)
		for _, o := range soon {
			if o.Score >= cycle.SoonScore && len(out.Soon) < 3 {
				out.Soon = append(out.Soon, o)
			}
		}
		switch {
		case len(out.Soon) > 0:
			out.Headline = fmt.Sprintf("%s can be sown in the next %d weeks, around %s.", out.Soon[0].Name, SoonDays/7, day(out.Soon[0].Sow))
		case len(r.Options) > 0:
			out.Headline = fmt.Sprintf("No field crop suits sowing in the next %d weeks on what is known. The next good sowing is %s, around %s.", SoonDays/7, strings.ToLower(r.Options[0].Name), day(r.Options[0].Sow))
		default:
			out.Headline = "No field crop in the library suits this place on what is known."
		}
	}
	out.Location, out.Regions, out.Assumed = r.Location, r.Regions, r.Assumed
	if r.Location != nil && in.Lat != nil && r.Location.DisplayName != "" {
		out.Place = r.Location.DisplayName
	}
	out.Today, out.From, out.To, out.Options, out.Notes = r.Today, r.From, r.To, r.Options, r.Notes
	if r.Soil != nil {
		out.Soil = r.Soil.Name
	}
	return out, nil
}

// PlaceTools returns recommend_for_place over p, finding places by name
// with find (nil: by position only). It reads only.
func PlaceTools(p *planner.Planner, find PlaceFinder) ([]tool.Tool, error) {
	t, err := tool.Func(RecommendForPlaceTool, "Recommend crops and when to sow them at a village or place where the farmer has no plot drawn yet, by the same rules as recommend_crops: the climate there, the district's own sowing windows, and the water and soil the farmer gives (left out: assumed rain only and the soil not known, listed in 'assumed'). Returns the place found, its region layers, the assumptions, and options best first with score, sowing and harvest dates, the good sowing window, water balance, reasons and risks. With no crops and no date given it also returns a headline, the field crops that suit sowing within the next six weeks (soon), the field crops at their best sowing time (options) and tree crops apart (trees). Use recommend_crops instead for a saved plot.",
		func(ctx context.Context, in PlaceAsk) (PlaceAnswer, error) {
			out, err := RecommendForPlace(ctx, p, find, in)
			if errors.Is(err, ErrNoPlace) || errors.Is(err, agro.ErrInvalid) {
				return PlaceAnswer{}, &tool.Error{Tool: RecommendForPlaceTool, Kind: tool.ErrInvalidInput, Err: err}
			}
			return out, err
		}, read)
	if err != nil {
		return nil, err
	}
	return []tool.Tool{t}, nil
}

// sortOut splits options into field crops and trees, leaving out crops
// new to the country.
func sortOut(p *planner.Planner, opts []agro.Option) (field, trees []agro.Option) {
	for _, o := range opts {
		c, err := p.Library().Crop(o.Crop)
		if err != nil || c.ClimateAnalog {
			continue
		}
		if c.Perennial {
			trees = append(trees, o)
		} else {
			field = append(field, o)
		}
	}
	return field, trees
}

func head(opts []agro.Option, n int) []agro.Option {
	if len(opts) > n {
		return opts[:n]
	}
	return opts
}

// day writes a YYYY-MM-DD day as "2 August 2027".
func day(d string) string {
	t, err := time.Parse(agro.DateLayout, d)
	if err != nil {
		return d
	}
	return t.Format("2 January 2006")
}
