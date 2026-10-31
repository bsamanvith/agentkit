package agro

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrUnknown is a soil or crop the library does not have.
var ErrUnknown = errors.New("agro: unknown")

// ErrInvalid is an input that breaks a rule of this package: a range the
// wrong way round, a profile naming an unknown soil, a bad date.
var ErrInvalid = errors.New("agro: invalid")

// Texture is a soil texture class, from the coarsest to the finest.
type Texture string

// The texture classes.
const (
	Sand      Texture = "sand"
	LoamySand Texture = "loamy_sand"
	SandyLoam Texture = "sandy_loam"
	Loam      Texture = "loam"
	SiltLoam  Texture = "silt_loam"
	ClayLoam  Texture = "clay_loam"
	Clay      Texture = "clay"
)

// Textures lists the texture classes, coarsest first.
func Textures() []Texture {
	return []Texture{Sand, LoamySand, SandyLoam, Loam, SiltLoam, ClayLoam, Clay}
}

// Range is a low and a high value, low first.
type Range [2]float64

// Mid returns the middle of the range.
func (r Range) Mid() float64 { return (r[0] + r[1]) / 2 }

// Contains reports whether v is in the range, ends included.
func (r Range) Contains(v float64) bool { return r[0] <= v && v <= r[1] }

// Soil is a soil type as farmers and soil surveys name it.
type Soil struct {
	ID    string            `json:"id"`
	Name  string            `json:"name"`
	Names map[string]string `json:"names,omitempty"` // by language code: hi, te, ...
	// Signs say how a farmer recognises it.
	Signs        string  `json:"signs"`
	Texture      Texture `json:"texture"`
	PH           Range   `json:"ph"`
	WaterHolding string  `json:"water_holding"` // very_low, low, medium, high, very_high
	Drainage     string  `json:"drainage"`      // very_poor, poor, moderate, good, excessive
	Fertility    string  `json:"fertility"`
	Found        string  `json:"found"`
	Suits        string  `json:"suits"`
	Salty        bool    `json:"salty,omitempty"`
}

// DrainsPoorly reports whether water stands on the soil after heavy rain.
func (s Soil) DrainsPoorly() bool { return s.Drainage == "poor" || s.Drainage == "very_poor" }

// Tolerances a crop may have.
const (
	TolerateWaterlogging = "waterlogging"
	TolerateFrost        = "frost"
	TolerateDrought      = "drought"
	TolerateSalinity     = "salinity"
)

// Crop is what a crop needs and how it is grown.
type Crop struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Names     map[string]string `json:"names,omitempty"`
	Family    string            `json:"family"` // botanical, for rotation
	Kind      string            `json:"kind"`   // cereal, millet, pulse, oilseed, ...
	Perennial bool              `json:"perennial"`
	// Days runs from sowing or planting to the (first) harvest.
	Days Range `json:"days"`
	// TempOpt and TempAbs are the monthly mean temperatures, in °C, it
	// grows best in and survives in.
	TempOpt Range `json:"temp_opt"`
	TempAbs Range `json:"temp_abs"`
	// WaterMM is the water it needs over its season (a perennial: over a
	// year), from rain and irrigation, in millimetres.
	WaterMM   Range     `json:"water_mm"`
	PH        Range     `json:"ph"`
	Textures  []Texture `json:"textures"`
	Tolerates []string  `json:"tolerates,omitempty"`
	// RowCM and PlantCM are the usual spacing between rows and between
	// plants in a row.
	RowCM   float64 `json:"row_cm"`
	PlantCM float64 `json:"plant_cm"`
	// Seasons are the Indian seasons it is sown in: kharif, rabi, zaid.
	Seasons []string `json:"seasons"`
	GrownIn []string `json:"grown_in"`
	// ClimateAnalog marks a crop new to India, suggested because the
	// climate where it is grown matches.
	ClimateAnalog bool `json:"climate_analog"`
	// Tall crops shade the ones beside them.
	Tall bool `json:"tall"`
	// Intercrops are crops it is known to grow well with.
	Intercrops []string `json:"intercrops,omitempty"`
}

// Tolerant reports whether the crop tolerates t (TolerateFrost, ...).
func (c Crop) Tolerant(t string) bool { return slices.Contains(c.Tolerates, t) }

// Suits reports whether the crop grows on the texture.
func (c Crop) Suits(t Texture) bool { return slices.Contains(c.Textures, t) }

// NameIn returns the crop's name in a language (a code like "te"), else its
// English name.
func (c Crop) NameIn(lang string) string {
	if n := c.Names[lang]; n != "" {
		return n
	}
	return c.Name
}

// Library is the soils and crops the rules know. It is read-only and safe
// for concurrent use.
type Library struct {
	SoilsSource string
	CropsSource string
	soils       []Soil
	crops       []Crop
}

//go:embed data/soils.json data/crops.json
var data embed.FS

// Default returns the library built into the app: about a dozen Indian
// soil types and the crops of data/crops.json.
func Default() (*Library, error) {
	soils, err := data.ReadFile("data/soils.json")
	if err != nil {
		return nil, err
	}
	crops, err := data.ReadFile("data/crops.json")
	if err != nil {
		return nil, err
	}
	return Load(soils, crops)
}

// Load builds a library from the JSON of a soil list ({"source", "soils"})
// and a crop list ({"source", "crops"}), and checks it: unique ids, ranges
// low first, known textures and tolerances, intercrops that are in the
// library and name each other.
func Load(soilsJSON, cropsJSON []byte) (*Library, error) {
	var s struct {
		Source string `json:"source"`
		Soils  []Soil `json:"soils"`
	}
	var c struct {
		Source string `json:"source"`
		Crops  []Crop `json:"crops"`
	}
	if err := json.Unmarshal(soilsJSON, &s); err != nil {
		return nil, fmt.Errorf("%w: soils: %w", ErrInvalid, err)
	}
	if err := json.Unmarshal(cropsJSON, &c); err != nil {
		return nil, fmt.Errorf("%w: crops: %w", ErrInvalid, err)
	}
	l := &Library{SoilsSource: s.Source, CropsSource: c.Source, soils: s.Soils, crops: c.Crops}
	if err := l.check(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *Library) check() error {
	var errs []error
	bad := func(format string, a ...any) {
		errs = append(errs, fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, a...)...))
	}
	ordered := func(what, id string, r Range) {
		if r[0] > r[1] {
			bad("%s %s: range %v is the wrong way round", id, what, r)
		}
	}
	seen := map[string]bool{}
	for _, s := range l.soils {
		if s.ID == "" || seen["soil:"+s.ID] {
			bad("soil %q: empty or repeated id", s.ID)
		}
		seen["soil:"+s.ID] = true
		ordered("ph", s.ID, s.PH)
		if !slices.Contains(Textures(), s.Texture) {
			bad("soil %s: texture %q", s.ID, s.Texture)
		}
	}
	for _, c := range l.crops {
		if c.ID == "" || seen[c.ID] {
			bad("crop %q: empty or repeated id", c.ID)
		}
		seen[c.ID] = true
		for what, r := range map[string]Range{"days": c.Days, "temp_opt": c.TempOpt, "temp_abs": c.TempAbs, "water_mm": c.WaterMM, "ph": c.PH} {
			ordered(what, c.ID, r)
		}
		if c.TempOpt[0] < c.TempAbs[0] || c.TempOpt[1] > c.TempAbs[1] {
			bad("crop %s: temp_opt %v is not inside temp_abs %v", c.ID, c.TempOpt, c.TempAbs)
		}
		if c.Days[0] <= 0 || c.RowCM <= 0 || c.PlantCM <= 0 {
			bad("crop %s: days and spacing must be positive", c.ID)
		}
		if len(c.Textures) == 0 {
			bad("crop %s: no textures", c.ID)
		}
		for _, t := range c.Textures {
			if !slices.Contains(Textures(), t) {
				bad("crop %s: texture %q", c.ID, t)
			}
		}
		for _, t := range c.Tolerates {
			if !slices.Contains([]string{TolerateWaterlogging, TolerateFrost, TolerateDrought, TolerateSalinity}, t) {
				bad("crop %s: tolerance %q", c.ID, t)
			}
		}
		for _, s := range c.Seasons {
			if !slices.Contains([]string{Kharif, Rabi, Zaid}, s) {
				bad("crop %s: season %q", c.ID, s)
			}
		}
	}
	for _, c := range l.crops {
		for _, o := range c.Intercrops {
			other, err := l.Crop(o)
			switch {
			case err != nil:
				bad("crop %s: intercrop %q is not in the library", c.ID, o)
			case !slices.Contains(other.Intercrops, c.ID):
				bad("crop %s: intercrop %s does not name it back", c.ID, o)
			}
		}
	}
	slices.SortFunc(l.soils, func(a, b Soil) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(l.crops, func(a, b Crop) int { return strings.Compare(a.ID, b.ID) })
	return errors.Join(errs...)
}

// Soils returns the soils, by id.
func (l *Library) Soils() []Soil { return slices.Clone(l.soils) }

// Crops returns the crops, by id.
func (l *Library) Crops() []Crop { return slices.Clone(l.crops) }

// Soil returns the soil with the id, or ErrUnknown.
func (l *Library) Soil(id string) (Soil, error) {
	i, ok := slices.BinarySearchFunc(l.soils, id, func(s Soil, id string) int { return strings.Compare(s.ID, id) })
	if !ok {
		return Soil{}, fmt.Errorf("%w: soil %q", ErrUnknown, id)
	}
	return l.soils[i], nil
}

// Crop returns the crop with the id, or ErrUnknown.
func (l *Library) Crop(id string) (Crop, error) {
	for _, c := range l.crops {
		if c.ID == id {
			return c, nil
		}
	}
	return Crop{}, fmt.Errorf("%w: crop %q", ErrUnknown, id)
}
