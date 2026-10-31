package farm

import (
	"fmt"
	"math"
)

// Unit is a unit of land area.
type Unit string

// The units farmers declare land in. India counts in acres, guntas (40 to
// the acre, Telangana, Andhra Pradesh, Karnataka, Maharashtra) and cents
// (100 to the acre, Andhra Pradesh, Kerala, Tamil Nadu). Bigha is left out:
// it ranges from about 1,300 to 6,800 m² from state to state.
const (
	SquareMetres Unit = "m2"
	Hectares     Unit = "ha"
	Acres        Unit = "acre"
	Guntas       Unit = "gunta"
	Cents        Unit = "cent"
)

// squareMetres is each unit's size. An acre is exactly 4046.8564224 m².
var squareMetres = map[Unit]float64{
	SquareMetres: 1,
	Hectares:     10000,
	Acres:        4046.8564224,
	Guntas:       4046.8564224 / 40,
	Cents:        4046.8564224 / 100,
}

// Units lists the known units.
func Units() []Unit {
	return []Unit{SquareMetres, Hectares, Acres, Guntas, Cents}
}

// SquareMetres returns the size of one u in square metres, and whether u is
// a known unit.
func (u Unit) SquareMetres() (float64, bool) {
	m, ok := squareMetres[u]
	return m, ok
}

// Area is an amount of land in a unit.
type Area struct {
	Value float64 `json:"value"`
	Unit  Unit    `json:"unit"`
}

// SquareMetres returns a in square metres; it fails with ErrInvalid for an
// unknown unit, or a value that is negative or not finite.
func (a Area) SquareMetres() (float64, error) {
	m, ok := a.Unit.SquareMetres()
	if !ok {
		return 0, fmt.Errorf("%w: unknown unit %q", ErrInvalid, a.Unit)
	}
	if math.IsNaN(a.Value) || math.IsInf(a.Value, 0) || a.Value < 0 {
		return 0, fmt.Errorf("%w: area %v", ErrInvalid, a.Value)
	}
	return a.Value * m, nil
}

// Convert returns m square metres in u.
func Convert(m float64, u Unit) (float64, error) {
	s, ok := u.SquareMetres()
	if !ok {
		return 0, fmt.Errorf("%w: unknown unit %q", ErrInvalid, u)
	}
	return m / s, nil
}

// AcresAndGuntas writes m square metres the way a pattadar passbook does:
// "2 acres 15 guntas", guntas rounded to the nearest whole one.
func AcresAndGuntas(m float64) string {
	g := int(math.Round(m / squareMetres[Guntas]))
	acres, guntas := g/40, g%40
	switch {
	case acres == 0:
		return fmt.Sprintf("%d %s", guntas, pick(guntas, "gunta", "guntas"))
	case guntas == 0:
		return fmt.Sprintf("%d %s", acres, pick(acres, "acre", "acres"))
	}
	return fmt.Sprintf("%d %s %d %s", acres, pick(acres, "acre", "acres"), guntas, pick(guntas, "gunta", "guntas"))
}

// Format writes v of u for people: "1 acre", "2.1 acres", "0.85 ha".
func (u Unit) Format(v float64) string {
	n := num(v)
	switch u {
	case SquareMetres:
		return n + " m²"
	case Hectares:
		return n + " ha"
	case Acres, Guntas, Cents:
		if n == "1" {
			return n + " " + string(u)
		}
		return n + " " + string(u) + "s"
	}
	return n + " " + string(u)
}
