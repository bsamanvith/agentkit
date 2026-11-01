package land_test

import (
	"errors"
	"math"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/land"
	"github.com/bsamanvith/agentkit/apps/farm/osm"
	"github.com/bsamanvith/agentkit/apps/farm/osm/osmtest"
)

var now = time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)

// rect is a plot length m east-west by width m north-south, its south-west
// corner at sw, corners anticlockwise from the south-west.
func rect(sw geo.Point, length, width float64) geo.Ring {
	se := geo.Offset(sw, 90, length)
	return geo.Ring{sw, se, geo.Offset(se, 0, width), geo.Offset(sw, 0, width)}
}

var sw = geo.Point{Lat: 17.2, Lng: 78.3}

func ids(ns []land.Note) []string {
	var out []string
	for _, n := range ns {
		out = append(out, n.ID)
	}
	return out
}

func TestDescribe(t *testing.T) {
	t.Parallel()
	l := rect(sw, 100, 100)
	lShape := geo.Ring{l[0], l[1], geo.Offset(l[1], 0, 50), geo.Offset(geo.Offset(l[1], 0, 50), 270, 50), geo.Offset(l[3], 90, 50), l[3]}
	for _, c := range []struct {
		name string
		r    geo.Ring
		kind land.ShapeKind
	}{
		{"square", rect(sw, 100, 95), land.Square},
		{"rectangle", rect(sw, 150, 60), land.Rectangle},
		{"strip", rect(sw, 300, 25), land.Strip},
		{"triangle", geo.Ring{sw, geo.Offset(sw, 90, 100), geo.Offset(sw, 0, 80)}, land.Triangle},
		{"trapezium", geo.Ring{sw, geo.Offset(sw, 90, 100), geo.Offset(geo.Offset(sw, 90, 70), 0, 60), geo.Offset(sw, 0, 60)}, land.Trapezium},
		{"L", lShape, land.Irregular},
	} {
		s, err := land.Describe(c.r)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if s.Kind != c.kind {
			t.Errorf("%s: kind %s, want %s (%+v)", c.name, s.Kind, c.kind, s)
		}
		if len(s.Notes) == 0 || s.LongSide == "" || len(s.Edges) != len(c.r) {
			t.Errorf("%s: notes %v, long side %q, %d edges", c.name, s.Notes, s.LongSide, len(s.Edges))
		}
	}
	// Faces: an anticlockwise rectangle from the south-west.
	s, _ := land.Describe(rect(sw, 150, 60))
	var faces []string
	for _, e := range s.Edges {
		faces = append(faces, e.Faces)
	}
	if !slices.Equal(faces, []string{"S", "E", "N", "W"}) {
		t.Errorf("faces %v, want S E N W", faces)
	}
	if math.Abs(s.Box.LengthM-150) > 0.1 || math.Abs(s.Box.WidthM-60) > 0.1 || s.LongSide != "east to west" {
		t.Errorf("box %+v, long side %q", s.Box, s.LongSide)
	}
	// Clockwise, the same faces.
	cw := rect(sw, 150, 60)
	cw = geo.Ring{cw[3], cw[2], cw[1], cw[0]}
	s, _ = land.Describe(cw)
	faces = faces[:0]
	for _, e := range s.Edges {
		faces = append(faces, e.Faces)
	}
	if !slices.Equal(faces, []string{"N", "E", "S", "W"}) {
		t.Errorf("clockwise faces %v, want N E S W", faces)
	}
	// Narrow and small plots get notes.
	s, _ = land.Describe(rect(sw, 60, 12))
	if len(s.Notes) < 3 {
		t.Errorf("a narrow small plot: %v", s.Notes)
	}
	for _, bad := range []geo.Ring{nil, {sw, geo.Offset(sw, 90, 10)}, {sw, geo.Offset(sw, 90, 10), geo.Offset(sw, 0, 10), geo.Offset(geo.Offset(sw, 0, 10), 90, 10)}} {
		if _, err := land.Describe(bad); err == nil {
			t.Errorf("Describe(%v) accepted", bad)
		}
	}
}

func TestAnalyseKothapeta(t *testing.T) {
	t.Parallel()
	s, err := land.Analyse(osmtest.KothapetaPlot, land.Inputs{Features: osmtest.Kothapeta(), FeaturesFrom: "OpenStreetMap", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if s.Shape.Kind != land.Square || math.Abs(s.Shape.AreaM2-7596) > 5 {
		t.Errorf("shape %s %.0f m²", s.Shape.Kind, s.Shape.AreaM2)
	}
	want := map[osm.Kind]struct {
		dist float64
		dir  string
	}{osm.Drain: {98, "E"}, osm.Road: {208, "NW"}, osm.Canal: {594, "W"}}
	for k, w := range want {
		n, ok := s.Find(k)
		if !ok || math.Abs(n.DistanceM-w.dist) > 2 || n.Direction != w.dir {
			t.Errorf("nearest %s: %+v, want %.0f m %s", k, n, w.dist, w.dir)
		}
	}
	for i := 1; i < len(s.Nearby); i++ {
		if s.Nearby[i].DistanceM < s.Nearby[i-1].DistanceM {
			t.Fatal("nearby not nearest first")
		}
	}
	if got := ids(s.Notes); !slices.Equal(got, []string{land.NoteRoadNear, land.NoteDrain, land.NoteVillage}) {
		t.Errorf("notes %v", got)
	}
	if s.Terrain != nil || len(s.Sources) != 1 || len(s.Missing) != 0 || len(s.Features) == 0 || s.Counts[osm.Road] == 0 {
		t.Errorf("terrain %v, sources %v, missing %v, %d features, counts %v", s.Terrain, s.Sources, s.Missing, len(s.Features), s.Counts)
	}
	if s.RadiusM < 400 || s.RadiusM > land.MaxRadiusM {
		t.Errorf("radius %.0f", s.RadiusM)
	}
	// The canal is beyond the radius but the nearest water of its kind, so
	// it is drawn: a callout points at it.
	canal, _ := s.Find(osm.Canal)
	if !slices.ContainsFunc(s.Features, func(f osm.Feature) bool { return f.ID == canal.ID }) {
		t.Error("the nearest canal is not among the features to draw")
	}
	// The same inputs give the same survey.
	again, _ := land.Analyse(osmtest.KothapetaPlot, land.Inputs{Features: osmtest.Kothapeta(), FeaturesFrom: "OpenStreetMap", Now: now})
	if !reflect.DeepEqual(s, again) {
		t.Error("not deterministic")
	}
}

func TestAnalyseRules(t *testing.T) {
	t.Parallel()
	plot := rect(sw, 120, 80)
	se, ne := plot[1], plot[2]
	fs := []osm.Feature{
		// A trunk road along the south side, 5 m off.
		{ID: "way/1", Kind: osm.Road, Class: "trunk", Ref: "NH44", Points: []geo.Point{geo.Offset(geo.Offset(sw, 180, 5), 270, 50), geo.Offset(geo.Offset(se, 180, 5), 90, 50)}},
		// A canal through the plot, north-south.
		{ID: "way/2", Kind: osm.Canal, Points: []geo.Point{geo.Offset(geo.Offset(sw, 90, 60), 180, 100), geo.Offset(geo.Offset(sw, 90, 60), 0, 200)}},
		// A power line 10 m east.
		{ID: "way/3", Kind: osm.Power, Class: "line", Points: []geo.Point{geo.Offset(geo.Offset(se, 90, 10), 180, 100), geo.Offset(geo.Offset(ne, 90, 10), 0, 100)}},
		// Four houses north, a building inside, a village 1 km west.
		{ID: "way/4", Kind: osm.Building, Closed: true, Points: geo.Rect(geo.Offset(ne, 0, 20), 0, 8, 8)},
		{ID: "way/5", Kind: osm.Building, Closed: true, Points: geo.Rect(geo.Offset(geo.Offset(ne, 0, 20), 270, 20), 0, 8, 8)},
		{ID: "way/6", Kind: osm.Building, Closed: true, Points: geo.Rect(geo.Offset(geo.Offset(ne, 0, 20), 270, 40), 0, 8, 8)},
		{ID: "way/7", Kind: osm.Building, Closed: true, Points: geo.Rect(geo.Offset(geo.Offset(ne, 0, 20), 270, 60), 0, 8, 8)},
		{ID: "way/8", Kind: osm.Building, Closed: true, Points: geo.Rect(geo.Offset(geo.Offset(sw, 0, 40), 90, 20), 0, 6, 6)},
		{ID: "node/9", Kind: osm.Settlement, Class: "village", Name: "Kandukur", Points: []geo.Point{geo.Offset(sw, 270, 1000)}},
	}
	pts := land.SamplePoints(plot)
	heights := make([]float64, len(pts))
	for i, p := range pts { // falls 6 m towards the east
		heights[i] = 600 - geo.Distance(sw, geo.Point{Lat: sw.Lat, Lng: p.Lng})/20
	}
	s, err := land.Analyse(plot, land.Inputs{Features: fs, FeaturesFrom: "test", Heights: heights, HeightsFrom: "test DEM", ResolutionM: 90, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	got := ids(s.Notes)
	for _, id := range []string{land.NoteSlope, land.NoteRoadFrontage, land.NoteBusyRoad, land.NoteWaterCrosses, land.NotePower, land.NoteBuildingIn, land.NoteBuildings, land.NoteVillage} {
		if !slices.Contains(got, id) {
			t.Errorf("no %s note in %v", id, got)
		}
	}
	tr := s.Terrain
	if tr == nil || tr.Flat || tr.Downhill != "E" || math.Abs(tr.FallM-6) > 0.1 || tr.SlopePct < 4 || tr.SlopePct > 6 {
		t.Errorf("terrain %+v", tr)
	}
	road, _ := s.Find(osm.Road)
	if !road.Touches || road.Direction != "S" || road.Edge != 0 || road.Label() != "trunk road NH44" {
		t.Errorf("road %+v label %q", road, road.Label())
	}
	canal, _ := s.Find(osm.Canal)
	if !canal.Crosses || canal.DistanceM != 0 {
		t.Errorf("canal %+v", canal)
	}

	// Flat land, and lookups that failed.
	flat := make([]float64, len(pts))
	s, err = land.Analyse(plot, land.Inputs{FeaturesErr: errors.New("busy"), Heights: flat, HeightsFrom: "dem", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(s.Notes); !slices.Equal(got, []string{land.NoteFlat, land.NoteNoMapData}) || len(s.Missing) != 1 {
		t.Errorf("notes %v, missing %v", got, s.Missing)
	}
	s, _ = land.Analyse(plot, land.Inputs{HeightsErr: errors.New("down"), FeaturesFrom: "map", Now: now})
	if s.Terrain != nil || len(s.Missing) != 1 || !slices.Contains(ids(s.Notes), land.NoteNoRoad) {
		t.Errorf("no heights, empty map: %v %v", s.Missing, ids(s.Notes))
	}
	if _, err := land.Analyse(plot, land.Inputs{Heights: []float64{1}}); err == nil {
		t.Error("wrong number of heights accepted")
	}
}

func TestSuggestKothapeta(t *testing.T) {
	t.Parallel()
	plot := osmtest.KothapetaPlot
	s, _ := land.Analyse(plot, land.Inputs{Features: osmtest.Kothapeta(), FeaturesFrom: "OpenStreetMap", Now: now})
	d, rep, err := land.Suggest(plot, &s, land.Want{Path: true, Shed: true, Pond: true, Border: true})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []land.ElementKind
	for _, e := range d.Elements {
		kinds = append(kinds, e.Kind)
		if e.Kind != land.Gate && e.Kind != land.Border && !geo.RingInside(e.Ring, plot) {
			t.Errorf("%s is not inside the plot", e.Kind)
		}
		if e.Why == "" {
			t.Errorf("%s has no reason", e.Kind)
		}
	}
	if !slices.Equal(kinds, land.ElementKinds()) {
		t.Errorf("elements %v", kinds)
	}
	if rep.Status != land.DesignSound {
		t.Errorf("status %s: %+v", rep.Status, rep.Checks)
	}
	pond := d.Elements[3]
	if pond.LengthM != 15 || rep.Elements[3].VolumeM3 != 675 {
		t.Errorf("pond %+v, %+v", pond, rep.Elements[3])
	}
	// The pond sits on the drain's (east) side.
	if geo.Centroid(pond.Ring).Lng < geo.Centroid(plot).Lng {
		t.Error("the pond is not on the drain's side")
	}
	// The gate is on the north side, nearest the road to the north-west.
	if gate := d.Elements[0].At; gate.Lat < plot[0].Lat-1e-6 {
		t.Errorf("gate %v not on the north side", gate)
	}
	border := d.Elements[4]
	if border.Trees < 70 || border.Trees > 90 || math.Abs(rep.Elements[4].AreaM2-1011) > 5 {
		t.Errorf("border %d trees, %.0f m²", border.Trees, rep.Elements[4].AreaM2)
	}
	if rep.FieldsM2 < 6000 || rep.FieldsM2 > 6200 {
		t.Errorf("fields %.0f m²", rep.FieldsM2)
	}
	d2, _, _ := land.Suggest(plot, &s, land.Want{Path: true, Shed: true, Pond: true, Border: true})
	if !reflect.DeepEqual(d, d2) {
		t.Error("not deterministic")
	}
}

func TestSuggestUsesSlopeAndRoad(t *testing.T) {
	t.Parallel()
	plot := rect(sw, 160, 100) // 1.6 ha
	se := plot[1]
	pts := land.SamplePoints(plot)
	hs := make([]float64, len(pts))
	for i, p := range pts { // lowest at the north-east
		hs[i] = 500 - (p.Lat-sw.Lat)*1e4 - (p.Lng-sw.Lng)*1e4
	}
	fs := []osm.Feature{{ID: "way/1", Kind: osm.Road, Class: "unclassified", Points: []geo.Point{geo.Offset(geo.Offset(se, 90, 4), 180, 50), geo.Offset(geo.Offset(se, 90, 4), 0, 200)}}}
	s, err := land.Analyse(plot, land.Inputs{Features: fs, FeaturesFrom: "t", Heights: hs, HeightsFrom: "dem", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	d, rep, err := land.Suggest(plot, &s, land.Want{Pond: true, Shed: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != land.DesignSound {
		t.Errorf("status %s %+v", rep.Status, rep.Checks)
	}
	var gate *geo.Point
	var pond geo.Ring
	for _, e := range d.Elements {
		switch e.Kind {
		case land.Gate:
			gate = e.At
		case land.Pond:
			pond = e.Ring
			if e.LengthM != 20 {
				t.Errorf("pond side %.0f for 1.6 ha, want 20", e.LengthM)
			}
		}
	}
	if gate == nil || math.Abs(geo.Distance(*gate, geo.Point{Lat: gate.Lat, Lng: se.Lng})) > 1 {
		t.Errorf("gate %v not on the east side by the road", gate)
	}
	if c := geo.Centroid(pond); geo.Distance(c, plot[2]) > 25 {
		t.Errorf("pond centre %v is %.0f m from the low (north-east) corner", c, geo.Distance(c, plot[2]))
	}
}

func TestSuggestRefuses(t *testing.T) {
	t.Parallel()
	if _, _, err := land.Suggest(rect(sw, 30, 20), nil, land.Want{Pond: true}); !errors.Is(err, land.ErrDesign) {
		t.Errorf("a pond on 600 m²: %v", err)
	}
	if _, _, err := land.Suggest(rect(sw, 100, 100), nil, land.Want{}); !errors.Is(err, land.ErrDesign) {
		t.Errorf("nothing wanted: %v", err)
	}
	if _, _, err := land.Suggest(rect(sw, 100, 100), nil, land.Want{Border: true, BorderWidthM: 60}); !errors.Is(err, land.ErrDesign) {
		t.Errorf("a 60 m border on 100 m: %v", err)
	}
	if _, _, err := land.Suggest(rect(sw, 100, 100), nil, land.Want{Shed: true, ShedLengthM: -1}); !errors.Is(err, land.ErrDesign) {
		t.Errorf("negative shed: %v", err)
	}
	if land.PondSide(4000) != 10 || land.PondSide(10000) != 15 || land.PondSide(20000) != 20 || land.PondSide(50000) != 30 || land.PondSide(900) != 0 {
		t.Error("PondSide")
	}
	// No survey: the gate on the longest side, the pond away from it.
	d, rep, err := land.Suggest(rect(sw, 150, 80), nil, land.Want{Gate: true, Pond: true})
	if err != nil || rep.Status != land.DesignSound || len(d.Elements) != 2 {
		t.Errorf("%v %s %+v", err, rep.Status, rep.Checks)
	}
	// A narrow plot gets a shade warning for a border.
	_, rep, err = land.Suggest(rect(sw, 200, 18), nil, land.Want{Border: true, BorderWidthM: 2})
	if err != nil || !slices.Contains(ids(rep.Checks), land.CheckBorderShade) {
		t.Errorf("narrow border: %v %v", err, ids(rep.Checks))
	}
}

func TestCheckDesignFindsMistakes(t *testing.T) {
	t.Parallel()
	plot := rect(sw, 100, 100)
	c := geo.Centroid(plot)
	gateAt := geo.Offset(plot[0], 90, 50)                                                                                       // south side
	road := osm.Feature{ID: "way/1", Kind: osm.Road, Points: []geo.Point{geo.Offset(plot[3], 0, 3), geo.Offset(plot[2], 0, 3)}} // along the north side
	s, _ := land.Analyse(plot, land.Inputs{Features: []osm.Feature{road}, FeaturesFrom: "t", Now: now})
	d := land.Design{Elements: []land.Element{
		{Kind: land.Gate, At: &gateAt},
		{Kind: land.Shed, Ring: geo.Rect(geo.Offset(c, 0, 40), 90, 6, 4)},
		{Kind: land.Pond, Ring: geo.Rect(geo.Offset(plot[0], 45, 2.5*math.Sqrt2+0.5), 0, 5, 5), DepthM: 2},
		{Kind: land.Pond, Ring: geo.Rect(c, 0, 40, 40), DepthM: 3},
		{Kind: land.Pond, Ring: geo.Rect(geo.Offset(plot[0], 225, 10), 0, 8, 8)},
		{Kind: land.Shed, Ring: geo.Rect(c, 0, 6, 4)},
		{Kind: "windmill"},
	}}
	zones := []land.Zone{{Name: "paddy", Ring: plot}}
	rep := land.CheckDesign(plot, &s, d, zones)
	got := ids(rep.Checks)
	for _, id := range []string{land.CheckGateRoad, land.CheckShedFar, land.CheckPondSetback, land.CheckPondShare, land.CheckOutside, land.CheckOverlap, land.CheckBadElement, land.CheckPondZone, land.CheckPondVolume} {
		if !slices.Contains(got, id) {
			t.Errorf("no %s in %v", id, got)
		}
	}
	if rep.Status != land.DesignInvalid {
		t.Errorf("status %s", rep.Status)
	}
	big := land.CheckDesign(plot, nil, land.Design{Elements: []land.Element{{Kind: land.Pond, Ring: geo.Rect(c, 0, 60, 60)}}}, nil)
	if !slices.Contains(ids(big.Checks), land.CheckFields) || math.Abs(big.FieldsM2-6400) > 5 || big.Status != land.DesignReview {
		t.Errorf("a 60 m pond: %s %.0f %v", big.Status, big.FieldsM2, ids(big.Checks))
	}
}
