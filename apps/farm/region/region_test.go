package region_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/agro/agrotest"
	"github.com/bsamanvith/agentkit/apps/farm/inputs"
	"github.com/bsamanvith/agentkit/apps/farm/prices"
	"github.com/bsamanvith/agentkit/apps/farm/region"
)

var (
	kadapa    = region.Place{Country: "IN", State: "Andhra Pradesh", District: "Y.S.R. Kadapa"}
	guntur    = region.Place{Country: "IN", State: "Andhra Pradesh", District: "Guntur"}
	bengal    = region.Place{Country: "IN", State: "West Bengal", District: "Nadia"}
	anantapur = region.Place{Country: "IN", State: "andhra pradesh", District: "Anantapuramu"}
	sathyaSai = region.Place{Country: "IN", State: "Andhra Pradesh", District: "Sri Sathya Sai District"}
)

func load(t *testing.T, files map[string]string) (*region.Set, error) {
	t.Helper()
	fsys := fstest.MapFS{}
	for name, body := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return region.Load(fsys)
}

func mustLoad(t *testing.T, files map[string]string) *region.Set {
	t.Helper()
	s, err := load(t, files)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func crop(t *testing.T, p *region.Pack, id string) agro.Crop {
	t.Helper()
	c, err := p.Library().Crop(id)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// With no overlay for a place, the pack is the base, and the base is the
// app's built-in data unchanged: the crop library, the inputs book's
// values and the rules the cycles were built with.
func TestBaseIsTheBuiltInData(t *testing.T) {
	s := mustLoad(t, nil)
	lib, err := agro.Default()
	if err != nil {
		t.Fatal(err)
	}
	book, err := inputs.Default(lib)
	if err != nil {
		t.Fatal(err)
	}
	for _, place := range []region.Place{bengal, {}, {Country: "KE", State: "Nakuru"}} {
		p := s.For(place)
		if p != s.Base() || p.Key() != region.Base {
			t.Fatalf("%+v: pack %s, want the base", place, p.Key())
		}
	}
	b := s.Base()
	if !reflect.DeepEqual(b.Library().Crops(), lib.Crops()) || !reflect.DeepEqual(b.Library().Soils(), lib.Soils()) {
		t.Error("the base library is not agro.Default's")
	}
	if !reflect.DeepEqual(b.Book().Values(), book.Values()) {
		t.Error("the base book is not inputs.Default's")
	}
	if !reflect.DeepEqual(b.Rules(), region.DefaultRules()) {
		t.Errorf("base rules %+v", b.Rules())
	}
	if got, want := b.Storage("groundnut"), prices.StorageFor("groundnut"); got != want {
		t.Errorf("base storage %+v, want %+v", got, want)
	}
	if got, want := b.MSP("groundnut"), prices.MSP("groundnut"); !reflect.DeepEqual(got, want) {
		t.Errorf("base MSP %+v, want %+v", got, want)
	}
	for _, path := range []string{"crops.groundnut.windows", "rules.turnaround_days", "schemes.pmfby.crops"} {
		if o := b.Origin(path); o.Layer != region.Base {
			t.Errorf("base origin of %s: %+v", path, o)
		}
	}
}

// The base seasons are agro.SeasonOf's, month by month.
func TestDefaultSeasonsAreTheIndianSeasons(t *testing.T) {
	r := region.DefaultRules()
	for m := time.January; m <= time.December; m++ {
		d := time.Date(2026, m, 10, 0, 0, 0, 0, time.UTC)
		if got, want := r.SeasonOf(d), agro.SeasonOf(d); got != want {
			t.Errorf("%s: %s, want %s", m, got, want)
		}
	}
}

// A plot in YSR Kadapa gets the district's sowing windows on top of
// Andhra Pradesh's scheme; another AP district gets the state's alone;
// and each value says which layer it came from.
func TestEmbeddedAndhraPradeshOverlays(t *testing.T) {
	s := mustLoad(t, nil)
	k := s.For(kadapa)
	if got := k.Layers(); !reflect.DeepEqual(got, []string{"base", "Andhra Pradesh", "YSR Kadapa"}) {
		t.Fatalf("Kadapa layers %v", got)
	}
	g := s.For(guntur)
	if got := g.Layers(); !reflect.DeepEqual(got, []string{"base", "Andhra Pradesh"}) {
		t.Fatalf("Guntur layers %v", got)
	}
	gn := crop(t, k, "groundnut")
	if len(gn.Windows) != 2 || gn.Windows[0].From != "06-01" || gn.Windows[0].To != "07-31" || gn.Windows[0].Water != "rainfed" {
		t.Errorf("Kadapa groundnut windows %+v", gn.Windows)
	}
	base := crop(t, s.Base(), "groundnut")
	if len(crop(t, g, "groundnut").Windows) != 0 || len(base.Windows) != 0 {
		t.Error("windows leaked outside Kadapa")
	}
	// The district's own varieties, each with where the plan lists it;
	// the base's (another state's guide) stay elsewhere.
	if len(gn.Varieties) != 10 || gn.Varieties[1].Name != "Dharani" || gn.Varieties[1].Ref.Source != "crida-cp-kadapa" {
		t.Errorf("Kadapa groundnut varieties %+v", gn.Varieties)
	}
	if hg := crop(t, k, "horse_gram"); len(hg.Varieties) != 4 || len(crop(t, g, "horse_gram").Varieties) != 0 {
		t.Errorf("horse gram varieties: Kadapa %+v", hg.Varieties)
	}
	// Field-level: everything but the windows and varieties is the base's.
	gn.Windows, gn.Varieties = nil, nil
	if !reflect.DeepEqual(gn, base) {
		t.Error("Kadapa's groundnut differs from the base beyond its windows and varieties")
	}
	// Castor's window is the plan's Southern zone table: any water.
	if ca := crop(t, k, "castor"); len(ca.Windows) != 2 || ca.Windows[0].From != "06-15" || ca.Windows[0].To != "07-15" || ca.Windows[0].Water != "" {
		t.Errorf("Kadapa castor windows %+v", ca.Windows)
	}
	if o := k.Origin("crops.groundnut.windows"); o.Layer != "YSR Kadapa" || o.Ref == nil || o.Ref.Source != "crida-cp-kadapa" || !strings.Contains(o.File, "kadapa") {
		t.Errorf("origin of Kadapa's windows %+v", o)
	}
	if o := k.Origin("crops.groundnut.days"); o.Layer != region.Base {
		t.Errorf("origin of Kadapa's groundnut days %+v", o)
	}
	if o := k.Origin("schemes.ap-crop-insurance-kharif-2026.crops"); o.Layer != "Andhra Pradesh" {
		t.Errorf("origin of the AP scheme %+v", o)
	}
	var names []string
	for _, sc := range g.Schemes() {
		names = append(names, sc.ID)
	}
	if !reflect.DeepEqual(names, []string{"pmfby", "ap-crop-insurance-kharif-2026"}) {
		t.Errorf("Guntur schemes %v", names)
	}
	for _, sc := range k.Schemes() {
		if sc.ID == "ap-crop-insurance-kharif-2026" && (sc.Season != agro.Kharif || sc.Year != 2026) {
			t.Errorf("the AP notice is for kharif 2026 only: %+v", sc)
		}
	}
	if b := s.Base().Schemes(); len(b) != 1 || b[0].Season != "" || b[0].Year != 0 {
		t.Errorf("PMFBY's guidelines hold every season: %+v", b)
	}
	if len(s.Base().Schemes()) != 1 || s.Base().Schemes()[0].LossNoticeHours != 72 {
		t.Errorf("base schemes %+v", s.Base().Schemes())
	}
	// Anantapur by its newer spelling, the state in lower case.
	if p := s.For(anantapur); p.Name() != "Anantapur" {
		t.Errorf("Anantapuramu resolves to %s", p.Name())
	}
	// As a map service writes it.
	if p := s.For(region.Place{Country: "IN", State: "Andhra Pradesh", District: "Anantapuramu District"}); p.Name() != "Anantapur" {
		t.Errorf("Anantapuramu District resolves to %s", p.Name())
	}
	// Sri Sathya Sai, split from Anantapur in 2022, is a district of its
	// own with the undivided plan's windows.
	ss := s.For(sathyaSai)
	if ss.Name() != "Sri Sathya Sai" {
		t.Errorf("Sri Sathya Sai resolves to %s", ss.Name())
	}
	a, b := crop(t, ss, "groundnut").Windows, crop(t, s.For(anantapur), "groundnut").Windows
	if len(a) != 2 || len(a) != len(b) {
		t.Fatalf("Sri Sathya Sai groundnut windows %+v, Anantapur's %+v", a, b)
	}
	for i := range a {
		if a[i].From != b[i].From || a[i].To != b[i].To || a[i].Water != b[i].Water || !strings.Contains(a[i].Source, "Sri Sathya Sai") {
			t.Errorf("window %d: %+v, Anantapur's %+v", i, a[i], b[i])
		}
	}
}

// The windows move the recommender where the overlay holds: a rainfed
// groundnut sowing in late August is outside Kadapa's window and scores
// less there than on the base, which says nothing of windows.
func TestWindowsMoveTheScoreThereOnly(t *testing.T) {
	s := mustLoad(t, nil)
	r := agro.Request{Climate: agrotest.Hyderabad(), Today: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)}
	late := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	in := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	baseLate := agro.Simulate(crop(t, s.Base(), "groundnut"), r, late)
	kadLate := agro.Simulate(crop(t, s.For(kadapa), "groundnut"), r, late)
	if kadLate.Score >= baseLate.Score || !hasNote(kadLate.Risks, "window.outside") || hasNote(baseLate.Risks, "window.outside") {
		t.Errorf("late sowing: Kadapa %d %v, base %d", kadLate.Score, kadLate.Risks, baseLate.Score)
	}
	baseIn := agro.Simulate(crop(t, s.Base(), "groundnut"), r, in)
	kadIn := agro.Simulate(crop(t, s.For(kadapa), "groundnut"), r, in)
	if kadIn.Score != baseIn.Score || !hasNote(kadIn.Reasons, "window.local") {
		t.Errorf("sowing in the window: Kadapa %d %v, base %d", kadIn.Score, kadIn.Reasons, baseIn.Score)
	}
}

func hasNote(ns []agro.Note, id string) bool {
	for _, n := range ns {
		if n.ID == id {
			return true
		}
	}
	return false
}

const sources = `"sources": [{"id": "t", "name": "Test source", "publisher": "us", "url": "https://example.org", "read": "2026-11-15"}]`

func overlay(region, body string) string {
	return `{"region": ` + region + `, ` + sources + `, ` + body + `}`
}

// An overlay from the directory may change each kind of value, field by
// field, and only for its place.
func TestDirectoryOverlayChangesItsPlaceOnly(t *testing.T) {
	s := mustLoad(t, map[string]string{
		"wb.json": overlay(`{"country": "IN", "state": "West Bengal"}`, `
			"crops": {
				"groundnut": {"days": [95, 120], "ref": {"source": "t", "where": "p. 1"}},
				"quinoa": {"off": true, "ref": {"source": "t", "where": "p. 2"}}
			},
			"inputs": {
				"crops": {"groundnut": {"rainfed": {"n": 20, "p": 40, "k": 40, "ref": {"source": "t", "where": "p. 3"}}, "ref": {"source": "t", "where": "p. 3"}}},
				"products": {"neem_cake_local": {"name": "Neem cake (local)", "kind": "manure", "synthetic": false, "npop": "permitted", "unit": "kg", "ref": {"source": "t", "where": "p. 4"}}}
			},
			"rules": {"turnaround_days": 30, "rotation": {"same_family": -40}, "ref": {"source": "t", "where": "p. 5"}},
			"storage": {"oilseeds": {"cost_per_q_month": 25, "ref": {"source": "t", "where": "p. 6"}}},
			"msp": [{"commodity": "groundnut", "season": "Kharif 2026-27", "price": 8000, "ref": {"source": "t", "where": "p. 7"}}],
			"schemes": {"pmfby": {"crops": ["rice", "groundnut"], "ref": {"source": "t", "where": "p. 8"}}}`),
	})
	wb, base := s.For(bengal), s.Base()
	if wb.Name() != "West Bengal" {
		t.Fatalf("pack %s", wb.Name())
	}
	g := crop(t, wb, "groundnut")
	if g.Days != (agro.Range{95, 120}) || g.Name != crop(t, base, "groundnut").Name {
		t.Errorf("groundnut %v %q", g.Days, g.Name)
	}
	if !crop(t, wb, "quinoa").Off || crop(t, base, "quinoa").Off {
		t.Error("quinoa is not off in West Bengal only")
	}
	r := wb.Rules()
	if r.TurnaroundDays != 30 || r.Rotation.SameFamily != -40 || r.Rotation.LegumeAfterOther != 10 || r.MaxWaitDays != region.BaseMaxWaitDays {
		t.Errorf("rules %+v", r)
	}
	if o := wb.Origin("rules.rotation.same_family"); o.Layer != "West Bengal" || o.Ref.Where != "p. 5" {
		t.Errorf("origin %+v", o)
	}
	if o := wb.Origin("rules.rotation.cereal_after_legume"); o.Layer != region.Base {
		t.Errorf("origin of an unchanged rule %+v", o)
	}
	if st := wb.Storage("groundnut"); st.CostPerQMonth != 25 || st.MaxMonths != prices.StorageFor("groundnut").MaxMonths || !strings.Contains(st.Source, "Test source, p. 6") {
		t.Errorf("storage %+v", st)
	}
	if m := wb.MSP("groundnut"); len(m) != 1 || m[0].Price != 8000 {
		t.Errorf("msp %+v", m)
	}
	if sc := wb.Schemes()[0]; !reflect.DeepEqual(sc.Crops, []string{"rice", "groundnut"}) || sc.LossNoticeHours != 72 || sc.Ref.Source != "t" {
		t.Errorf("scheme %+v", sc)
	}
	if _, ok := wb.Book().Product("neem_cake_local"); !ok {
		t.Error("the new product is not in West Bengal's book")
	}
	if _, ok := base.Book().Product("neem_cake_local"); ok {
		t.Error("the new product leaked into the base")
	}
	req := inputs.Request{Fields: []inputs.Field{{Crop: "groundnut", Hectares: 1, Sow: "2026-07-01"}}, Method: inputs.Conventional, Water: inputs.Rainfed}
	pw, err := wb.Book().Plan(req)
	if err != nil {
		t.Fatal(err)
	}
	pb, err := base.Book().Plan(req)
	if err != nil {
		t.Fatal(err)
	}
	jw, _ := json.Marshal(pw.BuyBefore)
	jb, _ := json.Marshal(pb.BuyBefore)
	if string(jw) == string(jb) {
		t.Error("the overlay's groundnut dose did not change the shopping list")
	}
}

// A bad overlay stops the load with ErrInvalid, naming its file and key.
func TestInvalidOverlayFailsWithFileAndKey(t *testing.T) {
	ap := `{"country": "IN", "state": "Bihar"}`
	ref := `"ref": {"source": "t", "where": "p. 1"}`
	for _, tc := range []struct{ name, body, want string }{
		{"top-level typo", overlay(ap, `"crop": {}`), `unknown field "crop"`},
		{"crop field typo", overlay(ap, `"crops": {"groundnut": {"dayz": [90, 100], `+ref+`}}`), "crops.groundnut.dayz: unknown key"},
		{"rules typo", overlay(ap, `"rules": {"turnaround": 20, `+ref+`}`), "rules.turnaround: unknown key"},
		{"rotation typo", overlay(ap, `"rules": {"rotation": {"same": -5}, `+ref+`}`), "rules.rotation.same: unknown key"},
		{"no ref", overlay(ap, `"crops": {"groundnut": {"days": [90, 100]}}`), `crops.groundnut: every overridden value needs a "ref"`},
		{"unknown source", overlay(ap, `"crops": {"groundnut": {"days": [90, 100], "ref": {"source": "x", "where": "p"}}}`), "crops.groundnut.ref"},
		{"wrong type", overlay(ap, `"crops": {"groundnut": {"days": "long", `+ref+`}}`), "crops.groundnut"},
		{"bad range", overlay(ap, `"crops": {"groundnut": {"days": [130, 100], `+ref+`}}`), "groundnut days: range"},
		{"unknown crop in inputs", overlay(ap, `"inputs": {"crops": {"yam": {"per_plant": false, `+ref+`}}}`), `crop "yam": not in the crop library`},
		{"banned product", overlay(ap, `"inputs": {"products": {"mono": {"name": "Monocrotophos 36 SL", "kind": "pesticide", "synthetic": true, "unit": "ml", "active": "monocrotophos", `+ref+`}}}`), "banned or restricted"},
		{"unknown crop in scheme", overlay(ap, `"schemes": {"x": {"name": "X", "kind": "insurance", "crops": ["yam"], `+ref+`}}`), `crop "yam"`},
		{"scheme year without a season", overlay(ap, `"schemes": {"x": {"name": "X", "kind": "insurance", "year": 2026, `+ref+`}}`), "schemes.x: year 2026"},
		{"scheme season unknown", overlay(ap, `"schemes": {"x": {"name": "X", "kind": "insurance", "season": "monsoon", "year": 2026, `+ref+`}}`), `season "monsoon"`},
		{"scheme cutoff of another season", overlay(ap, `"schemes": {"x": {"name": "X", "kind": "insurance", "season": "kharif", "year": 2026, "cutoffs": [{"season": "rabi", "what": "enrolment", "by": "12-15"}], `+ref+`}}`), "for kharif only"},
		{"bad rules", overlay(ap, `"rules": {"max_wait_days": 0, `+ref+`}`), "max_wait_days"},
		{"bad window", overlay(ap, `"crops": {"groundnut": {"windows": [{"season": "kharif", "from": "6-1", "to": "07-31", "source": "s"}], `+ref+`}}`), "want MM-DD"},
		{"bad region", `{"region": {"country": "India"}}`, "two-letter country"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := load(t, map[string]string{"bihar.json": tc.body})
			if !errors.Is(err, region.ErrInvalid) {
				t.Fatalf("err %v, want ErrInvalid", err)
			}
			if !strings.Contains(err.Error(), "bihar.json") || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err %q: want the file and %q", err, tc.want)
			}
		})
	}
}

// Two files for one region are an error; so is one for an embedded
// region, since the most specific layer must be one file.
func TestTwoFilesForOneRegion(t *testing.T) {
	_, err := load(t, map[string]string{"a.json": `{"region": {"country": "IN", "state": "Andhra Pradesh"}}`})
	if !errors.Is(err, region.ErrInvalid) || !strings.Contains(err.Error(), "in-andhra-pradesh.json") {
		t.Errorf("err %v", err)
	}
	// Names that differ only in case and punctuation match the same
	// place, so they are the same region.
	for _, name := range []string{"andhra-pradesh", "ANDHRA PRADESH.", "Andhra  Pradesh"} {
		_, err := load(t, map[string]string{"a.json": `{"region": {"country": "IN", "state": "` + name + `"}}`})
		if !errors.Is(err, region.ErrInvalid) || !strings.Contains(err.Error(), "in-andhra-pradesh.json") {
			t.Errorf("%q: err %v; want it refused as Andhra Pradesh again", name, err)
		}
	}
}
