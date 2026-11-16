package farmagent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
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
- A farmer who has not drawn a plot yet, or asks about a village or place rather than one of their plots ("what can I sow in Pulivendula now?"), gets recommend_for_place with the place's name (or a position), never a guess. For a saved plot use recommend_crops. Say the assumptions it returns in a few plain words ("assuming rain only, no irrigation") and offer to redo it with their water and soil.`

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
}

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
	if in.Limit == 0 {
		in.Limit = 5
	}
	r, err := p.RecommendAt(ctx, at, planner.Ask{Around: in.Around, Spread: in.Spread, Crops: in.Crops, Limit: in.Limit})
	if err != nil {
		return PlaceAnswer{}, err
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
	t, err := tool.Func(RecommendForPlaceTool, "Recommend crops and when to sow them at a village or place where the farmer has no plot drawn yet, by the same rules as recommend_crops: the climate there, the district's own sowing windows, and the water and soil the farmer gives (left out: assumed rain only and the soil not known, listed in 'assumed'). Returns the place found, its region layers, the assumptions, and options best first with score, sowing and harvest dates, the good sowing window, water balance, reasons and risks. Use recommend_crops instead for a saved plot.",
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
