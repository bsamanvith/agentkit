package inputs

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/calendar"
)

// Method is how the farm is run.
type Method string

// The methods.
const (
	Organic      Method = "organic"
	Conventional Method = "conventional"
)

// Water is how a field gets its water.
type Water string

// The ways of watering.
const (
	Rainfed   Water = "rainfed"
	Irrigated Water = "irrigated"
)

// AcresPerHectare converts areas: doses are shown per acre, as farmers buy.
const AcresPerHectare = 2.4710538

// Notes every plan carries.
const (
	StarterNote = "These are starter values from the sources listed, for an agronomist to review before you buy or spray: check them against your soil test and the local package of practices."
	LabelNote   = "Always follow the product label: its dose, how to mix it, the protective gear and the waiting period before harvest. Where the label and this list differ, the label wins."
)

// Cautions on a schedule entry.
const (
	CautionFeed  = "Into moist soil beside the rows; not just before heavy rain, which washes it away."
	CautionRain  = "Do not spray if rain is likely within a day: it washes the spray off."
	CautionWind  = "Do not spray in wind; spray in the cool of the morning or evening, away from water and bee hives."
	CautionGear  = "Wear gloves, a mask, long sleeves and boots; wash after spraying; keep children and animals away."
	CautionLabel = "Follow the label: dose, mixing and the waiting period before harvest."
	CautionSeed  = "Wear gloves; dry treated seed in the shade and sow it the same day; never eat or feed treated seed."
	// NoRemedy stands in for a remedy the sources do not give.
	NoRemedy      = "Our sources give no remedy for this on your method: ask your agronomist or the agriculture office before you buy anything."
	CautionNoBuy  = "Buy only when you see it and it crosses the threshold: these are not for every season."
	cautionNoPHIs = "The waiting period before harvest is not in our sources: read it on the label before you spray."
)

// Field is one crop on an area.
type Field struct {
	Crop     string  `json:"crop"`
	Hectares float64 `json:"hectares"`
	// Sow is the sowing (or planting) date, as agro.DateLayout.
	Sow string `json:"sow"`
}

// Request is what to plan: fields (an intercrop is two fields with the
// same sowing date and their own shares of the area), how they are farmed
// and watered.
type Request struct {
	Fields []Field `json:"fields"`
	Method Method  `json:"method"`
	Water  Water   `json:"water"`
}

// Dose is one product in a schedule entry.
type Dose struct {
	Product   string `json:"product"`
	Name      string `json:"name"`
	Synthetic bool   `json:"synthetic,omitempty"`
	// Qty is for the whole field, in Unit; 0 when the source gives the
	// dose only per litre of spray and no spray volume.
	Qty  float64 `json:"qty"`
	Unit string  `json:"unit"`
	// PerAcre is the dose per acre, PerPlant per plant (crops fed per
	// plant), in Unit.
	PerAcre  float64 `json:"per_acre,omitempty"`
	PerPlant float64 `json:"per_plant,omitempty"`
	// Rate is the dose as the source gives it, for a spray.
	Rate string `json:"rate,omitempty"`
	Note string `json:"note,omitempty"`
}

// Task is one dated entry of the schedule.
type Task struct {
	Field int    `json:"field"`
	Crop  string `json:"crop"`
	// Key is the crop calendar's task key when the entry falls on one
	// ("sow", "nitrogen-1", "y1-fertilise-2"), else a key of this
	// package that stays the same when the sowing date moves
	// ("feed-das-45", "feed-flowering", "jeevamrutham-2", "spray-fall_armyworm").
	Key   string `json:"key"`
	Date  string `json:"date"`
	DAS   int    `json:"das"`
	Stage string `json:"stage"`
	// Kind is feed, seed-treatment, prevent or spray.
	Kind         string   `json:"kind"`
	Title        string   `json:"title"`
	Doses        []Dose   `json:"doses"`
	WaterPerAcre string   `json:"water_per_acre,omitempty"`
	PHIDays      *int     `json:"phi_days,omitempty"`
	Cautions     []string `json:"cautions"`
	Refs         []Ref    `json:"refs"`
	// Default marks values taken from a crop of the same kind.
	Default bool `json:"default,omitempty"`
}

// Item is one line of the shopping list.
type Item struct {
	Product   string `json:"product"`
	Name      string `json:"name"`
	Synthetic bool   `json:"synthetic,omitempty"`
	// Make is true for what is made on the farm or got locally.
	Make bool `json:"make,omitempty"`
	// Need is the exact quantity, Qty rounded up to what is sold: whole
	// packs (Packs of Pack), whole kilograms, 100 g or 100 ml steps,
	// whole litres, half tonnes, whole pieces.
	Need  float64 `json:"need"`
	Qty   float64 `json:"qty"`
	Unit  string  `json:"unit"`
	Packs int     `json:"packs,omitempty"`
	Pack  string  `json:"pack,omitempty"`
	// First is the first day it is needed; For the schedule entries
	// that use it.
	First   string   `json:"first"`
	For     []string `json:"for"`
	Refs    []Ref    `json:"refs"`
	Default bool     `json:"default,omitempty"`
}

// Option is one way to treat a pest or disease.
type Option struct {
	Route     string `json:"route"`
	Product   string `json:"product,omitempty"`
	Name      string `json:"name,omitempty"`
	Active    string `json:"active,omitempty"`
	Synthetic bool   `json:"synthetic,omitempty"`
	// Rate is the dose as the source gives it; Qty and Unit what one
	// spray of the field takes at the highest dose, when it can be known.
	Rate         string  `json:"rate,omitempty"`
	Qty          float64 `json:"qty,omitempty"`
	Unit         string  `json:"unit,omitempty"`
	WaterPerAcre string  `json:"water_per_acre,omitempty"`
	PHIDays      *int    `json:"phi_days,omitempty"`
	// LastSpray is the last day it may be sprayed: the first harvest day
	// less the PHI.
	LastSpray string   `json:"last_spray,omitempty"`
	Text      string   `json:"text,omitempty"`
	Cautions  []string `json:"cautions,omitempty"`
	Ref       Ref      `json:"ref"`
}

// Watch is a pest or disease to look out for, and what to buy only if
// it shows up.
type Watch struct {
	Field   int    `json:"field"`
	Crop    string `json:"crop"`
	Problem string `json:"problem"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	// Title reads "Only if you see ...".
	Title     string   `json:"title"`
	Stages    []string `json:"stages"`
	From      string   `json:"from,omitempty"`
	To        string   `json:"to,omitempty"`
	Signs     string   `json:"signs"`
	Threshold string   `json:"threshold,omitempty"`
	Prevent   []string `json:"prevent,omitempty"`
	Options   []Option `json:"options"`
	Refs      []Ref    `json:"refs"`
}

// Plan is the inputs plan of a request.
type Plan struct {
	Method Method `json:"method"`
	Water  Water  `json:"water"`
	// Notes always start with StarterNote and LabelNote.
	Notes     []string `json:"notes"`
	BuyBefore []Item   `json:"buy_before"`
	BuyLater  []Item   `json:"buy_later"`
	IfSeen    []Watch  `json:"if_seen"`
	Schedule  []Task   `json:"schedule"`
	// Sources are those the plan's values come from.
	Sources []Source `json:"sources"`
}

// Plan returns the shopping list, the schedule and the watch list of a
// request. Quantities of all fields add up before they are rounded to
// what is sold. Errors wrap agro.ErrInvalid or agro.ErrUnknown.
func (b *Book) Plan(r Request) (Plan, error) {
	if r.Method != Organic && r.Method != Conventional {
		return Plan{}, fmt.Errorf("%w: method %q", agro.ErrInvalid, r.Method)
	}
	if r.Water != Rainfed && r.Water != Irrigated {
		return Plan{}, fmt.Errorf("%w: water %q", agro.ErrInvalid, r.Water)
	}
	if len(r.Fields) == 0 {
		return Plan{}, fmt.Errorf("%w: no fields", agro.ErrInvalid)
	}
	p := Plan{Method: r.Method, Water: r.Water, Notes: []string{StarterNote, LabelNote}, Schedule: []Task{}, IfSeen: []Watch{}}
	var needs []need
	for i, f := range r.Fields {
		fp, err := b.field(f, i, r.Method, r.Water == Irrigated)
		if err != nil {
			return Plan{}, err
		}
		p.Schedule = append(p.Schedule, fp.tasks...)
		p.IfSeen = append(p.IfSeen, fp.watch...)
		needs = append(needs, fp.needs...)
		for _, n := range fp.notes {
			if !slices.Contains(p.Notes, n) {
				p.Notes = append(p.Notes, n)
			}
		}
	}
	slices.SortStableFunc(p.Schedule, func(a, b Task) int {
		return cmp.Or(strings.Compare(a.Date, b.Date), a.Field-b.Field, strings.Compare(a.Key, b.Key))
	})
	p.BuyBefore, p.BuyLater = b.shop(needs)
	used := map[string]bool{}
	mark := func(rs []Ref) {
		for _, r := range rs {
			used[r.Source] = true
		}
	}
	for _, t := range p.Schedule {
		mark(t.Refs)
	}
	for _, it := range slices.Concat(p.BuyBefore, p.BuyLater) {
		mark(it.Refs)
	}
	for _, w := range p.IfSeen {
		mark(w.Refs)
	}
	for _, s := range b.data.Sources {
		if used[s.ID] {
			p.Sources = append(p.Sources, s)
		}
	}
	return p, nil
}

// need is a quantity of a product one schedule entry uses.
type need struct {
	product string
	qty     float64 // in the product's unit
	date    string
	before  bool
	label   string
	refs    []Ref
	dflt    bool
	// unit and name are set for seed, which is not a product.
	unit, name string
}

type fieldPlan struct {
	tasks []Task
	watch []Watch
	needs []need
	notes []string
}

// fieldCtx is what the rules need about one field.
type fieldCtx struct {
	b         *Book
	i         int
	crop      agro.Crop
	sow       time.Time
	end       time.Time
	harvest   time.Time
	cal       []calendar.Task
	method    Method
	irrigated bool
	ha        float64
	plants    float64
	perPlant  bool
	tasks     map[string]*Task
	out       fieldPlan
}

func (b *Book) field(f Field, i int, method Method, irrigated bool) (fieldPlan, error) {
	lc, err := b.lib.Crop(f.Crop)
	if err != nil {
		return fieldPlan{}, err
	}
	sow, err := time.Parse(agro.DateLayout, f.Sow)
	if err != nil {
		return fieldPlan{}, fmt.Errorf("%w: field %d sowing date %q: %w", agro.ErrInvalid, i, f.Sow, err)
	}
	if !(f.Hectares > 0 && f.Hectares <= 1e4) {
		return fieldPlan{}, fmt.Errorf("%w: field %d area %v ha", agro.ErrInvalid, i, f.Hectares)
	}
	z, err := calendar.ForZone(b.lib, i, agro.Zone{Crop: lc.ID, Sow: f.Sow}, irrigated)
	if err != nil {
		return fieldPlan{}, err
	}
	x := &fieldCtx{
		b: b, i: i, crop: lc, sow: sow, cal: z.Tasks, method: method, irrigated: irrigated, ha: f.Hectares,
		harvest: sow.AddDate(0, 0, int(lc.Days[0])), tasks: map[string]*Task{},
	}
	x.end = sow.AddDate(0, 0, int(lc.Days[1])+14)
	if lc.Perennial {
		x.end = sow.AddDate(1, 0, 0)
		x.perPlant = true
		x.plants = f.Hectares * 1e8 / (lc.RowCM * lc.PlantCM)
		x.out.notes = append(x.out.notes, fmt.Sprintf("%s is planned for its first year from planting, for %.0f plants (%.0f x %.0f cm spacing).", lc.Name, x.plants, lc.RowCM, lc.PlantCM))
	}
	cd, dflt := b.resolve(lc)
	if dflt {
		x.out.notes = append(x.out.notes, fmt.Sprintf("%s has no values of its own yet: its feeding follows %s (a %s crop) and is marked default; there is no seed rate or pest list for it.", lc.Name, b.cropName(cd.Crop), lc.Kind))
	}
	if err := x.seed(cd, dflt); err != nil {
		return fieldPlan{}, err
	}
	if err := x.nutrition(cd, dflt); err != nil {
		return fieldPlan{}, err
	}
	if err := x.uses(cd, dflt); err != nil {
		return fieldPlan{}, err
	}
	if !dflt {
		if err := x.protect(cd); err != nil {
			return fieldPlan{}, err
		}
	}
	if method == Organic && !lc.Perennial {
		x.jeevamrutham()
	}
	for _, t := range x.tasks {
		x.out.tasks = append(x.out.tasks, *t)
	}
	if len(x.tasks) == 0 {
		x.out.notes = append(x.out.notes, fmt.Sprintf("%s: our sources give no inputs for the %s route in its first season; ask your agronomist.", lc.Name, method))
	}
	slices.SortFunc(x.out.tasks, func(a, b Task) int { return cmp.Or(a.DAS-b.DAS, strings.Compare(a.Key, b.Key)) })
	return x.out, nil
}

// resolve returns the data of a crop, or of the crop its kind defaults
// to (only its feeding) and true.
func (b *Book) resolve(c agro.Crop) (Crop, bool) {
	if d, ok := b.crops[c.ID]; ok {
		return d, false
	}
	like := b.crops[b.kinds[c.Kind]]
	return Crop{
		Crop: like.Crop, PerPlant: like.PerPlant, Rainfed: like.Rainfed, Irrigated: like.Irrigated, PProduct: like.PProduct,
		Splits: like.Splits, SplitsRainfed: like.SplitsRainfed, Add: like.Add, Organic: like.Organic,
	}, true
}

func (b *Book) cropName(id string) string {
	if c, err := b.lib.Crop(id); err == nil {
		return c.Name
	}
	return id
}

// when places a time given as a calendar key, days after sowing, the
// first day of a stage or a month of a perennial's first year. ok is
// false when it falls outside the plan's window.
func (x *fieldCtx) when(at string, das *int, stage string, month int, prefix string) (key string, day time.Time, ok bool, err error) {
	switch {
	case at != "":
		for _, t := range x.cal {
			if t.Key == at || (x.crop.Perennial && strings.HasSuffix(t.Key, "-"+at) && strings.HasPrefix(t.Key, "y")) {
				d, _ := time.Parse(agro.DateLayout, t.Date)
				if t.Key != at && !d.Before(x.end) {
					continue
				}
				return t.Key, d, !d.Before(x.sow.AddDate(0, 0, -60)) && !d.After(x.end), nil
			}
		}
		switch {
		case at == "treat-seed" && x.crop.Perennial:
			return "sow", x.sow, true, nil
		case at == "harvest-from": // a tree's first harvest, years away
			return at, x.harvest, !x.harvest.After(x.end), nil
		}
		return "", time.Time{}, false, fmt.Errorf("%w: %s has no calendar task %q", agro.ErrInvalid, x.crop.ID, at)
	case das != nil:
		d := x.sow.AddDate(0, 0, *das)
		return fmt.Sprintf("%s-das-%d", prefix, *das), d, !d.After(x.end), nil
	case stage != "":
		for d := 0; d <= int(x.crop.Days[1]); d++ {
			day := x.sow.AddDate(0, 0, d)
			if calendar.StageAt(x.crop, x.sow, day).Name == stage {
				return prefix + "-" + stage, day, true, nil
			}
		}
		return "", time.Time{}, false, fmt.Errorf("%w: %s never reaches stage %q", agro.ErrInvalid, x.crop.ID, stage)
	case month >= 1 && month <= 12:
		from := x.sow.AddDate(0, 0, 45)
		d := time.Date(from.Year(), time.Month(month), 1, 0, 0, 0, 0, time.UTC)
		if d.Before(from) {
			d = d.AddDate(1, 0, 0)
		}
		return fmt.Sprintf("y%d-%s-m%02d", d.Year()-x.sow.Year()+1, prefix, month), d, d.Before(x.end), nil
	}
	return "", time.Time{}, false, fmt.Errorf("%w: %s: a time needs a key, days, a stage or a month", agro.ErrInvalid, x.crop.ID)
}

// task returns the schedule entry at key, making it on first use.
func (x *fieldCtx) task(key string, day time.Time, kind string, dflt bool) *Task {
	if t, ok := x.tasks[key]; ok {
		t.Default = t.Default || dflt
		return t
	}
	t := &Task{
		Field: x.i, Crop: x.crop.ID, Key: key, Date: day.Format(agro.DateLayout), DAS: daysBetween(x.sow, day),
		Stage: calendar.StageAt(x.crop, x.sow, day).Name, Kind: kind, Doses: []Dose{}, Cautions: []string{}, Refs: []Ref{}, Default: dflt,
	}
	for _, c := range x.cal {
		if c.Key == key {
			t.Title = c.Title
		}
	}
	if t.Title == "" {
		t.Title = map[string]string{"feed": "Feed the crop", "seed-treatment": "Treat the seed", "prevent": "Set up protection", "spray": "Spray"}[kind]
	}
	x.tasks[key] = t
	return t
}

func daysBetween(a, b time.Time) int { return int(math.Round(b.Sub(a).Hours() / 24)) }

func addRef(rs []Ref, r Ref) []Ref {
	if slices.Contains(rs, r) {
		return rs
	}
	return append(rs, r)
}

func addCaution(cs []string, c ...string) []string {
	for _, s := range c {
		if !slices.Contains(cs, s) {
			cs = append(cs, s)
		}
	}
	return cs
}

// dose adds qty (the product's unit, per hectare or per plant) of a
// product to task t and to the shopping needs.
func (x *fieldCtx) dose(t *Task, p Product, per float64, note string, refs []Ref, dflt bool) {
	total := per * x.ha
	d := Dose{Product: p.ID, Name: p.Name, Synthetic: p.Synthetic, Unit: p.Unit, Note: note}
	if x.perPlant {
		total = per * x.plants
		d.PerPlant = round2(per)
		d.PerAcre = round2(per * x.plants / x.ha / AcresPerHectare)
	} else {
		d.PerAcre = round2(per / AcresPerHectare)
	}
	d.Qty = round2(total)
	t.Doses = append(t.Doses, d)
	for _, r := range append(refs, p.Ref) {
		t.Refs = addRef(t.Refs, r)
	}
	x.out.needs = append(x.out.needs, need{
		product: p.ID, qty: total, date: t.Date, before: t.DAS <= 0, refs: refs, dflt: dflt,
		label: fmt.Sprintf("%s: %s (%s)", x.crop.Name, t.Title, t.Date),
	})
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func (x *fieldCtx) seed(cd Crop, dflt bool) error {
	s := cd.Seed
	if !x.irrigated && cd.SeedRainfed != nil {
		s = cd.SeedRainfed
	}
	if dflt || s == nil {
		return nil
	}
	key, day, _, err := x.when("sow", nil, "", 0, "")
	if err != nil {
		return err
	}
	t := x.task(key, day, "feed", false)
	t.Doses = append(t.Doses, Dose{Product: "seed", Name: x.crop.Name + " " + s.What, Qty: round2(s.Qty * x.ha), Unit: s.Unit, PerAcre: round2(s.Qty / AcresPerHectare)})
	t.Refs = addRef(t.Refs, s.Ref)
	x.out.needs = append(x.out.needs, need{product: "seed:" + x.crop.ID, name: x.crop.Name + " " + s.What, unit: s.Unit, qty: s.Qty * x.ha, date: t.Date, before: true, refs: []Ref{s.Ref}, label: fmt.Sprintf("%s: %s (%s)", x.crop.Name, t.Title, t.Date)})
	seedKg := map[string]float64{"kg": 1, "g": 0.001}[s.Unit] * s.Qty
	for _, st := range cd.SeedTreatment {
		if !x.onRoute(st.Route) {
			continue
		}
		p := x.b.products[st.Product]
		key, day, _, err := x.when("treat-seed", nil, "", 0, "")
		if err != nil {
			return err
		}
		t := x.task(key, day, "seed-treatment", false)
		t.Cautions = addCaution(t.Cautions, CautionSeed, CautionLabel)
		note := fmt.Sprintf("%g %s per kg of seed", st.PerKgSeed, p.Unit)
		if seedKg == 0 {
			t.Doses = append(t.Doses, Dose{Product: p.ID, Name: p.Name, Synthetic: p.Synthetic, Unit: p.Unit, Note: note})
			t.Refs = addRef(t.Refs, st.Ref)
			continue
		}
		x.dose(t, p, st.PerKgSeed*seedKg, note, []Ref{st.Ref}, false)
	}
	return nil
}

func (x *fieldCtx) onRoute(route string) bool {
	switch cmpOr(route, RouteBoth) {
	case RouteBoth:
		return true
	case RouteOrganic:
		return x.method == Organic
	default:
		return x.method == Conventional
	}
}

// nutrition turns the crop's N-P2O5-K2O into fertiliser (conventional)
// and its sulphur into gypsum (both routes), split by stage.
func (x *fieldCtx) nutrition(cd Crop, dflt bool) error {
	n, splits, fallback := cd.Irrigated, cd.Splits, false
	if !x.irrigated {
		n = cd.Rainfed
		if len(cd.SplitsRainfed) > 0 {
			splits = cd.SplitsRainfed
		}
	}
	if n == nil {
		n, fallback = cmpPtr(cd.Irrigated, cd.Rainfed), true
	}
	if n == nil {
		if x.method == Conventional && cd.Mature != nil {
			x.out.notes = append(x.out.notes, fmt.Sprintf("%s: our sources give fertiliser only for grown trees, not the first year; the plan feeds manure only. Ask your agronomist about young trees.", x.crop.Name))
		}
		return nil
	}
	if fallback {
		x.out.notes = append(x.out.notes, fmt.Sprintf("%s: our sources give no %s dose, so the plan uses the other one, marked default.", x.crop.Name, map[bool]string{true: "irrigated", false: "rainfed"}[x.irrigated]))
	}
	d := dflt || fallback
	// Per plant doses are in grams; fertilisers are in kilograms.
	scale := 1.0
	if x.perPlant {
		scale = 0.001
	}
	pProd := cmpOr(cd.PProduct, "dap")
	for si, s := range splits {
		key, day, ok, err := x.when(s.At, s.DAS, s.Stage, s.Month, "feed")
		if err != nil {
			return err
		}
		gypsum := si == 0 && n.S > 0
		if !ok || (x.method != Conventional && !gypsum) {
			continue
		}
		t := x.task(key, day, "feed", d)
		t.Cautions = addCaution(t.Cautions, CautionFeed)
		if gypsum {
			g := x.b.products["gypsum"]
			x.dose(t, g, n.S/(g.S/100)*scale, fmt.Sprintf("for %g of sulphur", n.S), []Ref{n.Ref}, d)
		}
		if x.method != Conventional {
			continue
		}
		nn, pp, kk := n.N*s.N/100, n.P*s.P/100, n.K*s.K/100
		if pp > 0 {
			p := x.b.products[pProd]
			kg := pp / (p.P / 100)
			nn = math.Max(0, nn-kg*p.N/100)
			x.dose(t, p, kg*scale, fmt.Sprintf("for %.4g of P2O5", pp), []Ref{n.Ref}, d)
		}
		if nn > 0 {
			p := x.b.products["urea"]
			x.dose(t, p, nn/(p.N/100)*scale, fmt.Sprintf("for %.4g of N", nn), []Ref{n.Ref}, d)
		}
		if kk > 0 {
			p := x.b.products["mop"]
			x.dose(t, p, kk/(p.K/100)*scale, fmt.Sprintf("for %.4g of K2O", kk), []Ref{n.Ref}, d)
		}
	}
	return nil
}

func cmpPtr[T any](a, b *T) *T {
	if a != nil {
		return a
	}
	return b
}

// uses adds the crop's extra inputs and manures of the method's route.
func (x *fieldCtx) uses(cd Crop, dflt bool) error {
	us := slices.Concat(cd.Add, cd.Organic)
	// Bulk manure (farmyard manure, vermicompost) is sold by the tonne.
	manure := slices.ContainsFunc(cd.Organic, func(u Use) bool {
		p := x.b.products[u.Product]
		return p.Kind == "manure" && p.Unit == "t"
	})
	extraDefault := false
	if x.method == Organic && !manure && !x.perPlant {
		us = append(us, x.b.data.DefaultOrganic...)
		extraDefault = true
	}
	for i, u := range us {
		if !x.onRoute(u.Route) {
			continue
		}
		p := x.b.products[u.Product]
		key, day, ok, err := x.when(u.At, u.DAS, "", 0, "feed")
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		d := dflt || (extraDefault && i >= len(cd.Add)+len(cd.Organic))
		t := x.task(key, day, "feed", d)
		t.Cautions = addCaution(t.Cautions, CautionFeed)
		x.dose(t, p, u.Qty*factor(cmpOr(u.Unit, p.Unit), p.Unit), u.Note, []Ref{u.Ref}, d)
	}
	return nil
}

func (x *fieldCtx) jeevamrutham() {
	j := x.b.data.Jeevamrutham
	stop := strings.TrimPrefix(j.Until, "stage:")
	acres := x.ha * AcresPerHectare
	for k, das := 1, j.EveryDays; ; k, das = k+1, das+j.EveryDays {
		day := x.sow.AddDate(0, 0, das)
		st := calendar.StageAt(x.crop, x.sow, day).Name
		if st == stop || st == calendar.Harvest || st == calendar.Harvested {
			return
		}
		t := x.task(fmt.Sprintf("jeevamrutham-%d", k), day, "feed", false)
		t.Title = fmt.Sprintf("Jeevamrutham: %.0f litres per acre (%.0f litres for the field)", j.LitresPerAcre, j.LitresPerAcre*acres)
		t.Cautions = addCaution(t.Cautions, j.Note)
		for _, in := range j.Batch {
			p := x.b.products[in.Product]
			x.dose(t, p, in.Qty*acres/x.ha, "per batch of 200 litres", []Ref{j.Ref}, false)
		}
	}
}

func rate(r Remedy, unit string) string {
	per := map[string]string{"ha": "per ha", "l": "per litre of water", "tree": "per tree"}[r.Per]
	if r.Dose[0] == r.Dose[1] {
		return fmt.Sprintf("%g %s %s", r.Dose[0], unit, per)
	}
	return fmt.Sprintf("%g to %g %s %s", r.Dose[0], r.Dose[1], unit, per)
}

func water(r Remedy) string {
	if r.WaterLH == nil {
		return ""
	}
	lo, hi := math.Round(r.WaterLH[0]/AcresPerHectare), math.Round(r.WaterLH[1]/AcresPerHectare)
	if lo == hi {
		return fmt.Sprintf("%.0f litres", lo)
	}
	return fmt.Sprintf("%.0f to %.0f litres", lo, hi)
}

// sprayQty is what one spray of the field takes at the highest dose, in
// the product's unit; 0 when the source gives no spray volume.
func (x *fieldCtx) sprayQty(r Remedy) float64 {
	switch r.Per {
	case "ha":
		return r.Dose[1] * x.ha
	case "tree":
		return r.Dose[1] * x.plants
	case "l":
		if r.WaterLH != nil {
			return r.Dose[1] * r.WaterLH[1] * x.ha
		}
	}
	return 0
}

func (x *fieldCtx) protect(cd Crop) error {
	for _, pr := range cd.Protect {
		w := Watch{
			Field: x.i, Crop: x.crop.ID, Problem: pr.ID, Name: pr.Name, Kind: pr.Kind, Stages: pr.Stages,
			Signs: pr.Signs, Threshold: pr.Threshold, Options: []Option{}, Refs: []Ref{pr.SignsRef},
			Title: "Only if you see " + strings.ToLower(pr.Name[:1]) + pr.Name[1:] + ": " + pr.Signs,
		}
		if pr.ThresholdRef != nil {
			w.Refs = addRef(w.Refs, *pr.ThresholdRef)
		}
		w.From, w.To = x.span(pr.Stages)
		for _, pv := range pr.Prevent {
			w.Refs = addRef(w.Refs, pv.Ref)
			if pv.Product == "" {
				w.Prevent = append(w.Prevent, pv.Text)
				continue
			}
			p := x.b.products[pv.Product]
			key, day, ok, err := x.when(pv.At, pv.DAS, "", 0, "prevent")
			if err != nil {
				return err
			}
			w.Prevent = append(w.Prevent, fmt.Sprintf("%s: %g per ha. %s", p.Name, pv.Qty, pv.Note))
			if !ok {
				continue
			}
			t := x.task(key, day, "prevent", false)
			t.Cautions = addCaution(t.Cautions, CautionLabel)
			x.dose(t, p, pv.Qty, strings.TrimSpace("Against "+strings.ToLower(pr.Name)+". "+pv.Note), []Ref{pv.Ref}, false)
		}
		routes := []struct {
			route string
			rs    []Remedy
		}{{RouteOrganic, pr.Organic}, {RouteConventional, pr.Conventional}}
		for _, rr := range routes {
			// Organic remedies suit both methods; synthetic ones only the
			// conventional.
			if rr.route == RouteConventional && x.method != Conventional {
				continue
			}
			for _, r := range rr.rs {
				w.Refs = addRef(w.Refs, r.Ref)
				o := Option{Route: rr.route, Text: r.Text, Ref: r.Ref}
				if r.Product == "" {
					w.Options = append(w.Options, o)
					continue
				}
				p := x.b.products[r.Product]
				o.Product, o.Name, o.Active, o.Synthetic = p.ID, p.Name, p.Active, p.Synthetic
				o.Rate, o.Unit, o.WaterPerAcre, o.PHIDays = rate(r, p.Unit), p.Unit, water(r), r.PHIDays
				o.Qty = round2(x.sprayQty(r))
				o.Cautions = []string{CautionNoBuy, CautionRain, CautionWind, CautionGear, CautionLabel}
				if r.PHIDays != nil {
					o.LastSpray = x.harvest.AddDate(0, 0, -*r.PHIDays).Format(agro.DateLayout)
					if w.From != "" && o.LastSpray < w.From {
						o.Cautions = append(o.Cautions, fmt.Sprintf("Its waiting period is too long for this crop when this problem usually shows up: it may not be sprayed after %s, so use another option.", o.LastSpray))
					}
				} else if p.Synthetic {
					o.Cautions = append(o.Cautions, cautionNoPHIs)
				}
				w.Options = append(w.Options, o)
				if r.DAS != nil {
					x.schedule(pr, r, p)
				}
			}
		}
		if len(w.Options) == 0 {
			w.Options = append(w.Options, Option{Route: string(x.method), Text: NoRemedy, Ref: pr.SignsRef})
		}
		x.out.watch = append(x.out.watch, w)
	}
	return nil
}

// schedule puts a preventive spray on the schedule, unless it is
// synthetic with no known PHI or would fall closer to harvest than its
// PHI.
func (x *fieldCtx) schedule(pr Problem, r Remedy, p Product) {
	day := x.sow.AddDate(0, 0, *r.DAS)
	switch {
	case p.Synthetic && r.PHIDays == nil:
		return
	case r.PHIDays != nil && day.After(x.harvest.AddDate(0, 0, -*r.PHIDays)):
		x.out.notes = append(x.out.notes, fmt.Sprintf("%s: the preventive %s spray against %s is left out: on %s it would be within its %d-day waiting period before harvest.",
			x.crop.Name, p.Name, strings.ToLower(pr.Name), day.Format(agro.DateLayout), *r.PHIDays))
		return
	case day.After(x.end):
		return
	}
	t := x.task("spray-"+pr.ID, day, "spray", false)
	t.Title = "Spray against " + strings.ToLower(pr.Name)
	t.WaterPerAcre, t.PHIDays = water(r), r.PHIDays
	t.Cautions = addCaution(t.Cautions, CautionRain, CautionWind, CautionGear, CautionLabel)
	qty := x.sprayQty(r)
	if qty == 0 {
		t.Doses = append(t.Doses, Dose{Product: p.ID, Name: p.Name, Synthetic: p.Synthetic, Unit: p.Unit, Rate: rate(r, p.Unit), Note: r.Text})
		t.Refs = addRef(addRef(t.Refs, r.Ref), p.Ref)
		return
	}
	x.dose(t, p, qty/x.ha, r.Text, []Ref{r.Ref}, false)
	t.Doses[len(t.Doses)-1].Rate = rate(r, p.Unit)
}

// span returns the first and last day the crop is in any of the stages,
// within the plan's window.
func (x *fieldCtx) span(stages []string) (from, to string) {
	for d := x.sow; !d.After(x.end); d = d.AddDate(0, 0, 1) {
		if slices.Contains(stages, calendar.StageAt(x.crop, x.sow, d).Name) {
			if from == "" {
				from = d.Format(agro.DateLayout)
			}
			to = d.Format(agro.DateLayout)
		}
	}
	return from, to
}

// shop adds the needs of all fields up by product, before sowing and
// later, and rounds each total up to what is sold.
func (b *Book) shop(needs []need) (before, later []Item) {
	before, later = []Item{}, []Item{}
	type key struct {
		product string
		before  bool
	}
	items := map[key]*Item{}
	var order []key
	for _, n := range needs {
		k := key{n.product, n.before}
		it, ok := items[k]
		if !ok {
			it = &Item{Product: n.product, Name: n.name, Unit: n.unit, First: n.date, For: []string{}, Refs: []Ref{}}
			if p, ok := b.products[n.product]; ok {
				it.Name, it.Unit, it.Synthetic, it.Make = p.Name, p.Unit, p.Synthetic, p.Make
				it.Refs = append(it.Refs, p.Ref)
				if p.PackRef != nil {
					it.Refs = append(it.Refs, *p.PackRef)
				}
			}
			items[k] = it
			order = append(order, k)
		}
		it.Need += n.qty
		it.First = min(it.First, n.date)
		it.Default = it.Default || n.dflt
		if !slices.Contains(it.For, n.label) {
			it.For = append(it.For, n.label)
		}
		for _, r := range n.refs {
			it.Refs = addRef(it.Refs, r)
		}
	}
	for _, k := range order {
		it := items[k]
		p := b.products[it.Product]
		it.Qty, it.Packs = Sold(p, it.Need, it.Unit)
		if it.Packs > 0 {
			it.Pack = p.PackName
		}
		it.Need = round2(it.Need)
		if k.before {
			before = append(before, *it)
		} else {
			later = append(later, *it)
		}
	}
	by := func(a, b Item) int {
		return cmp.Or(strings.Compare(a.First, b.First), strings.Compare(a.Product, b.Product))
	}
	slices.SortFunc(before, by)
	slices.SortFunc(later, by)
	return before, later
}

// Sold rounds a need up to what is sold: whole packs when the product
// comes in packs, else whole kilograms, 100 g or 100 ml steps, whole
// litres, half tonnes or whole pieces (seed setts, cuttings). unit is the
// need's unit, the product's when p is empty.
func Sold(p Product, need float64, unit string) (qty float64, packs int) {
	up := func(v, step float64) float64 { return math.Ceil(v/step-1e-9) * step }
	if p.Pack > 0 {
		packs = int(math.Ceil(need/p.Pack - 1e-9))
		return float64(packs) * p.Pack, packs
	}
	step := map[string]float64{"t": 0.5, "kg": 1, "g": 100, "l": 1, "ml": 100}[cmpOr(p.Unit, unit)]
	if step == 0 {
		step = 1
	}
	return round2(up(need, step)), 0
}
