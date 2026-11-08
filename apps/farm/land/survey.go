package land

import (
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/osm"
)

// Survey distances and limits.
const (
	// AroundM is how far around the plot a survey looks.
	AroundM = 400
	// MaxRadiusM caps the radius of the box asked for (from the plot's
	// centre): about the map call's largest box.
	MaxRadiusM = 1000
	// TouchM: a feature this close to the boundary touches it (a road
	// along the plot, give or take the road's own width).
	TouchM = 8
	// MaxNearby is how many features a survey lists, nearest first.
	MaxNearby = 60
	// MaxDrawn is how many features a survey keeps to draw.
	MaxDrawn = 500
	// FlatM: a fall smaller than this is below what a 30 or 90 m elevation
	// model can tell.
	FlatM = 2
)

// Radius is how far from r's centroid a survey must look: past its
// farthest corner by AroundM, at most MaxRadiusM.
func Radius(r geo.Ring) float64 {
	c := geo.Centroid(r)
	far := 0.0
	for _, p := range r {
		far = math.Max(far, geo.Distance(c, p))
	}
	return math.Min(far+AroundM, MaxRadiusM)
}

// Nearby is a feature near a plot: what it is and where.
type Nearby struct {
	ID    string   `json:"id"`
	Kind  osm.Kind `json:"kind"`
	Class string   `json:"class,omitempty"`
	Name  string   `json:"name,omitempty"`
	Ref   string   `json:"ref,omitempty"`
	// DistanceM is from the plot's boundary, in whole metres: 0 when it
	// touches, crosses or is inside the plot.
	DistanceM float64 `json:"distance_m"`
	// Direction is the compass point from the plot to the feature
	// (N, NE, ...); "inside" for a feature in the plot.
	Direction string `json:"direction"`
	// Edge is the plot's side nearest to it (side i runs from corner i).
	Edge int `json:"edge"`
	// Touches: it runs along or meets the boundary (within TouchM).
	// Crosses: it passes through the plot. Inside: some of it is in the
	// plot.
	Touches bool `json:"touches,omitempty"`
	Crosses bool `json:"crosses,omitempty"`
	Inside  bool `json:"inside,omitempty"`
	// At is its nearest point to the plot; From is the plot's.
	At   geo.Point `json:"at"`
	From geo.Point `json:"from"`
}

// Label names the feature for a farmer: "trunk road NH216E (Amalapuram -
// Bobbarlanka Road)", "canal", "pond Village tank".
func (n Nearby) Label() string {
	s := string(n.Kind)
	if n.Class != "" && n.Class != string(n.Kind) && n.Class != "yes" {
		s = n.Class + " " + s
	}
	if n.Ref != "" {
		s += " " + n.Ref
	}
	if n.Name != "" {
		s += " (" + n.Name + ")"
	}
	return s
}

// Terrain is how the land lies, from heights at the corners and centre.
type Terrain struct {
	Source string `json:"source"`
	// Heights at the sample points: the corners (or up to 24 of them) then
	// the centre, metres above sea level.
	Points  []geo.Point `json:"points"`
	Heights []float64   `json:"heights"`
	// LowestCorner and HighestCorner index Points.
	Lowest  int     `json:"lowest"`
	Highest int     `json:"highest"`
	FallM   float64 `json:"fall_m"`
	// SlopePct is the fall over the distance between the highest and
	// lowest points, in percent.
	SlopePct float64 `json:"slope_pct"`
	// Downhill is the compass point the land falls towards.
	Downhill string `json:"downhill,omitempty"`
	// Flat: the fall is below what the elevation model can tell (FlatM).
	Flat bool `json:"flat"`
	// ResolutionM is the elevation model's grid.
	ResolutionM float64 `json:"resolution_m"`
}

// Note is something a survey or a design check found.
type Note struct {
	ID       string `json:"id"`
	Severity string `json:"severity"` // info, warning, error
	Message  string `json:"message"`
	// Feature is the feature it is about, if any.
	Feature string `json:"feature,omitempty"`
	// Element is the design element it is about, -1 for none.
	Element int `json:"element"`
}

// Severities.
const (
	Info    = "info"
	Warning = "warning"
	Error   = "error"
)

// Survey is a plot and what is around it.
type Survey struct {
	Shape   Shape    `json:"shape"`
	Terrain *Terrain `json:"terrain,omitempty"`
	// RadiusM is how far from the plot's centre features were read.
	RadiusM float64 `json:"radius_m"`
	// Nearest is the nearest feature of each kind, nearest first.
	Nearest []Nearby `json:"nearest"`
	// Nearby is every feature within AroundM of the boundary, nearest
	// first, at most MaxNearby.
	Nearby []Nearby `json:"nearby"`
	// Counts is how many features of each kind are within AroundM.
	Counts map[osm.Kind]int `json:"counts"`
	// Features are the features within the radius, to draw.
	Features []osm.Feature `json:"features"`
	Notes    []Note        `json:"notes"`
	// Sources says where the data came from; Missing what could not be
	// looked up and why.
	Sources    []string  `json:"sources"`
	Missing    []string  `json:"missing,omitempty"`
	SurveyedAt time.Time `json:"surveyed_at"`
}

// Find returns the nearest feature of a kind, if the survey has one.
func (s *Survey) Find(k osm.Kind) (Nearby, bool) {
	for _, n := range s.Nearest {
		if n.Kind == k {
			return n, true
		}
	}
	return Nearby{}, false
}

// Inputs is what [Analyse] reads, fetched by the caller.
type Inputs struct {
	// Features are the map's features in the box of Radius; FeaturesFrom
	// names their source. FeaturesErr says why there are none, if the
	// lookup failed.
	Features     []osm.Feature
	FeaturesFrom string
	FeaturesErr  error
	// Heights are the ground's heights at SamplePoints(r), from
	// HeightsFrom; HeightsErr says why there are none.
	Heights     []float64
	HeightsFrom string
	HeightsErr  error
	ResolutionM float64
	Now         time.Time
}

// SamplePoints are where a survey wants the ground's height: the corners
// (up to 24, evenly) and the centre.
func SamplePoints(r geo.Ring) []geo.Point {
	r = r.Open()
	var out []geo.Point
	step := 1.0
	if len(r) > 24 {
		step = float64(len(r)) / 24
	}
	for f := 0.0; int(f) < len(r) && len(out) < 24; f += step {
		out = append(out, r[int(f)])
	}
	return append(out, geo.Centroid(r))
}

// Analyse surveys plot r from its inputs.
func Analyse(r geo.Ring, in Inputs) (Survey, error) {
	r = r.Open()
	shape, err := Describe(r)
	if err != nil {
		return Survey{}, err
	}
	s := Survey{Shape: shape, RadiusM: Radius(r), Counts: map[osm.Kind]int{}, Nearest: []Nearby{}, Nearby: []Nearby{}, Features: []osm.Feature{}, Notes: []Note{}, SurveyedAt: in.Now.UTC()}
	if in.FeaturesErr != nil {
		s.Missing = append(s.Missing, "nearby roads, water and buildings: "+in.FeaturesErr.Error())
	} else if in.FeaturesFrom != "" {
		s.Sources = append(s.Sources, in.FeaturesFrom+" — "+osm.Attribution)
	}
	if in.HeightsErr != nil {
		s.Missing = append(s.Missing, "the slope of the land: "+in.HeightsErr.Error())
	} else if len(in.Heights) > 0 {
		pts := SamplePoints(r)
		if len(in.Heights) != len(pts) {
			return Survey{}, fmt.Errorf("land: %d heights for %d sample points", len(in.Heights), len(pts))
		}
		s.Terrain = terrain(pts, in.Heights, in.HeightsFrom, in.ResolutionM)
		s.Sources = append(s.Sources, in.HeightsFrom)
	}
	var all []Nearby
	drawn := map[string]bool{}
	byID := map[string]osm.Feature{}
	for _, f := range in.Features {
		if len(f.Points) == 0 {
			continue
		}
		c := geo.ClosestTo(r, f.Points, f.Closed)
		n := Nearby{
			ID: f.ID, Kind: f.Kind, Class: f.Class, Name: f.Name, Ref: f.Ref,
			DistanceM: math.Round(c.DistanceM), Edge: c.Edge, At: c.OnShape, From: c.OnRing,
			Crosses: c.Crosses, Inside: c.Inside || c.Encloses, Touches: c.DistanceM <= TouchM,
		}
		switch {
		case n.Inside && !n.Crosses:
			n.Direction = "inside"
		case c.DistanceM < 0.5 && c.Edge >= 0:
			n.Direction = shape.Edges[c.Edge].Faces
		default:
			n.Direction = geo.Compass(geo.Bearing(c.OnRing, c.OnShape))
		}
		byID[f.ID] = f
		if geo.Distance(shape.Centroid, c.OnShape) <= s.RadiusM+50 && !drawn[f.ID] {
			drawn[f.ID] = true
			s.Features = append(s.Features, f)
		}
		all = append(all, n)
	}
	slices.SortStableFunc(all, func(a, b Nearby) int {
		if a.DistanceM != b.DistanceM {
			return cmpF(a.DistanceM, b.DistanceM)
		}
		return cmpS(a.ID, b.ID)
	})
	seen := map[osm.Kind]bool{}
	for _, n := range all {
		if !seen[n.Kind] {
			seen[n.Kind] = true
			s.Nearest = append(s.Nearest, n)
		}
		if n.DistanceM <= AroundM {
			s.Counts[n.Kind]++
			if len(s.Nearby) < MaxNearby {
				s.Nearby = append(s.Nearby, n)
			}
		}
	}
	if len(s.Features) > MaxDrawn {
		// Keep the nearest to draw.
		rank := map[string]int{}
		for i, n := range all {
			if _, ok := rank[n.ID]; !ok {
				rank[n.ID] = i
			}
		}
		slices.SortStableFunc(s.Features, func(a, b osm.Feature) int { return rank[a.ID] - rank[b.ID] })
		s.Features = s.Features[:MaxDrawn]
		clear(drawn)
		for _, f := range s.Features {
			drawn[f.ID] = true
		}
	}
	// The nearest of each kind is drawn even when farther out, so the
	// picture can show what a callout points at.
	for _, n := range s.Nearest {
		if !drawn[n.ID] {
			drawn[n.ID] = true
			s.Features = append(s.Features, byID[n.ID])
		}
	}
	s.Notes = surveyNotes(&s, in.FeaturesErr == nil && in.FeaturesFrom != "")
	return s, nil
}

func cmpF(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func cmpS(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func terrain(pts []geo.Point, hs []float64, from string, res float64) *Terrain {
	if res == 0 {
		res = 90
	}
	t := &Terrain{Source: from, Points: pts, Heights: hs, ResolutionM: res}
	for i, h := range hs {
		if h < hs[t.Lowest] {
			t.Lowest = i
		}
		if h > hs[t.Highest] {
			t.Highest = i
		}
	}
	t.FallM = hs[t.Highest] - hs[t.Lowest]
	if d := geo.Distance(pts[t.Highest], pts[t.Lowest]); d > 0 {
		t.SlopePct = math.Round(t.FallM/d*1000) / 10
	}
	t.Flat = t.FallM < FlatM
	if !t.Flat {
		t.Downhill = geo.Compass(geo.Bearing(pts[t.Highest], pts[t.Lowest]))
	}
	return t
}

// The ids of survey notes.
const (
	NoteRoadFrontage = "road_frontage"
	NoteRoadNear     = "road_near"
	NoteNoRoad       = "no_road"
	NoteBusyRoad     = "busy_road"
	NoteCanal        = "canal_near"
	NoteDrain        = "drain_near"
	NoteWaterBody    = "water_body_near"
	NoteWaterCrosses = "water_crosses"
	NotePower        = "power_line"
	NoteBuildings    = "buildings_near"
	NoteBuildingIn   = "building_inside"
	NoteVillage      = "village"
	NoteRail         = "railway_near"
	NoteSlope        = "slope"
	NoteFlat         = "flat"
	NoteNoMapData    = "no_map_data"
)

func surveyNotes(s *Survey, haveMap bool) []Note {
	out := []Note{}
	add := func(id, sev, msg, feature string) {
		out = append(out, Note{ID: id, Severity: sev, Message: msg, Feature: feature, Element: -1})
	}
	if t := s.Terrain; t != nil {
		if t.Flat {
			add(NoteFlat, Info, fmt.Sprintf("The land is flat at the elevation model's %.0f m grid (fall under %d m): look at where rain water stands to find the low corner.", t.ResolutionM, FlatM), "")
		} else {
			add(NoteSlope, Info, fmt.Sprintf("The land falls about %.0f m towards the %s (%.1f%%): rain runs that way; a pond or a drain goes at the low end, and bunds across the slope hold water.", t.FallM, Words(t.Downhill), t.SlopePct), "")
		}
	}
	if !haveMap {
		add(NoteNoMapData, Warning, "Nearby roads, water and buildings could not be looked up; the survey has the shape and slope only.", "")
		return out
	}
	road, okRoad := nearestOf(s, osm.Road, osm.Track)
	switch {
	case okRoad && road.Touches:
		add(NoteRoadFrontage, Info, fmt.Sprintf("The plot meets a %s on its %s side: that is where the gate goes, and produce can leave by vehicle.", road.Label(), Words(road.Direction)), road.ID)
	case okRoad && road.DistanceM <= 500:
		add(NoteRoadNear, Info, fmt.Sprintf("The nearest %s is %.0f m to the %s: check there is a right of way to it.", road.Label(), road.DistanceM, Words(road.Direction)), road.ID)
	default:
		add(NoteNoRoad, Warning, "No road or track within 500 m on the map: a farm with no vehicle access costs more to run; the map can also be missing village tracks.", "")
	}
	for _, n := range s.Nearby {
		if n.Kind == osm.Road && (n.Class == "trunk" || n.Class == "primary" || n.Class == "motorway") && n.DistanceM <= 50 {
			add(NoteBusyRoad, Warning, fmt.Sprintf("A busy %s passes %.0f m away: dust and fumes reach the nearest rows; a hedge or tree line on that side helps, and keep animals and children away from it.", n.Label(), n.DistanceM), n.ID)
			break
		}
	}
	for _, k := range []osm.Kind{osm.Canal, osm.River, osm.Stream} {
		if n, ok := s.Find(k); ok && n.DistanceM <= 300 {
			msg := fmt.Sprintf("A %s is %.0f m to the %s: a possible water source (lifting from it may need the irrigation department's permission).", n.Label(), n.DistanceM, Words(n.Direction))
			if n.Crosses || n.Inside {
				add(NoteWaterCrosses, Warning, fmt.Sprintf("A %s runs through the plot: keep its banks free, and plan the fields on each side.", n.Label()), n.ID)
				continue
			}
			add(NoteCanal, Info, msg, n.ID)
		}
	}
	if n, ok := s.Find(osm.Drain); ok && n.DistanceM <= 200 {
		add(NoteDrain, Info, fmt.Sprintf("A drain is %.0f m to the %s: excess water from the plot can go there in heavy rain.", n.DistanceM, Words(n.Direction)), n.ID)
	}
	if n, ok := s.Find(osm.Water); ok && n.DistanceM <= 300 {
		add(NoteWaterBody, Info, fmt.Sprintf("A %s is %.0f m to the %s: groundwater nearby is usually higher; it also brings birds to ripening grain.", n.Label(), n.DistanceM, Words(n.Direction)), n.ID)
	}
	if n, ok := s.Find(osm.Power); ok && n.DistanceM <= 20 {
		add(NotePower, Warning, fmt.Sprintf("An overhead power line is %.0f m away (%s): no tall trees, sheds or cranes under it; a pump connection is close.", n.DistanceM, Words(n.Direction)), n.ID)
	}
	if n, ok := s.Find(osm.Rail); ok && n.DistanceM <= 100 {
		add(NoteRail, Info, fmt.Sprintf("A railway line is %.0f m to the %s.", n.DistanceM, Words(n.Direction)), n.ID)
	}
	inside, near := 0, 0
	for _, n := range s.Nearby {
		if n.Kind != osm.Building {
			continue
		}
		if n.Inside {
			inside++
		} else if n.DistanceM <= 50 {
			near++
		}
	}
	if inside > 0 {
		add(NoteBuildingIn, Info, fmt.Sprintf("The map shows %d building(s) inside the plot: leave them out of the cropped area.", inside), "")
	}
	if near >= 3 {
		add(NoteBuildings, Info, fmt.Sprintf("%d buildings within 50 m: houses close by mean spray drift must be avoided, and stray animals and theft are likelier; a fence or hedge helps.", near), "")
	}
	if n, ok := s.Find(osm.Settlement); ok && n.DistanceM <= 3000 {
		where := fmt.Sprintf("%.0f m to the %s", n.DistanceM, Words(n.Direction))
		if n.DistanceM == 0 {
			where = "around the plot"
		}
		add(NoteVillage, Info, fmt.Sprintf("Nearest settlement: %s, %s.", n.Label(), where), n.ID)
	}
	return out
}

func nearestOf(s *Survey, kinds ...osm.Kind) (Nearby, bool) {
	var best Nearby
	ok := false
	for _, n := range s.Nearest {
		if slices.Contains(kinds, n.Kind) && (!ok || n.DistanceM < best.DistanceM) {
			best, ok = n, true
		}
	}
	return best, ok
}
