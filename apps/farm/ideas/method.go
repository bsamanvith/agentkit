package ideas

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
)

//go:embed data/ideas.json
var bookJSON []byte

// Source is a document the book's numbers come from.
type Source struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Publisher string `json:"publisher"`
	URL       string `json:"url"`
	Read      string `json:"read"`
	Note      string `json:"note,omitempty"`
}

// Ref points into a Source.
type Ref struct {
	Source string `json:"source"`
	Where  string `json:"where"`
}

// Method is a way of growing a crop other than in the open field, as what
// it changes for the crop and what it costs.
type Method struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// What says it in a sentence, for the farmer.
	What string `json:"what"`
	// Kinds are the crop kinds it is built for (none: any; unused when
	// Crops is set); MaxHeightM the
	// tallest crop that fits under it (0: any); AnnualOnly leaves out
	// trees and other crops that stand for years.
	Kinds []string `json:"kinds,omitempty"`
	// Crops, when set, are the only crops it is suggested for: a costly
	// structure is built for the few crops that are grown under one.
	Crops      []string `json:"crops,omitempty"`
	MaxHeightM float64  `json:"max_height_m,omitempty"`
	AnnualOnly bool     `json:"annual_only,omitempty"`
	// TempMax and TempMin are how it moves the day's high and the night's
	// low, in °C.
	TempMax float64 `json:"temp_max,omitempty"`
	TempMin float64 `json:"temp_min,omitempty"`
	// Light is the share of sunlight it lets through (0: all of it).
	Light float64 `json:"light,omitempty"`
	// Shelter keeps the rain off: the crop lives on irrigation alone.
	Shelter bool `json:"shelter,omitempty"`
	// WaterSave is the share of the crop's water need it saves.
	WaterSave float64 `json:"water_save,omitempty"`
	// Drip puts the plot under drip; Drains takes standing water off the
	// roots; NoSoil takes the soil out of the question.
	Drip   bool `json:"drip,omitempty"`
	Drains bool `json:"drains,omitempty"`
	NoSoil bool `json:"no_soil,omitempty"`
	// NeedsWater: it cannot run on a rainfed plot.
	NeedsWater bool `json:"needs_water,omitempty"`
	// CostPerSqM or CostPerHa is the scheme's cost norm in rupees, Subsidy
	// the share assisted, MaxSqM the largest area assisted for one farmer.
	CostPerSqM float64 `json:"cost_per_sqm,omitempty"`
	CostPerHa  float64 `json:"cost_per_ha,omitempty"`
	Subsidy    float64 `json:"subsidy,omitempty"`
	MaxSqM     float64 `json:"max_sqm,omitempty"`
	// Skill is how much there is to learn: low, medium or high.
	Skill string `json:"skill"`
	Ref   *Ref   `json:"ref,omitempty"`
}

// Route is a way to sell a crop.
type Route struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	How  string `json:"how"`
	// Kinds are the crop kinds it takes (none: any), States where it
	// exists (none: everywhere), MSPOnly for crops with a support price.
	Kinds   []string `json:"kinds,omitempty"`
	States  []string `json:"states,omitempty"`
	MSPOnly bool     `json:"msp_only,omitempty"`
}

// Book is the ideas data: the methods, the ways to sell, and the plants
// people often ask about.
type Book struct {
	Sources []Source `json:"sources"`
	// Assumed says what in the book is a starter value.
	Assumed string   `json:"assumed"`
	Methods []Method `json:"methods"`
	// Asked are ids of the experimental tier's plants farmers often ask
	// about (apple, saffron, strawberry): always checked, wherever they
	// land.
	Asked  []string `json:"asked"`
	Routes []Route  `json:"routes"`
}

// DefaultBook returns the embedded book.
func DefaultBook() (*Book, error) { return LoadBook(bookJSON) }

// LoadBook reads a book and checks it: unknown fields, a method with no
// effect or a cost without its source, and references to nothing are
// errors.
func LoadBook(data []byte) (*Book, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var b Book
	if err := dec.Decode(&b); err != nil {
		return nil, fmt.Errorf("ideas: %w", err)
	}
	src := map[string]bool{}
	for _, s := range b.Sources {
		if s.ID == "" || s.Name == "" || s.URL == "" || s.Read == "" || src[s.ID] {
			return nil, fmt.Errorf("ideas: source %q: needs an id of its own, a name, a url and the day it was read", s.ID)
		}
		src[s.ID] = true
	}
	if b.Assumed == "" {
		return nil, fmt.Errorf("ideas: the book does not say what is assumed")
	}
	seen := map[string]bool{}
	for _, m := range b.Methods {
		switch {
		case m.ID == "" || m.Name == "" || m.What == "" || seen[m.ID]:
			return nil, fmt.Errorf("ideas: method %q: needs an id of its own, a name and what it is", m.ID)
		case !slices.Contains([]string{"low", "medium", "high"}, m.Skill):
			return nil, fmt.Errorf("ideas: method %s: skill %q", m.ID, m.Skill)
		case m.TempMax == 0 && m.TempMin == 0 && m.Light == 0 && !m.Shelter && m.WaterSave == 0 && !m.Drip && !m.Drains && !m.NoSoil:
			return nil, fmt.Errorf("ideas: method %s changes nothing", m.ID)
		case m.Light < 0 || m.Light > 1 || m.WaterSave < 0 || m.WaterSave >= 1 || m.Subsidy < 0 || m.Subsidy > 1 || m.TempMax < -8 || m.TempMax > 8 || m.TempMin < -8 || m.TempMin > 8:
			return nil, fmt.Errorf("ideas: method %s: a value out of range", m.ID)
		case (m.CostPerSqM > 0 || m.CostPerHa > 0) && (m.Ref == nil || !src[m.Ref.Source] || m.Ref.Where == ""):
			return nil, fmt.Errorf("ideas: method %s: a cost needs its source", m.ID)
		case m.CostPerSqM > 0 && m.CostPerHa > 0:
			return nil, fmt.Errorf("ideas: method %s: cost a square metre or a hectare, not both", m.ID)
		}
		seen[m.ID] = true
	}
	seen = map[string]bool{}
	for _, r := range b.Routes {
		if r.ID == "" || r.Name == "" || r.How == "" || seen[r.ID] {
			return nil, fmt.Errorf("ideas: route %q: needs an id of its own, a name and how it works", r.ID)
		}
		seen[r.ID] = true
	}
	return &b, nil
}

// Fits reports whether the method is one a crop can be grown under.
func (m Method) Fits(c agro.Crop) bool {
	switch {
	case len(m.Crops) > 0:
		return slices.Contains(m.Crops, c.ID)
	case len(m.Kinds) > 0 && !slices.Contains(m.Kinds, c.Kind):
		return false
	case m.AnnualOnly && c.Perennial:
		return false
	case m.MaxHeightM > 0 && c.HeightM > m.MaxHeightM:
		return false
	}
	return true
}

// Apply returns the crop and the plot as they are under the method: the
// climate moved, the rain kept off, the water need cut, the soil drained
// or out of the question.
func (m Method) Apply(c agro.Crop, r agro.Request) (agro.Crop, agro.Request) {
	for i := range r.Climate.Months {
		mo := &r.Climate.Months[i]
		mo.TempMax += m.TempMax
		mo.TempMin += m.TempMin
		mo.Temp += (m.TempMax + m.TempMin) / 2
		if m.Light > 0 {
			mo.SunMJ *= m.Light
			mo.ClearSunMJ *= m.Light
		}
		if m.Shelter {
			mo.RainMM = 0
		}
	}
	if m.WaterSave > 0 {
		c.WaterMM = agro.Range{c.WaterMM[0] * (1 - m.WaterSave), c.WaterMM[1] * (1 - m.WaterSave)}
	}
	if m.Shelter {
		// No rain to drown it either: its upper limit no longer binds.
		c.WaterMM[1] = max(c.WaterMM[1], 5000)
	}
	if m.Drip {
		r.Profile.Drip = true
	}
	if m.Drains && r.Soil != nil {
		soil := *r.Soil
		soil.Drainage = "good"
		r.Soil = &soil
	}
	if m.NoSoil {
		r.Soil = nil
		r.Profile.Soil, r.Profile.PH = "", 0
	}
	return c, r
}

// Cost is what a method costs on an area, by the scheme's norm.
type Cost struct {
	// SqM is the area costed: the plot, or the largest area the scheme
	// assists when the plot is larger.
	SqM float64 `json:"sqm"`
	// Total is the norm's cost of that area in rupees, Subsidy the part
	// the scheme assists and Farmer what is left to pay.
	Total   float64 `json:"total"`
	Subsidy float64 `json:"subsidy"`
	Farmer  float64 `json:"farmer"`
	Ref     *Ref    `json:"ref,omitempty"`
}

// CostOn returns the method's cost on a plot of sqm square metres, or nil
// when the book has no cost for it.
func (m Method) CostOn(sqm float64) *Cost {
	per := m.CostPerSqM
	if m.CostPerHa > 0 {
		per = m.CostPerHa / 10000
	}
	if per <= 0 || sqm <= 0 {
		return nil
	}
	if m.MaxSqM > 0 {
		sqm = min(sqm, m.MaxSqM)
	}
	total := per * sqm
	return &Cost{SqM: sqm, Total: total, Subsidy: total * m.Subsidy, Farmer: total * (1 - m.Subsidy), Ref: m.Ref}
}
