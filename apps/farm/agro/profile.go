package agro

import (
	"fmt"
	"slices"
)

// WaterKind is a source of water for a plot.
type WaterKind string

// The water sources. Rain falls on every plot; the rest irrigate.
const (
	Canal    WaterKind = "canal"
	River    WaterKind = "river"
	Tank     WaterKind = "tank" // a village tank or lake
	Borewell WaterKind = "borewell"
	OpenWell WaterKind = "open_well"
	FarmPond WaterKind = "farm_pond"
)

// WaterKinds lists the irrigation sources.
func WaterKinds() []WaterKind {
	return []WaterKind{Canal, River, Tank, Borewell, OpenWell, FarmPond}
}

// irrigationMM is how much water a source can give a plot in a month, in
// millimetres over the plot, when it is reliable: a rough rule (a 2-inch
// borewell runs a few hours a day on an acre or two) to be replaced by the
// farmer's own numbers.
var irrigationMM = map[WaterKind]float64{
	Canal: 150, River: 150, Tank: 90, Borewell: 120, OpenWell: 80, FarmPond: 50,
}

// Water is one of a plot's water sources.
type Water struct {
	Kind WaterKind `json:"kind"`
	// Reliable is false for a source that runs dry or is shared
	// unpredictably; it then counts for half.
	Reliable bool `json:"reliable"`
}

// Profile is what the farmer tells us about a plot's land and water.
type Profile struct {
	// Soil is a soil id of the library; empty when the farmer does not
	// know it.
	Soil string `json:"soil,omitempty"`
	// PH is a measured pH (a soil health card), 0 when not measured.
	PH float64 `json:"ph,omitempty"`
	// Water lists the irrigation sources; none means rainfed.
	Water []Water `json:"water,omitempty"`
	// Drip is true when the plot is under drip or sprinkler irrigation,
	// which makes irrigation water go further.
	Drip bool `json:"drip,omitempty"`
	// PreviousCrop is the crop of the last season, a crop id; empty when
	// fallow or unknown.
	PreviousCrop string `json:"previous_crop,omitempty"`
}

// Check reports a profile that names a soil or crop the library lacks, a
// pH off the scale, or an unknown water source.
func (p Profile) Check(l *Library) error {
	if p.Soil != "" {
		if _, err := l.Soil(p.Soil); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}
	}
	if p.PH != 0 && (p.PH < 3 || p.PH > 11) {
		return fmt.Errorf("%w: pH %v: want 3 to 11", ErrInvalid, p.PH)
	}
	for _, w := range p.Water {
		if !slices.Contains(WaterKinds(), w.Kind) {
			return fmt.Errorf("%w: water source %q", ErrInvalid, w.Kind)
		}
	}
	if p.PreviousCrop != "" {
		if _, err := l.Crop(p.PreviousCrop); err != nil {
			return fmt.Errorf("%w: previous crop: %w", ErrInvalid, err)
		}
	}
	return nil
}

// IrrigationMM returns how much irrigation water the plot can get in a
// month, in millimetres: the sources added up, unreliable ones at half,
// with drip stretching it by 40 %, at most 250.
func (p Profile) IrrigationMM() float64 {
	t := 0.0
	for _, w := range p.Water {
		mm := irrigationMM[w.Kind]
		if !w.Reliable {
			mm /= 2
		}
		t += mm
	}
	if p.Drip {
		t *= 1.4
	}
	return min(t, 250)
}

// Rainfed reports whether the plot has no irrigation.
func (p Profile) Rainfed() bool { return len(p.Water) == 0 }
