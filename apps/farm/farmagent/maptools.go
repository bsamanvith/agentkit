package farmagent

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/land"
	"github.com/bsamanvith/agentkit/apps/farm/osm"
	"github.com/bsamanvith/agentkit/apps/farm/surveyor"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// The map tools' names.
const (
	SurveyLandTool     = "survey_land"
	NearbyTool         = "nearby_features"
	LocatePointTool    = "locate_point"
	MeasureTool        = "measure"
	FindPlaceTool      = "find_place"
	SuggestDesignTool  = "suggest_design"
	CheckDesignTool    = "check_design"
	SaveDesignTool     = "save_design"
	MaxNearbyResults   = 40
	defaultNearbyLimit = 15
)

// MapSystem is added to the system prompt when the assistant has the map
// tools.
const MapSystem = `
You also read the land and shape the farm with the map tools.

- Distances, directions, areas, slopes and what is near a plot come from the tools (survey_land, nearby_features, locate_point, measure, find_place). Never guess a distance or a direction from a name or a coordinate.
- Directions are compass points from the plot (N, NE, ...); say them in words in the farmer's language. Say "about" for straight-line distances: the road may be longer.
- The map (OpenStreetMap) can miss village tracks, small canals and new buildings, and the elevation data is coarse (90 m): when the farmer knows better, believe them, and say the map may be out of date.
- A farm's fixed parts (gate, cart path, shed, farm pond, border trees) come from suggest_design and are checked with check_design; save one with save_design only when the farmer agrees. Explain where each part goes and why, from the tool's reasons.
- Coordinates are decimal degrees, latitude first. When the farmer gives a place by name, find it with find_place.`

type surveyIn struct {
	Farm    string `json:"farm" jsonschema:"the farm's id, from list_farms"`
	Refresh bool   `json:"refresh,omitempty" jsonschema:"look the map up again instead of using the saved survey"`
}

// surveyOut is a survey without the geometry it keeps for drawing.
type surveyOut struct {
	Farm       string           `json:"farm"`
	Shape      land.Shape       `json:"shape"`
	Terrain    *terrainOut      `json:"terrain,omitempty"`
	Nearest    []nearbyOut      `json:"nearest"`
	Nearby     []nearbyOut      `json:"nearby"`
	Counts     map[osm.Kind]int `json:"counts_within_400m"`
	Notes      []land.Note      `json:"notes"`
	Sources    []string         `json:"sources"`
	Missing    []string         `json:"missing,omitempty"`
	SurveyedAt string           `json:"surveyed_at"`
}

type terrainOut struct {
	FallM       float64 `json:"fall_m"`
	SlopePct    float64 `json:"slope_pct"`
	Downhill    string  `json:"downhill,omitempty"`
	Flat        bool    `json:"flat"`
	LowestAt    string  `json:"lowest_at"`
	ResolutionM float64 `json:"resolution_m"`
}

type nearbyOut struct {
	Label     string   `json:"label"`
	Kind      osm.Kind `json:"kind"`
	DistanceM float64  `json:"distance_m"`
	Direction string   `json:"direction"`
	Side      int      `json:"side"`
	Touches   bool     `json:"touches,omitempty"`
	Crosses   bool     `json:"crosses,omitempty"`
	Inside    bool     `json:"inside,omitempty"`
	At        string   `json:"at"`
}

func ll(p geo.Point) string { return fmt.Sprintf("%.6f, %.6f", p.Lat, p.Lng) }

func nearbyOf(n land.Nearby) nearbyOut {
	return nearbyOut{
		Label: n.Label(), Kind: n.Kind, DistanceM: n.DistanceM, Direction: n.Direction, Side: n.Edge,
		Touches: n.Touches, Crosses: n.Crosses, Inside: n.Inside, At: ll(n.At),
	}
}

func surveyOf(id string, s land.Survey, limit int) surveyOut {
	out := surveyOut{
		Farm: id, Shape: s.Shape, Counts: s.Counts, Notes: s.Notes, Sources: s.Sources, Missing: s.Missing,
		SurveyedAt: s.SurveyedAt.Format("2006-01-02"), Nearest: []nearbyOut{}, Nearby: []nearbyOut{},
	}
	out.Shape.Box.Corners = nil
	if t := s.Terrain; t != nil {
		out.Terrain = &terrainOut{FallM: math.Round(t.FallM*10) / 10, SlopePct: t.SlopePct, Downhill: t.Downhill, Flat: t.Flat, ResolutionM: t.ResolutionM}
		if t.Lowest < len(t.Points) {
			out.Terrain.LowestAt = ll(t.Points[t.Lowest])
		}
	}
	for _, n := range s.Nearest {
		out.Nearest = append(out.Nearest, nearbyOf(n))
	}
	for i, n := range s.Nearby {
		if i >= limit {
			break
		}
		out.Nearby = append(out.Nearby, nearbyOf(n))
	}
	return out
}

type nearbyIn struct {
	Farm    string     `json:"farm" jsonschema:"the farm's id, from list_farms"`
	Kinds   []osm.Kind `json:"kinds,omitempty" jsonschema:"only these kinds: road, track, path, rail, river, stream, canal, drain, water, wetland, building, settlement, farmland, trees, industry, power_line, well, facility"`
	WithinM float64    `json:"within_m,omitempty" jsonschema:"only features at most this far from the plot's boundary, in metres (default 400, the survey's reach)"`
	Limit   int        `json:"limit,omitempty" jsonschema:"how many at most, nearest first (default 15, at most 40)"`
}

type nearbyList struct {
	Farm     string      `json:"farm"`
	Features []nearbyOut `json:"features"`
	Note     string      `json:"note,omitempty"`
}

type pointIn struct {
	Lat float64 `json:"lat" jsonschema:"latitude in decimal degrees"`
	Lng float64 `json:"lng" jsonschema:"longitude in decimal degrees"`
}

type measureIn struct {
	Points []geo.Point `json:"points" jsonschema:"two or more points in order, as {lat, lng} in decimal degrees"`
	Closed bool        `json:"closed,omitempty" jsonschema:"the points bound an area: also measure its area and close the last leg"`
}

type leg struct {
	From      int     `json:"from"`
	To        int     `json:"to"`
	DistanceM float64 `json:"distance_m"`
	Bearing   float64 `json:"bearing"`
	Direction string  `json:"direction"`
}

type measureOut struct {
	Legs    []leg    `json:"legs"`
	TotalM  float64  `json:"total_m"`
	AreaM2  *float64 `json:"area_m2,omitempty"`
	Acres   *float64 `json:"acres,omitempty"`
	Warning string   `json:"warning,omitempty"`
}

type findIn struct {
	Query string `json:"query" jsonschema:"the place's name, with its district or state if known: 'Kothapeta, Andhra Pradesh', 'Amalapuram market'"`
	Farm  string `json:"farm,omitempty" jsonschema:"a farm's id: search near it and give each place's distance from it"`
	Limit int    `json:"limit,omitempty" jsonschema:"how many places at most (default 5)"`
}

type designIn struct {
	Farm string `json:"farm" jsonschema:"the farm's id, from list_farms"`
	land.Want
}

type designOut struct {
	Farm   string            `json:"farm"`
	Design []elementOut      `json:"design"`
	Report land.DesignReport `json:"report"`
	Saved  bool              `json:"saved,omitempty"`
	// Raw is the design to pass back to check_design or save_design.
	Raw land.Design `json:"raw"`
}

type elementOut struct {
	Kind     land.ElementKind `json:"kind"`
	Size     string           `json:"size,omitempty"`
	Centre   string           `json:"centre,omitempty"`
	Trees    int              `json:"trees,omitempty"`
	AreaM2   float64          `json:"area_m2,omitempty"`
	VolumeM3 float64          `json:"volume_m3,omitempty"`
	Why      string           `json:"why,omitempty"`
}

type checkDesignIn struct {
	Farm   string       `json:"farm" jsonschema:"the farm's id, from list_farms"`
	Design *land.Design `json:"design,omitempty" jsonschema:"a design to check, as the 'raw' of suggest_design; leave out to check the farm's saved design"`
}

type saveDesignIn struct {
	Farm   string      `json:"farm" jsonschema:"the farm's id, from list_farms"`
	Design land.Design `json:"design" jsonschema:"the design to save, as the 'raw' of suggest_design or check_design"`
}

func describeDesign(id string, d land.Design, rep land.DesignReport) designOut {
	out := designOut{Farm: id, Report: rep, Raw: d, Design: []elementOut{}}
	for i, e := range d.Elements {
		eo := elementOut{Kind: e.Kind, Trees: e.Trees, Why: e.Why}
		if i < len(rep.Elements) {
			eo.AreaM2, eo.VolumeM3 = math.Round(rep.Elements[i].AreaM2), rep.Elements[i].VolumeM3
		}
		switch {
		case e.At != nil:
			eo.Centre = ll(*e.At)
		case len(e.Ring) >= 3 && e.Kind != land.Border:
			eo.Centre = ll(geo.Centroid(e.Ring))
		}
		switch e.Kind {
		case land.Pond:
			eo.Size = fmt.Sprintf("%.0f m × %.0f m, %.0f m deep", e.LengthM, e.WidthM, e.DepthM)
		case land.Shed:
			eo.Size = fmt.Sprintf("%.0f m × %.0f m", e.LengthM, e.WidthM)
		case land.Path:
			eo.Size = fmt.Sprintf("%.0f m long, %.0f m wide", e.LengthM, e.WidthM)
		case land.Border:
			eo.Size = fmt.Sprintf("%.0f m wide, a tree every %.0f m", e.WidthM, e.TreeSpacingM)
		}
		out.Design = append(out.Design, eo)
	}
	return out
}

// MapTools returns the map tools over s: survey_land, nearby_features,
// locate_point, measure, find_place, suggest_design and check_design,
// which read (survey_land saves the survey it makes, a cache), and
// save_design, which saves what the farmer agreed to.
func MapTools(s *surveyor.Surveyor) ([]tool.Tool, error) {
	var tools []tool.Tool
	add := func(t tool.Tool, err error) error {
		if err == nil {
			tools = append(tools, t)
		}
		return err
	}
	invalid := func(name string, err error) error {
		return &tool.Error{Tool: name, Kind: tool.ErrInvalidInput, Err: err}
	}
	err := errors.Join(
		add(tool.Func(SurveyLandTool, "Survey a farm's plot: its shape (size, length and width, which way it runs, how regular, which way each side faces), how the land slopes, and the nearest road, water (canal, river, drain, pond), buildings, power lines and village from the map, with their distance and direction from the plot, and notes on what they mean for farming. Uses the saved survey unless refresh.",
			func(ctx context.Context, in surveyIn) (surveyOut, error) {
				sv, err := s.Survey(ctx, in.Farm, in.Refresh)
				if err != nil {
					return surveyOut{}, err
				}
				return surveyOf(in.Farm, sv, defaultNearbyLimit), nil
			}, read)),
		add(tool.Func(NearbyTool, "List the map features near a farm's plot, nearest first, optionally only some kinds (such as canal and drain, or road) and within a distance of the boundary. With kinds and no distance it also gives the nearest of each kind however far it is.",
			func(ctx context.Context, in nearbyIn) (nearbyList, error) {
				sv, err := s.Survey(ctx, in.Farm, false)
				if err != nil {
					return nearbyList{}, err
				}
				if in.Limit <= 0 {
					in.Limit = defaultNearbyLimit
				}
				in.Limit = min(in.Limit, MaxNearbyResults)
				within := in.WithinM
				switch {
				case within <= 0 && len(in.Kinds) > 0:
					// "How far is the canal?": the nearest canal, wherever it is.
					within = math.Inf(1)
				case within <= 0:
					within = land.AroundM
				}
				out := nearbyList{Farm: in.Farm, Features: []nearbyOut{}}
				// Nearest of each kind reaches past AroundM; Nearby does not.
				pool := sv.Nearby
				if within > land.AroundM {
					pool = append(slices.Clone(sv.Nearby), sv.Nearest...)
				}
				seen := map[string]bool{}
				for _, n := range pool {
					if seen[n.ID] || n.DistanceM > within || (len(in.Kinds) > 0 && !slices.Contains(in.Kinds, n.Kind)) {
						continue
					}
					seen[n.ID] = true
					out.Features = append(out.Features, nearbyOf(n))
				}
				slices.SortStableFunc(out.Features, func(a, b nearbyOut) int { return int(a.DistanceM - b.DistanceM) })
				if len(out.Features) > in.Limit {
					out.Features = out.Features[:in.Limit]
				}
				if len(out.Features) == 0 {
					out.Note = "none on the map within that distance; the map can miss small tracks and channels"
				}
				if len(sv.Missing) > 0 {
					out.Note = "the map could not be read: " + sv.Missing[0]
				}
				return out, nil
			}, read)),
		add(tool.Func(LocatePointTool, "Place a point (latitude and longitude) among the farmer's plots: which plot it is in, and how far and in which direction it is from each plot's boundary.",
			func(ctx context.Context, in pointIn) (surveyor.Located, error) {
				p := geo.Point{Lat: in.Lat, Lng: in.Lng}
				if !p.Valid() {
					return surveyor.Located{}, invalid(LocatePointTool, fmt.Errorf("%v is not a position", p))
				}
				return s.Locate(ctx, p)
			}, read)),
		add(tool.Func(MeasureTool, "Measure between points given as latitude and longitude: each leg's distance and direction, the total, and the area when the points bound one (closed). For a line along a field, a pipe from a well, a fence.",
			func(_ context.Context, in measureIn) (measureOut, error) {
				if len(in.Points) < 2 || len(in.Points) > 500 {
					return measureOut{}, invalid(MeasureTool, errors.New("give between 2 and 500 points"))
				}
				for i, p := range in.Points {
					if !p.Valid() {
						return measureOut{}, invalid(MeasureTool, fmt.Errorf("point %d is not a position", i))
					}
				}
				out := measureOut{Legs: []leg{}}
				n := len(in.Points)
				legs := n - 1
				if in.Closed && n >= 3 {
					legs = n
				}
				for i := range legs {
					a, b := in.Points[i], in.Points[(i+1)%n]
					d, br := geo.Distance(a, b), geo.Bearing(a, b)
					out.Legs = append(out.Legs, leg{From: i, To: (i + 1) % n, DistanceM: math.Round(d*10) / 10, Bearing: math.Round(br), Direction: geo.Compass(br)})
					out.TotalM += d
				}
				out.TotalM = math.Round(out.TotalM*10) / 10
				if in.Closed && n >= 3 {
					a := math.Round(geo.Area(in.Points))
					acres := math.Round(a/4046.8564224*100) / 100
					out.AreaM2, out.Acres = &a, &acres
					if len(geo.SelfIntersections(in.Points)) > 0 {
						out.Warning = "the sides cross each other: the area is not meaningful; put the points in order around the shape"
					}
				}
				if out.TotalM > 50000 {
					out.Warning = "over 50 km: distances are approximate at that range"
				}
				return out, nil
			}, read)),
		add(tool.Func(FindPlaceTool, "Find a place by name on the map (a village, town, market, river, temple): its coordinates, its full address, and, given a farm, its straight-line distance and direction from the plot.",
			func(ctx context.Context, in findIn) (map[string][]surveyor.Found, error) {
				found, err := s.FindPlace(ctx, in.Query, in.Farm, in.Limit)
				if err != nil {
					return nil, err
				}
				return map[string][]surveyor.Found{"places": found}, nil
			}, read)),
		add(tool.Func(SuggestDesignTool, "Lay out a farm's fixed parts on its plot — gate, cart path, shed, farm pond, border trees — from its shape and survey: the gate on the road side, the pond at the low end or by the drain, the shed by the gate. Ask only for the parts the farmer wants; sizes default from the plot. Saves nothing; returns each part with its size, place and reason, the check, and the raw design to save.",
			func(ctx context.Context, in designIn) (designOut, error) {
				d, rep, err := s.Suggest(ctx, in.Farm, in.Want)
				if err != nil {
					return designOut{}, err
				}
				return describeDesign(in.Farm, d, rep), nil
			}, read)),
		add(tool.Func(CheckDesignTool, "Check a farm's design (its saved one, or one given) against the rules: inside the plot, no overlaps, pond setback, size and placement, shed near the gate, gate on the road side, trees and power lines, what is left to crop, what the pond takes from the season plan. Each check has an id, a severity and a message.",
			func(ctx context.Context, in checkDesignIn) (designOut, error) {
				d := in.Design
				saved := false
				if d == nil {
					f, err := s.Farms().Get(ctx, in.Farm)
					if err != nil {
						return designOut{}, err
					}
					if f.Design == nil {
						return designOut{}, invalid(CheckDesignTool, fmt.Errorf("farm %s has no saved design: give one, or call suggest_design", in.Farm))
					}
					d, saved = f.Design, true
				}
				rep, err := s.CheckDesign(ctx, in.Farm, *d)
				if err != nil {
					return designOut{}, err
				}
				out := describeDesign(in.Farm, *d, rep)
				out.Saved = saved
				return out, nil
			}, read)),
		add(tool.Func(SaveDesignTool, "Save a design for a farm once the farmer has agreed to it; it shows on the farmer's map and land view. A design with errors is not saved: fix them first.",
			func(ctx context.Context, in saveDesignIn) (designOut, error) {
				f, err := s.SaveDesign(ctx, in.Farm, in.Design)
				var bad *surveyor.DesignError
				if errors.As(err, &bad) {
					return describeDesign(in.Farm, in.Design, bad.Report), nil
				}
				if err != nil {
					return designOut{}, err
				}
				out := describeDesign(in.Farm, *f.Design, *f.DesignReport)
				out.Saved = true
				return out, nil
			}, write)),
	)
	if err != nil {
		return nil, err
	}
	return tools, nil
}
