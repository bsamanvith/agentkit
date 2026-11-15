package inputs_test

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/calendar"
	"github.com/bsamanvith/agentkit/apps/farm/inputs"
)

func lib(t *testing.T) *agro.Library {
	t.Helper()
	l, err := agro.Default()
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func book(t *testing.T) (*agro.Library, *inputs.Book) {
	t.Helper()
	l := lib(t)
	b, err := inputs.Default(l)
	if err != nil {
		t.Fatal(err)
	}
	return l, b
}

func plan(t *testing.T, b *inputs.Book, m inputs.Method, w inputs.Water, fs ...inputs.Field) inputs.Plan {
	t.Helper()
	p, err := b.Plan(inputs.Request{Fields: fs, Method: m, Water: w})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func date(s string) time.Time {
	d, err := time.Parse(agro.DateLayout, s)
	if err != nil {
		panic(err)
	}
	return d
}

var (
	methods = []inputs.Method{inputs.Organic, inputs.Conventional}
	waters  = []inputs.Water{inputs.Rainfed, inputs.Irrigated}
)

// every runs f on the plan of one hectare of every crop of the library,
// both methods and both ways of watering.
func every(t *testing.T, f func(c agro.Crop, m inputs.Method, w inputs.Water, p inputs.Plan)) {
	t.Helper()
	l, b := book(t)
	for _, c := range l.Crops() {
		for _, m := range methods {
			for _, w := range waters {
				p, err := b.Plan(inputs.Request{Fields: []inputs.Field{{Crop: c.ID, Hectares: 1, Sow: "2026-06-15"}}, Method: m, Water: w})
				if err != nil {
					t.Fatalf("%s %s %s: %v", c.ID, m, w, err)
				}
				f(c, m, w, p)
			}
		}
	}
}

// TestEveryCropResolves: every crop of the library gets a plan on both
// methods and both waterings, with something to apply, and every plan
// says the values are starter values to review and the label wins.
func TestEveryCropResolves(t *testing.T) {
	t.Parallel()
	n := 0
	every(t, func(c agro.Crop, m inputs.Method, w inputs.Water, p inputs.Plan) {
		n++
		if len(p.Notes) < 2 || p.Notes[0] != inputs.StarterNote || p.Notes[1] != inputs.LabelNote {
			t.Errorf("%s %s %s: notes %q do not start with the starter and label notes", c.ID, m, w, p.Notes)
		}
		if len(p.Schedule) == 0 || len(p.BuyBefore)+len(p.BuyLater) == 0 {
			// Only when the sources give nothing, and the plan says so.
			if !slices.ContainsFunc(p.Notes, func(s string) bool { return strings.Contains(s, "give no inputs") }) {
				t.Errorf("%s %s %s: an empty plan that does not say why", c.ID, m, w)
			}
			return
		}
		if len(p.Sources) == 0 {
			t.Errorf("%s %s %s: no sources", c.ID, m, w)
		}
	})
	if want := len(lib(t).Crops()) * 4; n != want || want < 39*4 {
		t.Errorf("%d plans, want %d (39 crops or more)", n, want)
	}
}

// TestGroundnutByHand: a hectare of rainfed groundnut, conventional, gets
// the TNAU dose 10:10:45 as DAP, urea and MOP worked out by hand, and
// rounded up to what is sold.
func TestGroundnutByHand(t *testing.T) {
	t.Parallel()
	_, b := book(t)
	p := plan(t, b, inputs.Conventional, inputs.Rainfed, inputs.Field{Crop: "groundnut", Hectares: 1, Sow: "2026-06-15"})
	dap := 10 / 0.46               // kg for 10 kg P2O5
	urea := (10 - dap*0.18) / 0.46 // the rest of the 10 kg N
	mop := 45 / 0.60
	for _, c := range []struct {
		product   string
		need, qty float64
		packs     int
	}{
		{"dap", dap, 50, 1}, {"urea", urea, 45, 1}, {"mop", mop, 75, 0}, {"seed:groundnut", 120, 120, 0}, {"fym", 12.5, 12.5, 0},
	} {
		i := slices.IndexFunc(p.BuyBefore, func(it inputs.Item) bool { return it.Product == c.product })
		if i < 0 {
			t.Errorf("%s not in the buy-before list", c.product)
			continue
		}
		it := p.BuyBefore[i]
		if math.Abs(it.Need-c.need) > 0.01 || it.Qty != c.qty || it.Packs != c.packs {
			t.Errorf("%s: need %v buy %v (%d packs), want need %.2f buy %v (%d packs)", c.product, it.Need, it.Qty, it.Packs, c.need, c.qty, c.packs)
		}
	}
	// Gypsum goes on at 45 days: bought later.
	if !slices.ContainsFunc(p.BuyLater, func(it inputs.Item) bool { return it.Product == "gypsum" && it.Qty == 400 }) {
		t.Errorf("400 kg gypsum is not in the buy-later list: %+v", p.BuyLater)
	}
}

// TestScalesWithArea: three hectares need three times what one does, and
// every quantity is rounded up to what is sold, never down.
func TestScalesWithArea(t *testing.T) {
	t.Parallel()
	_, b := book(t)
	for _, crop := range []string{"groundnut", "rice", "mango", "tomato"} {
		for _, m := range methods {
			one := plan(t, b, m, inputs.Irrigated, inputs.Field{Crop: crop, Hectares: 1, Sow: "2026-06-15"})
			three := plan(t, b, m, inputs.Irrigated, inputs.Field{Crop: crop, Hectares: 3, Sow: "2026-06-15"})
			needs := func(p inputs.Plan) map[string]float64 {
				out := map[string]float64{}
				for _, it := range slices.Concat(p.BuyBefore, p.BuyLater) {
					out[it.Product] += it.Need
				}
				return out
			}
			n1, n3 := needs(one), needs(three)
			if len(n1) == 0 || len(n1) != len(n3) {
				t.Fatalf("%s %s: %d products for 1 ha, %d for 3 ha", crop, m, len(n1), len(n3))
			}
			for k, v := range n1 {
				if math.Abs(n3[k]-3*v) > 0.05 {
					t.Errorf("%s %s %s: %v for 3 ha, want 3 x %v", crop, m, k, n3[k], v)
				}
			}
			for _, it := range slices.Concat(three.BuyBefore, three.BuyLater) {
				p, _ := b.Product(it.Product)
				step := map[string]float64{"t": 0.5, "kg": 1, "g": 100, "l": 1, "ml": 100}[it.Unit]
				if p.Pack > 0 {
					step = p.Pack
					if it.Qty != float64(it.Packs)*p.Pack || it.Pack != p.PackName {
						t.Errorf("%s: %v %s is not %d whole %s", it.Product, it.Qty, it.Unit, it.Packs, p.PackName)
					}
				}
				if step == 0 {
					step = 1
				}
				if it.Qty < it.Need-1e-9 || it.Qty-it.Need >= step || math.Abs(it.Qty/step-math.Round(it.Qty/step)) > 1e-6 {
					t.Errorf("%s: need %v, buy %v %s: not the need rounded up to a step of %v", it.Product, it.Need, it.Qty, it.Unit, step)
				}
			}
		}
	}
}

// TestSold: rounding up to sold units.
func TestSold(t *testing.T) {
	t.Parallel()
	_, b := book(t)
	urea, _ := b.Product("urea")
	dap, _ := b.Product("dap")
	mop, _ := b.Product("mop")
	fym, _ := b.Product("fym")
	tri, _ := b.Product("trichoderma")
	azo, _ := b.Product("azospirillum")
	for _, c := range []struct {
		p     inputs.Product
		need  float64
		qty   float64
		packs int
	}{
		{urea, 1, 45, 1},
		{urea, 45, 45, 1},
		{urea, 45.01, 90, 2},
		{dap, 100, 100, 2},
		{dap, 100.5, 150, 3},
		{mop, 20.01, 21, 0},
		{mop, 75, 75, 0},
		{fym, 12.6, 13, 0},
		{fym, 12.5, 12.5, 0},
		{tri, 32, 100, 0},
		{tri, 480, 500, 0},
		{azo, 2000, 2000, 10},
		{azo, 2001, 2200, 11},
	} {
		qty, packs := inputs.Sold(c.p, c.need, "")
		if qty != c.qty || packs != c.packs {
			t.Errorf("%s %v: %v (%d packs), want %v (%d packs)", c.p.ID, c.need, qty, packs, c.qty, c.packs)
		}
	}
	if qty, _ := inputs.Sold(inputs.Product{}, 74999.5, "setts"); qty != 75000 {
		t.Errorf("setts: %v, want whole setts", qty)
	}
}

// TestOrganicHasNoSynthetic: an organic plan never lists a synthetic
// product: not to buy, not on the schedule, not as a remedy.
func TestOrganicHasNoSynthetic(t *testing.T) {
	t.Parallel()
	_, b := book(t)
	synthetic := func(id string) bool {
		p, ok := b.Product(id)
		return ok && p.Synthetic
	}
	every(t, func(c agro.Crop, m inputs.Method, w inputs.Water, p inputs.Plan) {
		if m != inputs.Organic {
			return
		}
		for _, it := range slices.Concat(p.BuyBefore, p.BuyLater) {
			if it.Synthetic || synthetic(it.Product) {
				t.Errorf("%s %s: buys synthetic %s", c.ID, w, it.Product)
			}
		}
		for _, k := range p.Schedule {
			for _, d := range k.Doses {
				if d.Synthetic || synthetic(d.Product) {
					t.Errorf("%s %s: %s applies synthetic %s", c.ID, w, k.Key, d.Product)
				}
			}
		}
		for _, wt := range p.IfSeen {
			for _, o := range wt.Options {
				if o.Synthetic || synthetic(o.Product) || o.Route == inputs.RouteConventional {
					t.Errorf("%s %s: %s offers %s", c.ID, w, wt.Problem, o.Product)
				}
			}
		}
	})
	// The conventional route does use synthetic products, so the check
	// above has something to catch.
	p := plan(t, b, inputs.Conventional, inputs.Irrigated, inputs.Field{Crop: "rice", Hectares: 1, Sow: "2026-06-15"})
	if !slices.ContainsFunc(p.BuyBefore, func(it inputs.Item) bool { return it.Synthetic }) {
		t.Error("conventional rice buys nothing synthetic")
	}
}

// TestCurativeOnlyIfSeen: what treats a pest is not on the shopping list
// but under "only if you see", with the threshold to act at.
func TestCurativeOnlyIfSeen(t *testing.T) {
	t.Parallel()
	_, b := book(t)
	p := plan(t, b, inputs.Conventional, inputs.Irrigated, inputs.Field{Crop: "rice", Hectares: 2, Sow: "2026-06-15"})
	for _, it := range slices.Concat(p.BuyBefore, p.BuyLater) {
		if it.Product == "chlorantraniliprole_185sc" || it.Product == "pymetrozine_50wg" || it.Product == "btk" {
			t.Errorf("curative %s is on the shopping list", it.Product)
		}
	}
	i := slices.IndexFunc(p.IfSeen, func(w inputs.Watch) bool { return w.Problem == "stem_borer" })
	if i < 0 {
		t.Fatalf("no stem borer watch: %+v", p.IfSeen)
	}
	w := p.IfSeen[i]
	if !strings.HasPrefix(w.Title, "Only if you see ") || w.Threshold == "" || w.Signs == "" {
		t.Errorf("stem borer watch: %+v", w)
	}
	o := w.Options[slices.IndexFunc(w.Options, func(o inputs.Option) bool { return o.Product == "chlorantraniliprole_185sc" })]
	if o.Qty != 300 || o.WaterPerAcre != "202 litres" || o.PHIDays == nil || *o.PHIDays != 47 || !slices.Contains(o.Cautions, inputs.CautionLabel) {
		t.Errorf("chlorantraniliprole for 2 ha: %+v", o)
	}
}

// TestNoBannedActive: no product, remedy or prevention in the data names
// an active ingredient on the banned and restricted list, and loading data
// that does is refused.
func TestNoBannedActive(t *testing.T) {
	t.Parallel()
	l, b := book(t)
	bl := b.Banned()
	if bl.AsOf == "" || bl.Checked == "" || len(bl.Actives) < 90 {
		t.Fatalf("banned list: as of %q, checked %q, %d actives", bl.AsOf, bl.Checked, len(bl.Actives))
	}
	src := map[string]bool{}
	for _, s := range b.Sources() {
		src[s.ID] = true
	}
	var names []string
	for _, a := range bl.Actives {
		if !src[a.Ref.Source] {
			t.Errorf("banned %s: unknown source %q", a.Name, a.Ref.Source)
		}
		for _, n := range append([]string{a.Name}, a.Aka...) {
			names = append(names, " "+norm(n)+" ")
		}
	}
	for _, want := range []string{"monocrotophos", "quinalphos", "chlorpyrifos", "carbofuran", "mancozeb", "phorate", "methomyl"} {
		if !slices.Contains(names, " "+want+" ") {
			t.Errorf("%s is not on the banned list", want)
		}
	}
	for _, v := range b.Values() {
		if strings.HasPrefix(v.What, "kept out: ") {
			continue
		}
		text := " " + norm(v.Value) + " "
		for _, n := range names {
			if strings.Contains(text, n) {
				t.Errorf("%s %s names %q: %s", v.Crop, v.What, strings.TrimSpace(n), v.Value)
			}
		}
	}
	data := readData(t)
	products := data["products"].([]any)
	data["products"] = append(products, map[string]any{
		"id": "mcp", "name": "Monocrotophos 36 % SL", "kind": "pesticide", "synthetic": true, "unit": "ml",
		"active": "monocrotophos", "ref": map[string]any{"source": "cibrc-mup-2019", "where": "x"},
	})
	if _, err := inputs.Load(l, marshal(t, data), readFile(t, "data/banned.json")); !errors.Is(err, agro.ErrInvalid) || !strings.Contains(err.Error(), "monocrotophos") {
		t.Errorf("loading a banned active: %v, want ErrInvalid naming it", err)
	}
}

func norm(s string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	}), " ")
}

func readFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func readData(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(readFile(t, "data/inputs.json"), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func marshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestLoadRefuses: data that breaks a rule is refused: a synthetic
// product in the organic route, a reference to no source, a crop not in
// the library, splits that do not add up.
func TestLoadRefuses(t *testing.T) {
	t.Parallel()
	l := lib(t)
	crop := func(m map[string]any, id string) map[string]any {
		for _, c := range m["crops"].([]any) {
			if c.(map[string]any)["crop"] == id {
				return c.(map[string]any)
			}
		}
		t.Fatalf("no crop %s", id)
		return nil
	}
	for name, edit := range map[string]func(m map[string]any){
		"urea in the organic route": func(m map[string]any) {
			c := crop(m, "groundnut")
			c["organic"] = append(c["organic"].([]any), map[string]any{"product": "urea", "qty": 10, "at": "sow", "route": "organic", "ref": map[string]any{"source": "tnau-cpg-agri-2020", "where": "x"}})
		},
		"unknown source": func(m map[string]any) {
			crop(m, "rice")["irrigated"].(map[string]any)["ref"] = map[string]any{"source": "a-blog", "where": "x"}
		},
		"crop not in the library": func(m map[string]any) {
			m["crops"] = append(m["crops"].([]any), map[string]any{"crop": "citrus"})
		},
		"splits off 100 %": func(m map[string]any) {
			crop(m, "wheat")["splits"].([]any)[1].(map[string]any)["n"] = 40.0
		},
		"unknown calendar key": func(m map[string]any) {
			crop(m, "groundnut")["splits"].([]any)[0].(map[string]any)["at"] = "no-such-task"
		},
	} {
		m := readData(t)
		edit(m)
		if _, err := inputs.Load(l, marshal(t, m), readFile(t, "data/banned.json")); !errors.Is(err, agro.ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
}

// TestEveryValueHasSource: every value, of every crop of the library,
// names a source with a name and a URL and the place in it; every crop
// has values, and those a crop takes from its kind are marked default.
func TestEveryValueHasSource(t *testing.T) {
	t.Parallel()
	l, b := book(t)
	crops := map[string]int{}
	defaults := map[string]bool{}
	for _, v := range b.Values() {
		if v.Source.ID == "" || v.Source.Name == "" || !strings.HasPrefix(v.Source.URL, "https://") || v.Ref.Where == "" || v.Source.Read == "" {
			t.Errorf("%s %s: source %+v at %q", v.Crop, v.What, v.Source, v.Ref.Where)
		}
		crops[v.Crop]++
		if v.Default {
			defaults[v.Crop] = true
		}
	}
	for _, c := range l.Crops() {
		if crops[c.ID] == 0 {
			t.Errorf("%s: no values", c.ID)
		}
	}
	for _, c := range []string{"quinoa", "mustard", "chia", "dragon_fruit", "avocado"} {
		if !defaults[c] {
			t.Errorf("%s has no values of its own but none is marked default", c)
		}
	}
	if defaults["groundnut"] {
		t.Error("groundnut has its own values but some are marked default")
	}
}

// TestScheduleFollowsSowing: moving the sowing date moves every entry by
// as much and keeps its key; each entry's stage is the calendar's stage on
// its day, and entries on a calendar task fall on that task's day.
func TestScheduleFollowsSowing(t *testing.T) {
	t.Parallel()
	l, b := book(t)
	for _, crop := range []string{"groundnut", "rice", "cotton", "mango", "cocoa"} {
		for _, m := range methods {
			c, _ := l.Crop(crop)
			a := plan(t, b, m, inputs.Irrigated, inputs.Field{Crop: crop, Hectares: 1, Sow: "2026-06-15"})
			z, err := calendar.ForZone(l, 0, agro.Zone{Crop: crop, Sow: "2026-06-15"}, true)
			if err != nil {
				t.Fatal(err)
			}
			for _, k := range a.Schedule {
				if got := calendar.StageAt(c, date("2026-06-15"), date(k.Date)).Name; got != k.Stage {
					t.Errorf("%s %s: stage %s on %s, calendar says %s", crop, k.Key, k.Stage, k.Date, got)
				}
				if i := slices.IndexFunc(z.Tasks, func(ct calendar.Task) bool { return ct.Key == k.Key }); i >= 0 && z.Tasks[i].Date != k.Date {
					t.Errorf("%s %s: on %s, the calendar task is on %s", crop, k.Key, k.Date, z.Tasks[i].Date)
				}
			}
			if c.Perennial {
				continue // month-keyed entries keep their month, not their gap
			}
			bb := plan(t, b, m, inputs.Irrigated, inputs.Field{Crop: crop, Hectares: 1, Sow: "2026-07-15"})
			if len(a.Schedule) != len(bb.Schedule) {
				t.Fatalf("%s %s: %d entries, then %d", crop, m, len(a.Schedule), len(bb.Schedule))
			}
			for i, k := range a.Schedule {
				k2 := bb.Schedule[i]
				if k.Key != k2.Key || k.DAS != k2.DAS || date(k2.Date).Sub(date(k.Date)) != 30*24*time.Hour {
					t.Errorf("%s %s: %s on %s (day %d), then %s on %s (day %d)", crop, m, k.Key, k.Date, k.DAS, k2.Key, k2.Date, k2.DAS)
				}
			}
		}
	}
}

// TestPHI: no spray is scheduled after the first harvest day less its
// PHI, and every remedy with a PHI says the last day it may be sprayed.
func TestPHI(t *testing.T) {
	t.Parallel()
	l := lib(t)
	check := func(c agro.Crop, p inputs.Plan, sow string) int {
		harvest := date(sow).AddDate(0, 0, int(c.Days[0]))
		n := 0
		for _, k := range p.Schedule {
			if k.PHIDays != nil {
				n++
				if date(k.Date).After(harvest.AddDate(0, 0, -*k.PHIDays)) {
					t.Errorf("%s: %s on %s is within its %d-day PHI of the harvest on %s", c.ID, k.Key, k.Date, *k.PHIDays, harvest.Format(agro.DateLayout))
				}
			}
			for _, d := range k.Doses {
				if d.Synthetic && k.Kind == "spray" && k.PHIDays == nil {
					t.Errorf("%s: synthetic spray %s scheduled with no PHI", c.ID, k.Key)
				}
			}
		}
		for _, w := range p.IfSeen {
			for _, o := range w.Options {
				if o.PHIDays != nil && o.LastSpray != harvest.AddDate(0, 0, -*o.PHIDays).Format(agro.DateLayout) {
					t.Errorf("%s %s: last spray %s, want the harvest %s less %d days", c.ID, o.Product, o.LastSpray, harvest.Format(agro.DateLayout), *o.PHIDays)
				}
			}
		}
		return n
	}
	every(t, func(c agro.Crop, _ inputs.Method, _ inputs.Water, p inputs.Plan) { check(c, p, "2026-06-15") })

	// A preventive spray with a 10-day PHI on day 100 of a crop harvested
	// from day 90 is left out with a note; on day 20 it is scheduled.
	maize, _ := l.Crop("maize")
	for _, c := range []struct {
		das       int
		scheduled bool
	}{{100, false}, {20, true}} {
		m := readData(t)
		for _, cr := range m["crops"].([]any) {
			if cr.(map[string]any)["crop"] != "maize" {
				continue
			}
			faw := cr.(map[string]any)["protect"].([]any)[0].(map[string]any)
			conv := faw["conventional"].([]any)[0].(map[string]any)
			conv["das"], conv["phi_days"], conv["water_l_ha"] = c.das, 10, []float64{500, 500}
		}
		b, err := inputs.Load(l, marshal(t, m), readFile(t, "data/banned.json"))
		if err != nil {
			t.Fatal(err)
		}
		p := plan(t, b, inputs.Conventional, inputs.Irrigated, inputs.Field{Crop: "maize", Hectares: 1, Sow: "2026-06-15"})
		n := check(maize, p, "2026-06-15")
		noted := slices.ContainsFunc(p.Notes, func(s string) bool { return strings.Contains(s, "spray against fall armyworm is left out") })
		if (n == 1) != c.scheduled || noted == c.scheduled {
			t.Errorf("spray on day %d: %d scheduled with a PHI, noted %v; want scheduled %v", c.das, n, noted, c.scheduled)
		}
	}
}

// TestFieldsAddUp: two fields (an intercrop, or two crops) need what each
// needs on its own, added up before rounding.
func TestFieldsAddUp(t *testing.T) {
	t.Parallel()
	_, b := book(t)
	gn := inputs.Field{Crop: "groundnut", Hectares: 0.8, Sow: "2026-06-15"}
	rg := inputs.Field{Crop: "pigeon_pea", Hectares: 0.2, Sow: "2026-06-15"}
	for _, m := range methods {
		both := plan(t, b, m, inputs.Rainfed, gn, rg)
		sum := map[string]float64{}
		for _, p := range []inputs.Plan{plan(t, b, m, inputs.Rainfed, gn), plan(t, b, m, inputs.Rainfed, rg)} {
			for _, it := range p.BuyBefore {
				sum[it.Product] += it.Need
			}
		}
		for _, it := range both.BuyBefore {
			if math.Abs(it.Need-sum[it.Product]) > 0.02 {
				t.Errorf("%s %s: need %v, the fields alone need %v", m, it.Product, it.Need, sum[it.Product])
			}
			p, _ := b.Product(it.Product)
			if q, _ := inputs.Sold(p, it.Need, it.Unit); q != it.Qty {
				t.Errorf("%s %s: buy %v, the total %v rounds to %v", m, it.Product, it.Qty, it.Need, q)
			}
			delete(sum, it.Product)
		}
		if len(sum) > 0 {
			t.Errorf("%s: missing from the combined list: %v", m, sum)
		}
		if len(both.IfSeen) != len(plan(t, b, m, inputs.Rainfed, gn).IfSeen)+len(plan(t, b, m, inputs.Rainfed, rg).IfSeen) {
			t.Errorf("%s: the watch lists do not add up", m)
		}
	}
	// Urea for conventional fields rounds once, on the total.
	p := plan(t, b, inputs.Conventional, inputs.Rainfed, gn, rg)
	n := 0
	for _, it := range p.BuyBefore {
		if it.Product == "urea" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d urea lines before sowing, want one", n)
	}
}

// TestRejects: requests that make no sense are refused.
func TestRejects(t *testing.T) {
	t.Parallel()
	_, b := book(t)
	ok := inputs.Field{Crop: "rice", Hectares: 1, Sow: "2026-06-15"}
	for name, c := range map[string]struct {
		r    inputs.Request
		want error
	}{
		"no fields":    {inputs.Request{Method: inputs.Organic, Water: inputs.Rainfed}, agro.ErrInvalid},
		"bad method":   {inputs.Request{Fields: []inputs.Field{ok}, Method: "biodynamic", Water: inputs.Rainfed}, agro.ErrInvalid},
		"bad water":    {inputs.Request{Fields: []inputs.Field{ok}, Method: inputs.Organic, Water: "flooded"}, agro.ErrInvalid},
		"zero area":    {inputs.Request{Fields: []inputs.Field{{Crop: "rice", Sow: "2026-06-15"}}, Method: inputs.Organic, Water: inputs.Rainfed}, agro.ErrInvalid},
		"bad date":     {inputs.Request{Fields: []inputs.Field{{Crop: "rice", Hectares: 1, Sow: "15/06/2026"}}, Method: inputs.Organic, Water: inputs.Rainfed}, agro.ErrInvalid},
		"unknown crop": {inputs.Request{Fields: []inputs.Field{{Crop: "citrus", Hectares: 1, Sow: "2026-06-15"}}, Method: inputs.Organic, Water: inputs.Rainfed}, agro.ErrUnknown},
	} {
		if _, err := b.Plan(c.r); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
}

// The crop calendar is read on every plot, organic ones too (the season's
// work where no organic feeding replaces a task, the weather and care
// page, the weekly notice): it names no synthetic product, only what to
// feed and how; the inputs plan says with what.
func TestCalendarNamesNoSyntheticProduct(t *testing.T) {
	t.Parallel()
	l, b := book(t)
	var names []string
	for _, p := range b.Products() {
		if p.Synthetic {
			n := p.Name
			if i := strings.Index(n, " ("); i > 0 {
				n = n[:i]
			}
			names = append(names, strings.ToLower(n))
		}
	}
	for _, c := range l.Crops() {
		for _, irrigated := range []bool{false, true} {
			z, err := calendar.ForZone(l, 0, agro.Zone{Crop: c.ID, Sow: "2026-06-20"}, irrigated)
			if err != nil {
				t.Fatal(err)
			}
			for _, k := range z.Tasks {
				text := strings.ToLower(k.Title + " " + k.Detail)
				for _, n := range names {
					if strings.Contains(text, n) {
						t.Errorf("%s calendar task %s names %q: %s", c.ID, k.Key, n, text)
					}
				}
			}
		}
	}
}

// A feeding says what it feeds and with what, in the same numbers as its
// doses: "First nitrogen feed: urea, 21.7 kg"; never "Feed the crop".
func TestFeedTitlesSayWhatAndWhy(t *testing.T) {
	t.Parallel()
	_, b := book(t)
	p := plan(t, b, inputs.Conventional, inputs.Rainfed, inputs.Field{Crop: "finger_millet", Hectares: 1, Sow: "2026-06-20"})
	var titles []string
	for _, k := range p.Schedule {
		if k.Kind == "feed" && k.Key != "sow" && k.Key != "plough" {
			titles = append(titles, k.Title)
			if len(k.Doses) == 1 && !strings.HasSuffix(k.Title, ": urea, "+inputs.Amount(k.Doses[0].Qty, k.Doses[0].Unit)) {
				t.Errorf("title %q does not say the dose %v as the schedule does", k.Title, k.Doses[0])
			}
		}
	}
	if len(titles) < 2 || !strings.HasPrefix(titles[0], "First nitrogen feed: urea, ") || !strings.HasPrefix(titles[1], "Second nitrogen feed: urea, ") {
		t.Errorf("titles %q; want the first and second nitrogen feeds of urea", titles)
	}
	og := plan(t, b, inputs.Organic, inputs.Rainfed, inputs.Field{Crop: "groundnut", Hectares: 1, Sow: "2026-06-20"})
	for _, k := range slices.Concat(p.Schedule, og.Schedule) {
		if k.Title == "" || strings.HasPrefix(k.Title, "Feed the crop") || k.Title == "Set up protection" {
			t.Errorf("%s: title %q says nothing", k.Key, k.Title)
		}
	}
	if !slices.ContainsFunc(og.Schedule, func(k inputs.Task) bool { return strings.HasPrefix(k.Title, "Sulphur feed: gypsum, ") }) {
		t.Errorf("organic groundnut: no sulphur feed of gypsum among %v", og.Schedule)
	}
}

// One number: the schedule, the shopping list's need and the titles say a
// quantity the same way; what to buy is the need rounded up, never down.
func TestOneNumberForAQuantity(t *testing.T) {
	t.Parallel()
	_, b := book(t)
	p := plan(t, b, inputs.Conventional, inputs.Rainfed, inputs.Field{Crop: "finger_millet", Hectares: 1.03, Sow: "2026-06-20"})
	for _, it := range slices.Concat(p.BuyBefore, p.BuyLater) {
		if it.Qty < it.Need {
			t.Errorf("%s: buy %v, under the %v needed", it.Name, it.Qty, it.Need)
		}
		if it.Product != "seed:finger_millet" {
			continue
		}
		var dose float64
		for _, k := range p.Schedule {
			for _, d := range k.Doses {
				if d.Product == "seed" {
					dose += d.Qty
				}
			}
		}
		if inputs.Amount(dose, it.Unit) != inputs.Amount(it.Need, it.Unit) || inputs.Amount(dose, it.Unit) == inputs.Amount(it.Qty, it.Unit) {
			t.Errorf("seed: schedule %s, need %s, buy %s; want the schedule to say the need, the buy rounded up past it",
				inputs.Amount(dose, it.Unit), inputs.Amount(it.Need, it.Unit), inputs.Amount(it.Qty, it.Unit))
		}
	}
	for _, c := range []struct {
		v    float64
		unit string
		want string
	}{{10.3, "kg", "10.3 kg"}, {21.74, "kg", "21.7 kg"}, {146.7, "kg", "147 kg"}, {11, "kg", "11 kg"}, {1, "t", "1 tonne"}} {
		if got := inputs.Amount(c.v, c.unit); got != c.want {
			t.Errorf("Amount(%v %s) = %q; want %q", c.v, c.unit, got, c.want)
		}
	}
}
