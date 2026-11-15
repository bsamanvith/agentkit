package addon

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/calendar"
	"github.com/bsamanvith/agentkit/apps/farm/cycle"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/inputs"
	"github.com/bsamanvith/agentkit/apps/farm/region"
	"github.com/bsamanvith/agentkit/apps/farm/work"
)

// The addons' names: the keys of cycle.Position.Addons.
const (
	InputsName = "inputs"
	TimingName = "timing"
)

// Field is one crop on one area of a cycle, in the order of the inputs
// plan's fields (an intercropped area is two fields).
type Field struct {
	// Area is the area's index in the cycle's Areas.
	Area     int     `json:"area"`
	Crop     string  `json:"crop"`
	CropName string  `json:"crop_name"`
	Hectares float64 `json:"hectares"`
	Acres    float64 `json:"acres"`
	Sow      string  `json:"sow"`
}

// InputsPlan is the inputs addon's answer for one cycle: the plan of
// package inputs for the cycle's areas, method and the plot's water, with
// the fields it was made for.
type InputsPlan struct {
	Fields []Field `json:"fields"`
	// Acres is the area the plan covers.
	Acres float64 `json:"acres"`
	inputs.Plan
}

// Packs gives the region pack for a plot's place: the inputs book and
// crop library that hold there. *planner.Planner is one.
type Packs interface {
	Pack(f farm.Farm) *region.Pack
}

// Option configures an addon.
type Option func(*regional)

// WithPacks takes each plot's book and library (and, for timing, its
// storage values) from its region pack; without it, or when it has none
// for a plot, the addon's own are used.
func WithPacks(p Packs) Option { return func(r *regional) { r.packs = p } }

type regional struct{ packs Packs }

func (r regional) pack(f farm.Farm) *region.Pack {
	if r.packs == nil {
		return nil
	}
	return r.packs.Pack(f)
}

// data returns the book and library for a plot.
func (r regional) data(f farm.Farm, book *inputs.Book, lib *agro.Library) (*inputs.Book, *agro.Library) {
	if p := r.pack(f); p != nil {
		return p.Book(), p.Library()
	}
	return book, lib
}

func regionalOf(opts []Option) regional {
	var r regional
	for _, o := range opts {
		o(&r)
	}
	return r
}

// Inputs is the cycle addon that plans what to buy and when to feed and
// spray, for each planned cycle.
type Inputs struct {
	book *inputs.Book
	lib  *agro.Library
	regional
}

// NewInputs returns the inputs addon over a book checked against lib.
func NewInputs(book *inputs.Book, lib *agro.Library, opts ...Option) *Inputs {
	return &Inputs{book: book, lib: lib, regional: regionalOf(opts)}
}

// Name implements cycle.Addon.
func (a *Inputs) Name() string { return InputsName }

// For implements cycle.Addon: nil for a cycle with no area to plan.
func (a *Inputs) For(_ context.Context, f farm.Farm, c cycle.Cycle) (any, error) {
	book, lib := a.data(f, a.book, a.lib)
	var req inputs.Request
	var fields []Field
	for i, ar := range c.Areas {
		z := agro.Zone{Crop: ar.Crop, With: ar.With}
		if ar.Zone >= 0 && ar.Zone < len(c.Plan.Zones) {
			z.Pattern = c.Plan.Zones[ar.Zone].Pattern
		}
		for _, p := range parts(z, ar.AreaHa) {
			name := p.crop
			if cr, err := lib.Crop(p.crop); err == nil {
				name = cr.Name
			}
			req.Fields = append(req.Fields, inputs.Field{Crop: p.crop, Hectares: p.ha, Sow: ar.Sow})
			fields = append(fields, Field{Area: i, Crop: p.crop, CropName: name, Hectares: round(p.ha, 4), Acres: round(p.ha*inputs.AcresPerHectare, 2), Sow: ar.Sow})
		}
	}
	if len(req.Fields) == 0 {
		return nil, nil
	}
	req.Method, req.Water = inputs.Method(c.Method), WaterOf(f)
	if req.Method == "" {
		req.Method = inputs.Conventional
	}
	p, err := book.Plan(req)
	if err != nil {
		return nil, err
	}
	out := InputsPlan{Fields: fields, Plan: p}
	for _, fl := range fields {
		out.Acres += fl.Acres
	}
	out.Acres = round(out.Acres, 2)
	return out, nil
}

// WaterOf is how the plot is watered: irrigated when its profile names a
// water source, as the season roadmap reads it.
func WaterOf(f farm.Farm) inputs.Water {
	if f.Profile != nil && len(f.Profile.Water) > 0 {
		return inputs.Irrigated
	}
	return inputs.Rainfed
}

type part struct {
	crop string
	ha   float64
	with bool
}

// parts splits an area between its crop and its intercrop by the rows of
// each (half each when the rows are not given); an area of no size has
// none.
func parts(z agro.Zone, ha float64) []part {
	if !(ha > 0) || z.Crop == "" {
		return nil
	}
	if z.With == "" {
		return []part{{crop: z.Crop, ha: ha}}
	}
	a, b := float64(z.Pattern[0]), float64(z.Pattern[1])
	if a <= 0 || b <= 0 {
		a, b = 1, 1
	}
	return []part{{crop: z.Crop, ha: ha * a / (a + b)}, {crop: z.With, ha: ha * b / (a + b), with: true}}
}

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

// Methods reads the method a plot's current cycle is grown by: the cycle
// store's choice at position 0, conventional when none.
type Methods interface {
	Get(ctx context.Context, farmID string) (cycle.Record, error)
}

// Roadmap is the season roadmap's source of the inputs schedule: the
// feeding and spray tasks of the plot's saved plan (its current cycle).
// An entry on a calendar task (sowing, a top-dressing, the seed treatment)
// enriches that task with the plan's products and quantities; the others
// (a feed on day 45, a preventive spray, jeevamrutham) are tasks of their
// own, keyed as the calendar keys its tasks, so a moved sowing keeps them
// and their events.
type Roadmap struct {
	book    *inputs.Book
	lib     *agro.Library
	methods Methods
	regional
}

// NewRoadmap returns the roadmap source over a book checked against lib
// and the cycles' store (for the method; nil for conventional always).
func NewRoadmap(book *inputs.Book, lib *agro.Library, methods Methods, opts ...Option) *Roadmap {
	return &Roadmap{book: book, lib: lib, methods: methods, regional: regionalOf(opts)}
}

// Tasks implements work.TaskSource.
func (r *Roadmap) Tasks(ctx context.Context, f farm.Farm, zones []work.Zone) ([]work.Task, []string, error) {
	if f.Plan == nil || len(zones) == 0 {
		return nil, nil, nil
	}
	book, lib := r.data(f, r.book, r.lib)
	method := inputs.Conventional
	if r.methods != nil {
		rec, err := r.methods.Get(ctx, f.ID)
		if err != nil {
			return nil, nil, err
		}
		if rec.Slots[0].Method == cycle.Organic {
			method = inputs.Organic
		}
	}
	type fieldOf struct {
		zone    int
		crop    string // the zone's crop, for the task's ID
		planned string // the zone's planned sowing, for the task's ID
		shift   int    // planned less real sowing, in days, for work before sowing
		with    bool
	}
	var req inputs.Request
	var of []fieldOf
	for _, z := range zones {
		if z.Zone < 0 || z.Zone >= len(f.Plan.Zones) {
			continue
		}
		pz := f.Plan.Zones[z.Zone]
		ha := 0.0
		if len(pz.Ring.Open()) >= 3 {
			ha = geo.Area(pz.Ring) / 10000
		}
		sow, shift := z.PlannedSow, 0
		if z.Sown != "" && z.Sown != z.PlannedSow && !perennial(lib, z.Crop) {
			// After the sowing, work follows the real date, as the
			// calendar's does; up to it, the planned date stands.
			sow, shift = z.Sown, days(z.Sown, z.PlannedSow)
		}
		for _, p := range parts(pz, ha) {
			req.Fields = append(req.Fields, inputs.Field{Crop: p.crop, Hectares: p.ha, Sow: sow})
			of = append(of, fieldOf{zone: z.Zone, crop: pz.Crop, planned: z.PlannedSow, shift: shift, with: p.with})
		}
	}
	if len(req.Fields) == 0 {
		return nil, nil, nil
	}
	req.Method, req.Water = method, WaterOf(f)
	p, err := book.Plan(req)
	if err != nil {
		return nil, nil, err
	}
	var out []work.Task
	for _, t := range p.Schedule {
		if t.Field < 0 || t.Field >= len(of) {
			continue
		}
		fo := of[t.Field]
		key := t.Key
		if fo.with {
			key = "with-" + key // the calendar's key for an intercrop's own work
		}
		date := t.Date
		if t.DAS <= 0 && fo.shift != 0 {
			date = addDays(t.Date, fo.shift)
		}
		das := t.DAS
		out = append(out, work.Task{
			ID: work.TaskID(fo.zone, fo.crop, fo.planned, key), Source: work.FromInputs, Zone: fo.zone, Crop: t.Crop,
			Kind: kindOf(t.Kind), Title: t.Title, Detail: Detail(t), Date: date, DAS: &das, Stage: t.Stage,
		})
	}
	return out, []string{inputs.StarterNote, inputs.LabelNote}, nil
}

// perennial: a perennial's work runs by the calendar month, so it keeps
// its planned dates when the planting moves, as on the roadmap.
func perennial(lib *agro.Library, crop string) bool {
	c, err := lib.Crop(crop)
	return err == nil && c.Perennial
}

func timeOf(day string) (time.Time, error) { return time.Parse(agro.DateLayout, day) }

func days(from, to string) int {
	a, err1 := timeOf(from)
	b, err2 := timeOf(to)
	if err1 != nil || err2 != nil {
		return 0
	}
	return int(math.Round(b.Sub(a).Hours() / 24))
}

func addDays(day string, n int) string {
	t, err := timeOf(day)
	if err != nil {
		return day
	}
	return t.AddDate(0, 0, n).Format(agro.DateLayout)
}

// kindOf is the roadmap's kind of an inputs schedule entry.
func kindOf(k string) string {
	switch k {
	case "feed":
		return calendar.Fertilise
	case "seed-treatment":
		return calendar.Prepare
	}
	return calendar.Protect
}

// Detail says a schedule entry in a line for the roadmap: each product
// with its quantity for the area and per acre (or per plant), the water,
// the wait before harvest and the cautions (the label's own is in the
// roadmap's notes).
func Detail(t inputs.Task) string {
	var b strings.Builder
	for _, d := range t.Doses {
		if b.Len() > 0 {
			b.WriteString("; ")
		}
		b.WriteString(d.Name)
		switch {
		case d.Qty > 0 && d.PerPlant > 0:
			fmt.Fprintf(&b, " %s (%s a plant)", Amount(d.Qty, d.Unit), Amount(d.PerPlant, d.Unit))
		case d.Qty > 0:
			fmt.Fprintf(&b, " %s (%s an acre)", Amount(d.Qty, d.Unit), Amount(d.PerAcre, d.Unit))
		case d.Rate != "":
			fmt.Fprintf(&b, " at %s", d.Rate)
		}
	}
	if b.Len() > 0 {
		b.WriteString(".")
	}
	if t.WaterPerAcre != "" {
		fmt.Fprintf(&b, " In %s of water an acre.", t.WaterPerAcre)
	}
	if t.PHIDays != nil {
		fmt.Fprintf(&b, " Wait %d days after it before harvest.", *t.PHIDays)
	}
	for _, c := range t.Cautions {
		if c != inputs.CautionLabel {
			b.WriteString(" " + c)
		}
	}
	return strings.TrimSpace(b.String())
}

// Amount says a quantity as it is bought: grams and millilitres from a
// thousand on in kilograms and litres, whole numbers from ten on, one
// decimal below.
func Amount(v float64, unit string) string {
	switch {
	case unit == "g" && v >= 1000:
		v, unit = v/1000, "kg"
	case unit == "ml" && v >= 1000:
		v, unit = v/1000, "l"
	}
	var n string
	if v >= 10 {
		n = strconv.FormatFloat(math.Round(v), 'f', -1, 64)
	} else {
		n = strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64)
	}
	switch unit {
	case "piece":
		unit = "pieces"
		if n == "1" {
			unit = "piece"
		}
	case "t":
		unit = "tonnes"
		if n == "1" {
			unit = "tonne"
		}
	case "l":
		unit = "litres"
		if n == "1" {
			unit = "litre"
		}
	}
	return n + " " + unit
}

// Label names the addon for the farmer, in a note when it cannot answer.
func (a *Inputs) Label() string { return "what to buy and the spray schedule" }
