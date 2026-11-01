package planting_test

import (
	"errors"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/land"
	"github.com/bsamanvith/agentkit/apps/farm/planting"
)

func library(t *testing.T) *agro.Library {
	t.Helper()
	lib, err := agro.Default()
	if err != nil {
		t.Fatal(err)
	}
	return lib
}

// rect is a plot south-west corner (lat, lng), dlat by dlng degrees: at
// 17° N, 0.0009° of latitude is about 100 m and 0.00094° of longitude
// about 100 m.
func rect(lat, lng, dlat, dlng float64) geo.Ring {
	return geo.Ring{{Lat: lat, Lng: lng}, {Lat: lat, Lng: lng + dlng}, {Lat: lat + dlat, Lng: lng + dlng}, {Lat: lat + dlat, Lng: lng}}
}

// field is about 200 m east-west by 100 m north-south.
var field = rect(17.07, 78.20, 0.0009, 0.00188)

func plot(r geo.Ring) []planting.Plot { return []planting.Plot{{ID: "a", Name: "East field", Ring: r}} }

func distribute(t *testing.T, plots []planting.Plot, d planting.Distribution) planting.Result {
	t.Helper()
	r, err := planting.Distribute(library(t), plots, d)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func block(id string, share float64, crops ...string) planting.PlantGroup {
	return planting.PlantGroup{ID: id, Name: strings.ToUpper(id[:1]) + id[1:], Crops: crops, Share: share}
}

func measure(t *testing.T, r planting.Result, id string) planting.Measure {
	t.Helper()
	i := slices.IndexFunc(r.Plants, func(m planting.Measure) bool { return m.Plant == id })
	if i < 0 {
		t.Fatalf("no measure for %s in %+v", id, r.Plants)
	}
	return r.Plants[i]
}

func effects(r planting.Result, id string) []planting.Effect {
	var out []planting.Effect
	for _, e := range r.Effects {
		if e.ID == id {
			out = append(out, e)
		}
	}
	return out
}

func near(got, want, tol float64) bool { return math.Abs(got-want) <= tol }

func TestRefusesBadDistributions(t *testing.T) {
	t.Parallel()
	lib := library(t)
	veg := block("veg", 1, "tomato")
	border := planting.PlantGroup{ID: "edge", Crops: []string{"mango"}, Place: planting.Border}
	for name, d := range map[string]planting.Distribution{
		"nothing":           {},
		"no block":          {Plants: []planting.PlantGroup{border}},
		"two borders":       {Plants: []planting.PlantGroup{veg, border, {ID: "e2", Crops: []string{"neem_x"}, Place: planting.Border}}},
		"unknown crop":      {Plants: []planting.PlantGroup{block("veg", 1, "tomatoes")}},
		"no crops":          {Plants: []planting.PlantGroup{{ID: "veg"}}},
		"repeated crop":     {Plants: []planting.PlantGroup{block("veg", 1, "tomato", "tomato")}},
		"bad id":            {Plants: []planting.PlantGroup{block("Veg Plot", 1, "tomato")}},
		"repeated id":       {Plants: []planting.PlantGroup{veg, veg}},
		"negative share":    {Plants: []planting.PlantGroup{block("veg", -1, "tomato")}},
		"share on a border": {Plants: []planting.PlantGroup{veg, {ID: "edge", Crops: []string{"mango"}, Place: planting.Border, Share: 0.2}}},
		"alley too dense":   {Plants: []planting.PlantGroup{veg, {ID: "rows", Crops: []string{"moringa"}, Place: planting.Alley, WidthM: 3, EveryM: 4}}},
		"unknown place":     {Plants: []planting.PlantGroup{{ID: "veg", Crops: []string{"tomato"}, Place: "middle"}}},
		"unknown arrange":   {Arrange: "spiral", Plants: []planting.PlantGroup{veg}},
		"unknown rows":      {Rows: "diagonal", Plants: []planting.PlantGroup{veg}},
		"strip too narrow":  {Arrange: planting.Alternate, StripM: 1, Plants: []planting.PlantGroup{veg}},
	} {
		if _, err := planting.Distribute(lib, plot(field), d); !errors.Is(err, planting.ErrInvalid) {
			t.Errorf("%s: err %v, want ErrInvalid", name, err)
		}
	}
	if _, err := planting.Distribute(lib, nil, planting.Distribution{Plants: []planting.PlantGroup{veg}}); !errors.Is(err, planting.ErrInvalid) {
		t.Errorf("no plots: err %v, want ErrInvalid", err)
	}
}

func TestStripsByShare(t *testing.T) {
	t.Parallel()
	r := distribute(t, plot(field), planting.Distribution{Plants: []planting.PlantGroup{block("veg", 1, "tomato"), block("pulse", 3, "green_gram")}})
	whole := geo.Area(field)
	if !near(r.AreaM2, whole, 1) || r.Status != planting.Sound {
		t.Fatalf("area %.0f (want %.0f), status %s, effects %+v", r.AreaM2, whole, r.Status, r.Effects)
	}
	for id, share := range map[string]float64{"veg": 0.25, "pulse": 0.75} {
		m := measure(t, r, id)
		if !near(m.AreaM2, share*whole, 0.01*whole) || m.Want != share || m.Pieces != 1 || m.Plots != 1 {
			t.Errorf("%s: %+v, want %.0f m² (%.2f)", id, m, share*whole, share)
		}
	}
	// Rows along the long side, east-west; tomato at 60 cm: the 25 m strip
	// has 41 rows and about 25 × 200 / (0.6 × 0.45) plants.
	if b := r.Plots[0].RowBearing; !near(b, 90, 1) {
		t.Errorf("rows bear %v°, want 90", b)
	}
	veg := r.Plots[0].Regions[0]
	if !near(veg.WidthM, 24.9, 0.5) || !near(veg.LengthM, 200, 2) || veg.Rows != int(veg.WidthM/0.6) {
		t.Errorf("veg strip %.1f × %.1f m, %d rows", veg.LengthM, veg.WidthM, veg.Rows)
	}
	if got, want := float64(measure(t, r, "veg").Counts[0].Plants), veg.AreaM2/(0.6*0.45); !near(got, want, 1) {
		t.Errorf("tomato plants %v, want %v", got, want)
	}
}

func TestNorthSouthRows(t *testing.T) {
	t.Parallel()
	r := distribute(t, plot(field), planting.Distribution{Rows: planting.NorthSouth, Plants: []planting.PlantGroup{block("veg", 1, "tomato"), block("pulse", 1, "green_gram")}})
	if r.Plots[0].RowBearing != 0 {
		t.Fatalf("rows bear %v, want 0", r.Plots[0].RowBearing)
	}
	for _, g := range r.Plots[0].Regions {
		if !near(g.LengthM, 99.6, 1) || !near(g.WidthM, 100, 2) {
			t.Errorf("%s: %.1f × %.1f m, want north-south strips about 100 × 100", g.Plant, g.LengthM, g.WidthM)
		}
	}
}

func TestBorderAndItsTrees(t *testing.T) {
	t.Parallel()
	r := distribute(t, plot(field), planting.Distribution{Plants: []planting.PlantGroup{
		{ID: "edge", Name: "Border trees", Crops: []string{"coconut"}, Place: planting.Border, WidthM: 4},
		block("veg", 1, "tomato"),
	}})
	inner, err := geo.Inset(field, 4)
	if err != nil {
		t.Fatal(err)
	}
	b := measure(t, r, "edge")
	if want := geo.Area(field) - geo.Area(inner); !near(b.AreaM2, want, 5) {
		t.Errorf("border %.0f m², want %.0f", b.AreaM2, want)
	}
	// Coconut every 7.5 m round the band's middle line.
	mid, _ := geo.Inset(field, 2)
	if want := geo.Perimeter(mid) / 7.5; !near(float64(b.Counts[0].Plants), want, 1) {
		t.Errorf("%d coconuts, want about %.0f", b.Counts[0].Plants, want)
	}
	if v := measure(t, r, "veg"); !near(v.AreaM2, geo.Area(inner), 5) {
		t.Errorf("veg %.0f m², want the inside, %.0f", v.AreaM2, geo.Area(inner))
	}
	if len(effects(r, "wind.shelter")) != 1 {
		t.Errorf("no windbreak note for 15 m coconuts: %+v", r.Effects)
	}
	// A border wider than the plot is a warning, not an error.
	small := rect(17.07, 78.20, 0.0001, 0.0001)
	r = distribute(t, plot(small), planting.Distribution{Plants: []planting.PlantGroup{{ID: "edge", Crops: []string{"mango"}, Place: planting.Border, WidthM: 8}, block("veg", 1, "tomato")}})
	if len(effects(r, "border.no_fit")) != 1 || measure(t, r, "edge").AreaM2 != 0 {
		t.Errorf("an 8 m border on an 11 m plot: %+v", r.Effects)
	}
}

func TestAlternateStrips(t *testing.T) {
	t.Parallel()
	r := distribute(t, plot(field), planting.Distribution{Arrange: planting.Alternate, StripM: 10, Plants: []planting.PlantGroup{block("veg", 1, "tomato"), block("pulse", 1, "green_gram")}})
	regions := r.Plots[0].Regions
	if len(regions) != 10 {
		t.Fatalf("%d strips across 100 m of 10 m, want 10", len(regions))
	}
	for i, g := range regions {
		want := []string{"veg", "pulse"}[i%2]
		w := 10.0
		if i == len(regions)-1 {
			w = 9.6 // the plot is 99.6 m across
		}
		if g.Plant != want || !near(g.WidthM, w, 0.2) {
			t.Errorf("strip %d: %s %.1f m wide, want %s 10 m", i, g.Plant, g.WidthM, want)
		}
	}
	if len(effects(r, "alternate.pests")) != 1 {
		t.Errorf("no note on alternating strips and pests")
	}
	// Maize in 1 m strips (a quarter of two 2 m strips): one row each.
	r = distribute(t, plot(field), planting.Distribution{Arrange: planting.Alternate, StripM: 2, Plants: []planting.PlantGroup{block("maize", 1, "maize"), block("gram", 3, "green_gram")}})
	if n := effects(r, "piece.narrow"); len(n) == 0 || n[0].Plant != "maize" {
		t.Errorf("2 m maize strips: %+v", n)
	}
}

func TestAlleys(t *testing.T) {
	t.Parallel()
	r := distribute(t, plot(field), planting.Distribution{Plants: []planting.PlantGroup{
		{ID: "rows", Name: "Moringa rows", Crops: []string{"moringa"}, Place: planting.Alley, WidthM: 2, EveryM: 20},
		block("veg", 1, "tomato"), block("pulse", 1, "green_gram"),
	}})
	var rows []planting.Region
	for _, g := range r.Plots[0].Regions {
		if g.Plant == "rows" {
			rows = append(rows, g)
		}
	}
	// Rows at 10, 30, 50, 70, 90 m across the 100 m.
	if len(rows) != 5 {
		t.Fatalf("%d tree rows, want 5: %+v", len(rows), r.Plots[0].Regions)
	}
	for _, g := range rows {
		// One line of moringa down the middle, every 2.5 m along 200 m.
		if !near(g.WidthM, 2, 0.1) || !near(float64(len(g.Trees)), 200/2.5, 1) {
			t.Errorf("tree row %.1f m wide with %d trees", g.WidthM, len(g.Trees))
		}
	}
	// The alleys go half to each block, by share.
	v, p := measure(t, r, "veg"), measure(t, r, "pulse")
	if !near(v.AreaM2, p.AreaM2, 0.25*v.AreaM2) {
		t.Errorf("veg %.0f m², pulses %.0f m²: want about even", v.AreaM2, p.AreaM2)
	}
}

func TestByPlot(t *testing.T) {
	t.Parallel()
	plots := []planting.Plot{
		{ID: "big", Name: "Big", Ring: rect(17.07, 78.20, 0.0009, 0.00188)},
		{ID: "mid", Name: "Mid", Ring: rect(17.08, 78.20, 0.0009, 0.00094)},
		{ID: "small", Name: "Small", Ring: rect(17.09, 78.20, 0.0009, 0.00094)},
	}
	r := distribute(t, plots, planting.Distribution{Arrange: planting.ByPlot, Plants: []planting.PlantGroup{block("veg", 1, "tomato"), block("pulse", 1, "green_gram")}})
	got := map[string]string{}
	for _, p := range r.Plots {
		if len(p.Regions) != 1 {
			t.Fatalf("plot %s: %d regions, want 1", p.Plot, len(p.Regions))
		}
		got[p.Plot] = p.Regions[0].Plant
	}
	if diff := cmp.Diff(map[string]string{"big": "veg", "mid": "pulse", "small": "pulse"}, got); diff != "" {
		t.Errorf("plots to groups (-want +got):\n%s", diff)
	}
	r = distribute(t, plots[:1], planting.Distribution{Arrange: planting.ByPlot, Plants: []planting.PlantGroup{block("veg", 1, "tomato"), block("pulse", 1, "green_gram")}})
	if n := effects(r, "by_plot.no_land"); len(n) != 1 || n[0].Plant != "pulse" || r.Status != planting.NeedsReview {
		t.Errorf("one plot for two blocks: %+v", r.Effects)
	}
}

// Tall sugarcane on the south of short groundnut shades it at midday in
// December; north-south strips side by side shade far less.
func TestShade(t *testing.T) {
	t.Parallel()
	ew := distribute(t, plot(field), planting.Distribution{Plants: []planting.PlantGroup{block("cane", 1, "sugarcane"), block("nut", 1, "groundnut")}})
	// The first strip is the southern one: check, then the shade.
	if c, n := geo.Centroid(ew.Plots[0].Regions[0].Ring), geo.Centroid(ew.Plots[0].Regions[1].Ring); c.Lat > n.Lat {
		t.Fatalf("the cane strip is north of the groundnut; the test expects it south")
	}
	s := effects(ew, "shade")
	if len(s) != 1 || s[0].Plant != "nut" || s[0].Other != "cane" {
		t.Fatalf("shade effects %+v", s)
	}
	// Cane 3.5 m over groundnut 0.4 m: at noon a 2.6 m shadow along 200 m;
	// in the morning and afternoon longer. Over half the day: 2 to 6 m.
	shaded := measure(t, ew, "nut").ShadedM2
	if shaded < 2*200 || shaded > 6*200 {
		t.Errorf("groundnut shaded %.0f m², want 400 to 1200", shaded)
	}
	if !strings.Contains(s[0].Message, "south") {
		t.Errorf("the shade is on the groundnut's south side: %q", s[0].Message)
	}
	ns := distribute(t, plot(field), planting.Distribution{Rows: planting.NorthSouth, Plants: []planting.PlantGroup{block("cane", 1, "sugarcane"), block("nut", 1, "groundnut")}})
	if got := measure(t, ns, "nut").ShadedM2; got >= shaded/2 {
		t.Errorf("north-south strips: groundnut shaded %.0f m², want well under the east-west %.0f", got, shaded)
	}
	// South of the equator the sun is to the north: groundnut south of the
	// cane is shaded instead.
	south := rect(-17.0709, 78.20, 0.0009, 0.00188)
	sr := distribute(t, plot(south), planting.Distribution{Plants: []planting.PlantGroup{block("nut", 1, "groundnut"), block("cane", 1, "sugarcane")}})
	if got := measure(t, sr, "nut").ShadedM2; got < 2*200 {
		t.Errorf("south of the equator, groundnut south of the cane shaded %.0f m², want over 400", got)
	}
	// A crop no shorter is never shaded.
	if got := measure(t, ew, "cane").ShadedM2; got != 0 {
		t.Errorf("cane shaded %.0f m²", got)
	}
}

func TestWater(t *testing.T) {
	t.Parallel()
	// A pond in the plot's south-west corner; strips run east-west, the
	// first the southern one.
	pond := geo.Rect(geo.Offset(geo.Offset(field[0], 0, 10), 90, 10), 90, 10, 10)
	design := &land.Design{Elements: []land.Element{{Kind: land.Pond, Ring: pond, LengthM: 10, WidthM: 10, DepthM: 3}}}
	plots := []planting.Plot{{ID: "a", Name: "East field", Ring: field, Design: design}}
	far := distribute(t, plots, planting.Distribution{Plants: []planting.PlantGroup{block("pulse", 1, "green_gram"), block("banana", 1, "banana")}})
	if w := effects(far, "water.far"); len(w) != 1 || w[0].Plant != "banana" || !strings.Contains(w[0].Message, "farm pond") {
		t.Errorf("banana far from the pond: %+v", far.Effects)
	}
	nearby := distribute(t, plots, planting.Distribution{Plants: []planting.PlantGroup{block("banana", 1, "banana"), block("pulse", 1, "green_gram")}})
	if len(effects(nearby, "water.far")) != 0 || len(effects(nearby, "water.near")) != 1 {
		t.Errorf("banana by the pond: %+v", nearby.Effects)
	}
	// The pond takes its 100 m² from the strip it is in.
	if b := measure(t, nearby, "banana"); !near(b.AreaM2, geo.Area(field)/2-100, 15) {
		t.Errorf("banana %.0f m², want half the plot less the pond", b.AreaM2)
	}
	if n := effects(nearby, "layout.takes"); len(n) != 1 || n[0].Plant != "banana" || !near(n[0].AreaM2, 100, 15) {
		t.Errorf("what the pond takes: %+v", n)
	}
	mixed := distribute(t, plot(field), planting.Distribution{Plants: []planting.PlantGroup{block("mix", 1, "rice", "chickpea")}})
	if len(effects(mixed, "water.mixed")) != 1 {
		t.Errorf("rice with chickpea in one watering: %+v", mixed.Effects)
	}
}

func TestNeighbours(t *testing.T) {
	t.Parallel()
	r := distribute(t, plot(field), planting.Distribution{Plants: []planting.PlantGroup{
		block("veg", 1, "tomato", "brinjal"), block("chilli", 1, "chilli"), block("pulse", 1, "green_gram", "pigeon_pea"),
	}})
	if n := effects(r, "mix.family"); len(n) != 2 { // tomato/brinjal, both pulses
		t.Errorf("one-family mixes: %+v", n)
	}
	if n := effects(r, "next.family"); len(n) != 1 || n[0].Plant != "veg" || n[0].Other != "chilli" {
		t.Errorf("Solanaceae side by side: %+v", n)
	}
	if n := effects(r, "next.good"); len(n) != 0 {
		t.Errorf("chilli and pulses are not a known pair: %+v", n)
	}
}

func TestTreesFit(t *testing.T) {
	t.Parallel()
	// Mango at 10 × 10 m on a thin triangle: the sharp corners waste room.
	tri := geo.Ring{{Lat: 17.07, Lng: 78.20}, {Lat: 17.07, Lng: 78.203}, {Lat: 17.0703, Lng: 78.2015}}
	r := distribute(t, plot(tri), planting.Distribution{Plants: []planting.PlantGroup{block("mango", 1, "mango")}})
	if len(effects(r, "trees.fit")) != 1 {
		t.Errorf("mango on a thin triangle: %+v", r.Effects)
	}
	// On the 200 × 99.6 m field, 9 rows of 20 trees, each 5 m in from the
	// edges: the tenth row would be 4.6 m from the boundary.
	whole := distribute(t, plot(field), planting.Distribution{Plants: []planting.PlantGroup{block("mango", 1, "mango")}})
	if got := measure(t, whole, "mango").Counts[0].Plants; got != 180 {
		t.Errorf("%d mango trees, want 180", got)
	}
	if len(effects(whole, "trees.fit")) != 0 {
		t.Errorf("a rectangle wastes little: %+v", whole.Effects)
	}
}

func TestDistributeIsPure(t *testing.T) {
	t.Parallel()
	plots := plot(append(geo.Ring{}, field...))
	plots[0].Ring = append(plots[0].Ring, plots[0].Ring[0]) // closed
	before := slices.Clone(plots[0].Ring)
	d := planting.Distribution{Plants: []planting.PlantGroup{block("veg", 1, "tomato"), {ID: "edge", Crops: []string{"mango"}, Place: planting.Border}}}
	a := distribute(t, plots, d)
	b := distribute(t, plots, d)
	if diff := cmp.Diff(a, b); diff != "" {
		t.Errorf("two runs differ:\n%s", diff)
	}
	if diff := cmp.Diff(before, plots[0].Ring); diff != "" {
		t.Errorf("the caller's ring changed:\n%s", diff)
	}
	if a.Distribution.Arrange != planting.Strips || a.Distribution.Plants[1].WidthM != planting.DefaultBorderM || a.Distribution.Plants[1].Name != "edge" {
		t.Errorf("defaults not filled in: %+v", a.Distribution)
	}
}
