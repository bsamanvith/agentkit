package inputs

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
)

// Ref says where a value was read: a source's id and the page or section.
type Ref struct {
	Source string `json:"source"`
	Where  string `json:"where"`
}

// Source is a document the data was read from.
type Source struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Publisher string `json:"publisher"`
	URL       string `json:"url"`
	// Read is the day it was read, YYYY-MM-DD.
	Read string `json:"read"`
}

// Product is something a farmer buys or makes: a fertiliser, a manure, a
// biofertiliser, a trap or a pesticide.
type Product struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Kind is fertiliser, amendment, manure, biofertiliser, biocontrol,
	// botanical, mineral, trap or pesticide.
	Kind string `json:"kind"`
	// Synthetic products are never in the organic route.
	Synthetic bool `json:"synthetic"`
	// NPOP is how India's organic standard (NPOP Annex 3) lists a
	// non-synthetic input: permitted or restricted (allowed when needed,
	// with the certifier's agreement).
	NPOP    string `json:"npop,omitempty"`
	NPOPRef *Ref   `json:"npop_ref,omitempty"`
	// N, P, K and S are its nutrient content in per cent: N, P2O5, K2O, S.
	N float64 `json:"n,omitempty"`
	P float64 `json:"p,omitempty"`
	K float64 `json:"k,omitempty"`
	S float64 `json:"s,omitempty"`
	// Unit is what quantities of it are in: t, kg, g, l, ml or piece.
	Unit string `json:"unit"`
	// Pack is the size it is sold in, in Unit (45 for a 45 kg bag); 0
	// when it is sold loose, by the unit.
	Pack     float64 `json:"pack,omitempty"`
	PackName string  `json:"pack_name,omitempty"`
	PackRef  *Ref    `json:"pack_ref,omitempty"`
	// Make is true for what a farmer makes or gets locally rather than
	// buys in a shop (farmyard manure, cow dung, bird perches).
	Make bool `json:"make,omitempty"`
	// Active is a pesticide's active ingredient(s), "a + b" for a mix.
	Active string `json:"active,omitempty"`
	Ref    Ref    `json:"ref"`
}

// NPK is a recommended dose: kg per hectare of N, P2O5, K2O and S, or
// grams per plant for a crop fed per plant.
type NPK struct {
	N   float64 `json:"n"`
	P   float64 `json:"p"`
	K   float64 `json:"k"`
	S   float64 `json:"s,omitempty"`
	Ref Ref     `json:"ref"`
}

// Split is the share of a crop's N, P2O5 and K2O (per cent of each)
// applied at one time, given as a calendar task key (At), days after
// sowing (DAS), the first day of a calendar stage (Stage), or, for a
// perennial, a month of its first year (Month).
type Split struct {
	At    string  `json:"at,omitempty"`
	DAS   *int    `json:"das,omitempty"`
	Stage string  `json:"stage,omitempty"`
	Month int     `json:"month,omitempty"`
	N     float64 `json:"n"`
	P     float64 `json:"p"`
	K     float64 `json:"k"`
}

// Routes an input belongs to.
const (
	RouteBoth         = "both"
	RouteOrganic      = "organic"
	RouteConventional = "conventional"
)

// Use is a product applied at a time: Qty per hectare (per plant for a
// crop fed per plant) in Unit, or the product's unit when Unit is empty.
type Use struct {
	Product string  `json:"product"`
	Qty     float64 `json:"qty"`
	Unit    string  `json:"unit,omitempty"`
	At      string  `json:"at,omitempty"`
	DAS     *int    `json:"das,omitempty"`
	Route   string  `json:"route,omitempty"`
	Note    string  `json:"note,omitempty"`
	Ref     Ref     `json:"ref"`
}

// SeedRate is the seed or planting material per hectare.
type SeedRate struct {
	Qty  float64 `json:"qty"`
	Unit string  `json:"unit"`
	What string  `json:"what"`
	Ref  Ref     `json:"ref"`
}

// SeedTreatment is a product per kilogram of seed, in the product's unit.
type SeedTreatment struct {
	Product   string  `json:"product"`
	PerKgSeed float64 `json:"per_kg_seed"`
	Route     string  `json:"route"`
	Ref       Ref     `json:"ref"`
}

// Remedy is one way to treat a pest or disease: a product at a dose per
// hectare, per litre of spray or per tree (Per), with the spray volume
// and the PHI when the source gives them; or only a Text.
type Remedy struct {
	Product string      `json:"product,omitempty"`
	Dose    agro.Range  `json:"dose,omitempty"`
	Per     string      `json:"per,omitempty"` // ha, l or tree
	WaterLH *agro.Range `json:"water_l_ha,omitempty"`
	PHIDays *int        `json:"phi_days,omitempty"`
	// DAS, when set, schedules it as a preventive spray on that day.
	DAS  *int   `json:"das,omitempty"`
	Text string `json:"text,omitempty"`
	Ref  Ref    `json:"ref"`
}

// Prevent is a preventive measure: a product (Qty per Per, "ha" (the
// default) or "tree" on a crop fed per plant, applied at At or DAS) or
// only a Text.
type Prevent struct {
	Product string  `json:"product,omitempty"`
	Qty     float64 `json:"qty,omitempty"`
	Per     string  `json:"per,omitempty"`
	At      string  `json:"at,omitempty"`
	DAS     *int    `json:"das,omitempty"`
	Note    string  `json:"note,omitempty"`
	Text    string  `json:"text,omitempty"`
	Ref     Ref     `json:"ref"`
}

// Problem is a pest or disease of a crop.
type Problem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"` // pest or disease
	// Stages are the calendar stages it shows up in.
	Stages       []string  `json:"stages"`
	Signs        string    `json:"signs"`
	SignsRef     Ref       `json:"signs_ref"`
	Threshold    string    `json:"threshold,omitempty"`
	ThresholdRef *Ref      `json:"threshold_ref,omitempty"`
	Prevent      []Prevent `json:"prevent"`
	Organic      []Remedy  `json:"organic"`
	Conventional []Remedy  `json:"conventional"`
}

// Crop is the data of one crop.
type Crop struct {
	Crop string `json:"crop"`
	// PerPlant crops (perennials) are fed per plant: NPK in grams per
	// plant for the first year after planting, uses per plant.
	PerPlant    bool      `json:"per_plant,omitempty"`
	Seed        *SeedRate `json:"seed,omitempty"`
	SeedRainfed *SeedRate `json:"seed_rainfed,omitempty"`
	Rainfed     *NPK      `json:"rainfed,omitempty"`
	Irrigated   *NPK      `json:"irrigated,omitempty"`
	// Mature is a perennial's dose once grown, for reference: a plan
	// covers the first year only.
	Mature        *NPK            `json:"mature,omitempty"`
	PProduct      string          `json:"p_product,omitempty"` // dap (the default) or ssp
	Splits        []Split         `json:"splits,omitempty"`
	SplitsRainfed []Split         `json:"splits_rainfed,omitempty"`
	Add           []Use           `json:"add,omitempty"`
	Organic       []Use           `json:"organic,omitempty"`
	SeedTreatment []SeedTreatment `json:"seed_treatment,omitempty"`
	Protect       []Problem       `json:"protect,omitempty"`
}

// Jeevamrutham is the fermented cow-dung brew of the organic route.
type Jeevamrutham struct {
	EveryDays     int     `json:"every_days"`
	Until         string  `json:"until"` // stage:NAME, the stage it stops at
	LitresPerAcre float64 `json:"litres_per_acre"`
	// Batch is what goes into LitresPerAcre litres.
	Batch []struct {
		Product string  `json:"product"`
		Qty     float64 `json:"qty"`
	} `json:"batch"`
	Note string `json:"note"`
	Ref  Ref    `json:"ref"`
}

// Banned is an active ingredient banned, withdrawn, refused, restricted
// or proposed for a ban or restriction in India.
type Banned struct {
	Name   string   `json:"name"`
	Aka    []string `json:"aka"`
	Status string   `json:"status"`
	Note   string   `json:"note"`
	Ref    Ref      `json:"ref"`
}

// BannedList is data/banned.json.
type BannedList struct {
	Note    string   `json:"note"`
	AsOf    string   `json:"as_of"`
	Checked string   `json:"checked"`
	Actives []Banned `json:"actives"`
}

type dataFile struct {
	Note     string    `json:"note"`
	Sources  []Source  `json:"sources"`
	Products []Product `json:"products"`
	Kinds    []struct {
		Kind string `json:"kind"`
		Like string `json:"like"`
	} `json:"kinds"`
	Jeevamrutham   Jeevamrutham `json:"jeevamrutham"`
	DefaultOrganic []Use        `json:"default_organic"`
	Crops          []Crop       `json:"crops"`
}

// Book is the inputs data, checked against a crop library. It is
// read-only and safe for concurrent use.
type Book struct {
	lib      *agro.Library
	data     dataFile
	banned   BannedList
	sources  map[string]Source
	products map[string]Product
	crops    map[string]Crop
	kinds    map[string]string
}

//go:embed data/inputs.json data/banned.json
var files embed.FS

// Default returns the book built into the app, checked against lib.
func Default(lib *agro.Library) (*Book, error) {
	d, b, err := DefaultJSON()
	if err != nil {
		return nil, err
	}
	return Load(lib, d, b)
}

// DefaultJSON returns the JSON [Default] is built from, the data and the
// banned list, for a region pack to lay its overlays on.
func DefaultJSON() (data, banned []byte, err error) {
	if data, err = files.ReadFile("data/inputs.json"); err != nil {
		return nil, nil, err
	}
	if banned, err = files.ReadFile("data/banned.json"); err != nil {
		return nil, nil, err
	}
	return data, banned, nil
}

// Load builds a book from the JSON of the data and the banned list and
// checks it: every reference names a known source, every product and crop
// is known, the organic route holds no synthetic product, no active
// ingredient is on the banned list, splits add up to 100 % and every
// calendar key resolves for every crop of lib. Errors wrap agro.ErrInvalid.
func Load(lib *agro.Library, dataJSON, bannedJSON []byte) (*Book, error) {
	b := &Book{lib: lib, sources: map[string]Source{}, products: map[string]Product{}, crops: map[string]Crop{}, kinds: map[string]string{}}
	if err := json.Unmarshal(dataJSON, &b.data); err != nil {
		return nil, fmt.Errorf("%w: inputs: %w", agro.ErrInvalid, err)
	}
	if err := json.Unmarshal(bannedJSON, &b.banned); err != nil {
		return nil, fmt.Errorf("%w: banned list: %w", agro.ErrInvalid, err)
	}
	if err := b.check(); err != nil {
		return nil, err
	}
	return b, nil
}

var units = []string{"t", "kg", "g", "l", "ml", "piece"}

func (b *Book) check() error {
	var errs []error
	bad := func(format string, a ...any) {
		errs = append(errs, fmt.Errorf("%w: inputs: "+format, append([]any{agro.ErrInvalid}, a...)...))
	}
	for _, s := range b.data.Sources {
		if s.ID == "" || s.Name == "" || s.URL == "" || s.Read == "" {
			bad("source %q: needs id, name, url and the day it was read", s.ID)
		}
		if _, dup := b.sources[s.ID]; dup {
			bad("source %q: repeated", s.ID)
		}
		b.sources[s.ID] = s
	}
	ref := func(what string, r Ref) {
		if _, ok := b.sources[r.Source]; !ok || r.Where == "" {
			bad("%s: reference %+v names no known source or no place in it", what, r)
		}
	}
	banned := b.bannedNames()
	isBanned := func(text string) string {
		t := " " + normal(text) + " "
		for _, n := range banned {
			if strings.Contains(t, " "+n+" ") {
				return n
			}
		}
		return ""
	}
	for _, p := range b.data.Products {
		if p.ID == "" || p.Name == "" {
			bad("product %q: needs id and name", p.ID)
		}
		if _, dup := b.products[p.ID]; dup {
			bad("product %q: repeated", p.ID)
		}
		b.products[p.ID] = p
		ref("product "+p.ID, p.Ref)
		if p.PackRef != nil {
			ref("product "+p.ID+" pack", *p.PackRef)
		}
		if p.NPOPRef != nil {
			ref("product "+p.ID+" npop", *p.NPOPRef)
		}
		if !slices.Contains(units, p.Unit) {
			bad("product %s: unit %q", p.ID, p.Unit)
		}
		if p.Pack > 0 && (p.PackName == "" || p.PackRef == nil) {
			bad("product %s: a pack size needs its name and a source", p.ID)
		}
		if !p.Synthetic && p.NPOP != "permitted" && p.NPOP != "restricted" {
			bad("product %s: not synthetic, so it needs its NPOP class", p.ID)
		}
		if p.Kind == "pesticide" && p.Active == "" {
			bad("product %s: a pesticide needs its active ingredient", p.ID)
		}
		for _, s := range []string{p.Active, p.Name} {
			if n := isBanned(s); n != "" {
				bad("product %s: %q is banned or restricted in India", p.ID, n)
			}
		}
	}
	// The fertilisers nutrition() turns doses into, by the nutrient it
	// divides by.
	for id, content := range map[string]func(Product) float64{
		"urea":   func(p Product) float64 { return p.N },
		"dap":    func(p Product) float64 { return p.P },
		"ssp":    func(p Product) float64 { return p.P },
		"mop":    func(p Product) float64 { return p.K },
		"gypsum": func(p Product) float64 { return p.S },
	} {
		if p, ok := b.products[id]; !ok || !(content(p) > 0) || p.N < 0 {
			bad("product %s: the fertiliser plans are made with it: it must be there, with its nutrient above 0", id)
		}
	}
	product := func(what, id string) (Product, bool) {
		p, ok := b.products[id]
		if !ok {
			bad("%s: unknown product %q", what, id)
		}
		return p, ok
	}
	route := func(what, r, id string) {
		p, ok := product(what, id)
		switch {
		case !slices.Contains([]string{RouteBoth, RouteOrganic, RouteConventional}, r):
			bad("%s: route %q", what, r)
		case ok && p.Synthetic && r != RouteConventional:
			bad("%s: %s is synthetic, so it can only be in the conventional route", what, id)
		}
	}
	for _, k := range b.data.Kinds {
		b.kinds[k.Kind] = k.Like
	}
	ref("jeevamrutham", b.data.Jeevamrutham.Ref)
	for _, x := range b.data.Jeevamrutham.Batch {
		route("jeevamrutham", RouteOrganic, x.Product)
	}
	for _, u := range b.data.DefaultOrganic {
		ref("default organic", u.Ref)
		route("default organic", RouteOrganic, u.Product)
	}
	for _, c := range b.data.Crops {
		lc, err := b.lib.Crop(c.Crop)
		if err != nil {
			bad("crop %q: not in the crop library", c.Crop)
			continue
		}
		if _, dup := b.crops[c.Crop]; dup {
			bad("crop %q: repeated", c.Crop)
		}
		b.crops[c.Crop] = c
		if c.PerPlant != lc.Perennial {
			bad("crop %s: per_plant must match the library's perennial", c.Crop)
		}
		what := "crop " + c.Crop
		for _, s := range []*SeedRate{c.Seed, c.SeedRainfed} {
			if s != nil {
				ref(what+" seed", s.Ref)
			}
		}
		for _, n := range []*NPK{c.Rainfed, c.Irrigated, c.Mature} {
			if n != nil {
				ref(what+" nutrition", n.Ref)
			}
		}
		if !slices.Contains([]string{"", "dap", "ssp"}, c.PProduct) {
			bad("%s: p_product %q", what, c.PProduct)
		}
		for _, ss := range [][]Split{c.Splits, c.SplitsRainfed} {
			if len(ss) == 0 {
				continue
			}
			var n, p, k float64
			for _, s := range ss {
				n, p, k = n+s.N, p+s.P, k+s.K
			}
			for _, sum := range []float64{n, p, k} {
				if math.Abs(sum-100) > 0.1 && sum != 0 {
					bad("%s: splits add up to %.2f %%, not 100", what, sum)
				}
			}
		}
		if (c.Rainfed != nil || c.Irrigated != nil) && len(c.Splits) == 0 {
			bad("%s: nutrition without splits", what)
		}
		for _, u := range slices.Concat(c.Add, c.Organic) {
			ref(what+" "+u.Product, u.Ref)
			route(what+" "+u.Product, cmpOr(u.Route, RouteBoth), u.Product)
			if p, ok := b.products[u.Product]; ok && u.Unit != "" && factor(u.Unit, p.Unit) == 0 {
				bad("%s %s: unit %q does not convert to %q", what, u.Product, u.Unit, p.Unit)
			}
			if u.Qty <= 0 {
				bad("%s %s: quantity must be positive", what, u.Product)
			}
		}
		for _, s := range c.SeedTreatment {
			ref(what+" seed treatment", s.Ref)
			route(what+" seed treatment", s.Route, s.Product)
		}
		for _, pr := range c.Protect {
			pw := what + " " + pr.ID
			ref(pw+" signs", pr.SignsRef)
			if pr.ThresholdRef != nil {
				ref(pw+" threshold", *pr.ThresholdRef)
			}
			if pr.Signs == "" || (pr.Kind != "pest" && pr.Kind != "disease") || len(pr.Stages) == 0 {
				bad("%s: needs signs, a kind and stages", pw)
			}
			for _, p := range pr.Prevent {
				ref(pw+" prevent", p.Ref)
				if p.Product != "" {
					route(pw+" prevent", RouteBoth, p.Product)
					if !slices.Contains([]string{"", "ha", "tree"}, p.Per) || (p.Per == "tree" && !c.PerPlant) {
						bad("%s prevent %s: per %q: want ha, or tree on a crop fed per plant", pw, p.Product, p.Per)
					}
				}
				if n := isBanned(p.Text + " " + p.Note); n != "" {
					bad("%s: prevention names %q, banned or restricted in India", pw, n)
				}
			}
			for i, rs := range [][]Remedy{pr.Organic, pr.Conventional} {
				for _, r := range rs {
					ref(pw+" remedy", r.Ref)
					if n := isBanned(r.Text); n != "" {
						bad("%s: remedy names %q, banned or restricted in India", pw, n)
					}
					if r.Product == "" {
						if r.Text == "" {
							bad("%s: a remedy needs a product or a text", pw)
						}
						continue
					}
					p, ok := product(pw, r.Product)
					if ok && i == 0 && p.Synthetic {
						bad("%s: organic remedy %s is synthetic", pw, r.Product)
					}
					if !(r.Dose[0] > 0 && r.Dose[0] <= r.Dose[1]) || !slices.Contains([]string{"ha", "l", "tree"}, r.Per) {
						bad("%s %s: dose %v per %q", pw, r.Product, r.Dose, r.Per)
					}
				}
			}
		}
	}
	for _, lc := range b.lib.Crops() {
		if _, ok := b.crops[lc.ID]; ok {
			continue
		}
		like, ok := b.crops[b.kinds[lc.Kind]]
		if !ok {
			bad("crop %s: no data and no crop of its kind %q to default to", lc.ID, lc.Kind)
			continue
		}
		if like.PerPlant != lc.Perennial {
			bad("crop %s: defaults to %s, which is not perennial like it", lc.ID, like.Crop)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	// Every crop's times resolve on its calendar, both ways of watering.
	sow := time.Date(2026, time.June, 15, 0, 0, 0, 0, time.UTC)
	for _, lc := range b.lib.Crops() {
		for _, irrigated := range []bool{false, true} {
			if _, err := b.field(Field{Crop: lc.ID, Hectares: 1, Sow: sow.Format(agro.DateLayout)}, 0, Conventional, irrigated); err != nil {
				bad("crop %s: %v", lc.ID, err)
			}
		}
	}
	return errors.Join(errs...)
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// normal lower-cases s and turns everything but letters and digits into
// single spaces, so names compare whatever their punctuation.
func normal(s string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	}), " ")
}

func (b *Book) bannedNames() []string {
	var out []string
	for _, a := range b.banned.Actives {
		for _, n := range append([]string{a.Name}, a.Aka...) {
			if n := normal(n); n != "" {
				out = append(out, n)
			}
		}
	}
	return out
}

// Banned returns the list of actives kept out of the data, with its
// source and the day it was checked.
func (b *Book) Banned() BannedList { return b.banned }

// Product returns a product by id.
func (b *Book) Product(id string) (Product, bool) {
	p, ok := b.products[id]
	return p, ok
}

// Products returns every product, by id.
func (b *Book) Products() []Product {
	out := slices.Clone(b.data.Products)
	slices.SortFunc(out, func(a, b Product) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// factor converts a quantity in unit from to unit to; 0 when it cannot.
func factor(from, to string) float64 {
	type u struct {
		dim  string
		base float64
	}
	units := map[string]u{"t": {"mass", 1e6}, "kg": {"mass", 1e3}, "g": {"mass", 1}, "l": {"volume", 1e3}, "ml": {"volume", 1}, "piece": {"count", 1}}
	f, ok1 := units[from]
	t, ok2 := units[to]
	if !ok1 || !ok2 || f.dim != t.dim {
		return 0
	}
	return f.base / t.base
}
