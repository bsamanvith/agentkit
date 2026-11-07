// Package sun is the sun at a plot: where it is in the sky at an hour of
// a day, how long the day is, the angle its rays strike flat or sloping
// land at, and how much of its energy a month's days bring to the ground
// on a slope, given what reaches flat ground (NASA POWER's monthly
// normals).
//
// The formulas are the textbook ones: declination, day length and the
// energy at the top of the atmosphere from FAO-56 (Allen et al. 1998,
// equations 21 to 25 and 34); sun hours from the Ångström relation with
// FAO-56's default coefficients (equation 35); the diffuse share of a
// month's light from Erbs, Klein and Duffie (1982); and the light on a
// slope from Liu and Jordan's isotropic sky (Duffie and Beckman, Solar
// Engineering of Thermal Processes, §2.19), the beam part integrated over
// the day. Hours are solar time: noon is when the sun is highest.
package sun

import (
	"errors"
	"fmt"
	"math"
)

// Albedo is the share of light the ground round a slope reflects onto it:
// 0.2, the usual value for fields and grass.
const Albedo = 0.2

// Ångström's coefficients (FAO-56's defaults where none are calibrated):
// a clear day brings a+b of the light at the top of the atmosphere, a
// day with no sun at all a.
const (
	angA = 0.25
	angB = 0.50
)

// ErrInvalid reports a latitude, day or slope out of range.
var ErrInvalid = errors.New("sun: invalid")

// MonthDays are each month's representative day of the year (Klein 1977):
// the day whose sun is closest to the month's average.
var MonthDays = [12]int{17, 47, 75, 105, 135, 162, 198, 228, 258, 288, 318, 344}

func rad(d float64) float64 { return d * math.Pi / 180 }
func deg(r float64) float64 { return r * 180 / math.Pi }

// Declination is the sun's declination on day of the year doy (1 to 366),
// in degrees: north of the equator positive.
func Declination(doy int) float64 {
	return deg(0.409 * math.Sin(2*math.Pi/365*float64(doy)-1.39))
}

// sunsetAngle is ωs, the hour angle of sunset, in radians: 0 in polar
// night, π in polar day.
func sunsetAngle(lat float64, doy int) float64 {
	x := -math.Tan(rad(lat)) * math.Tan(rad(Declination(doy)))
	return math.Acos(max(-1, min(1, x)))
}

// DayLength is the hours from sunrise to sunset on day doy at latitude lat.
func DayLength(lat float64, doy int) float64 {
	return 24 / math.Pi * sunsetAngle(lat, doy)
}

// Extraterrestrial is the sun's energy over day doy on flat ground at the
// top of the atmosphere above latitude lat, in MJ/m²: Ra of FAO-56.
func Extraterrestrial(lat float64, doy int) float64 {
	const gsc = 0.0820 // MJ/m² a minute
	dr := 1 + 0.033*math.Cos(2*math.Pi/365*float64(doy))
	φ, δ, ωs := rad(lat), rad(Declination(doy)), sunsetAngle(lat, doy)
	return 24 * 60 / math.Pi * gsc * dr * (ωs*math.Sin(φ)*math.Sin(δ) + math.Cos(φ)*math.Cos(δ)*math.Sin(ωs))
}

// Vector is the sun's direction at an hour (solar time) of day doy at
// latitude lat, as a unit vector: east, north and up. Up is negative at
// night.
func Vector(lat float64, doy int, hour float64) (e, n, u float64) {
	φ, δ, h := rad(lat), rad(Declination(doy)), rad(15*(hour-12))
	e = -math.Cos(δ) * math.Sin(h)
	n = math.Sin(δ)*math.Cos(φ) - math.Cos(δ)*math.Cos(h)*math.Sin(φ)
	u = math.Sin(δ)*math.Sin(φ) + math.Cos(δ)*math.Cos(h)*math.Cos(φ)
	return e, n, u
}

// Position is where the sun is in the sky.
type Position struct {
	// Elevation is its angle above the horizon, in degrees.
	Elevation float64 `json:"elevation"`
	// Azimuth is its compass bearing, in degrees from north through east.
	Azimuth float64 `json:"azimuth"`
}

// At returns the sun's position at an hour (solar time) of day doy at
// latitude lat.
func At(lat float64, doy int, hour float64) Position {
	e, n, u := Vector(lat, doy, hour)
	return Position{Elevation: deg(math.Asin(max(-1, min(1, u)))), Azimuth: math.Mod(deg(math.Atan2(e, n))+360, 360)}
}

// Slope is how the ground lies: how steep, and which way it faces.
type Slope struct {
	// TiltDeg is the ground's angle from flat, in degrees (0 to 60).
	TiltDeg float64 `json:"tilt_deg"`
	// AspectDeg is the compass bearing the ground faces (falls towards),
	// in degrees from north through east. It means nothing when flat.
	AspectDeg float64 `json:"aspect_deg"`
}

// FromPercent returns the slope of ground that falls pct metres in a
// hundred towards the bearing aspect.
func FromPercent(pct, aspect float64) Slope {
	return Slope{TiltDeg: deg(math.Atan(pct / 100)), AspectDeg: math.Mod(aspect+360, 360)}
}

// Check reports a slope out of range.
func (s Slope) Check() error {
	if math.IsNaN(s.TiltDeg) || s.TiltDeg < 0 || s.TiltDeg > 60 {
		return fmt.Errorf("%w: tilt %v°: want 0 to 60", ErrInvalid, s.TiltDeg)
	}
	if math.IsNaN(s.AspectDeg) || s.AspectDeg < 0 || s.AspectDeg >= 360 {
		return fmt.Errorf("%w: aspect %v°: want 0 to 360", ErrInvalid, s.AspectDeg)
	}
	return nil
}

// normal is the slope's upward unit normal: east, north, up.
func (s Slope) normal() (e, n, u float64) {
	β, γ := rad(s.TiltDeg), rad(s.AspectDeg)
	return math.Sin(β) * math.Sin(γ), math.Sin(β) * math.Cos(γ), math.Cos(β)
}

// Incidence is the angle, in degrees, between the sun's rays and the
// perpendicular of ground lying as s, at an hour of day doy: 0 when the
// sun shines straight onto it, 90 or more when it grazes it or shines on
// its back.
func Incidence(lat float64, doy int, hour float64, s Slope) float64 {
	e, n, u := Vector(lat, doy, hour)
	ne, nn, nu := s.normal()
	return deg(math.Acos(max(-1, min(1, e*ne+n*nn+u*nu))))
}

// BeamRatio is Rb: the direct sunlight a day brings to ground lying as s
// over what it brings to flat ground, both under a clear sky's direct
// beam. Below 1 the slope gets less, above 1 more. It counts only the
// hours the sun is above the horizon and in front of the slope.
func BeamRatio(lat float64, doy int, s Slope) float64 {
	ωs := sunsetAngle(lat, doy)
	if ωs == 0 {
		return 1
	}
	const steps = 240
	var onSlope, onFlat float64
	ne, nn, nu := s.normal()
	for i := range steps {
		h := -ωs + (float64(i)+0.5)*2*ωs/steps
		hour := 12 + deg(h)/15
		e, n, u := Vector(lat, doy, hour)
		if u <= 0 {
			continue
		}
		onFlat += u
		onSlope += max(0, e*ne+n*nn+u*nu)
	}
	if onFlat == 0 {
		return 1
	}
	return onSlope / onFlat
}

// diffuseShare is Erbs, Klein and Duffie's share of a month's daily light
// that comes from the whole sky rather than straight from the sun, from
// the clearness index kt (light on the ground over light at the top of
// the atmosphere) and the sunset hour angle.
func diffuseShare(kt, ωs float64) float64 {
	kt = max(0.3, min(0.8, kt)) // the range the fit was made on
	if deg(ωs) <= 81.4 {
		return 1.391 - 3.560*kt + 4.189*kt*kt - 2.137*kt*kt*kt
	}
	return 1.311 - 3.022*kt + 3.427*kt*kt - 1.821*kt*kt*kt
}

// OnSlope is the light, in MJ/m² a day, that a day of month m (1 to 12)
// brings to ground lying as s at latitude lat, when flat ground there gets
// flat MJ/m². Direct light scales by BeamRatio, the sky's diffuse light by
// the share of sky the slope sees, and the slope also gets light reflected
// from the ground before it.
func OnSlope(lat float64, m int, flat float64, s Slope) float64 {
	if s.TiltDeg == 0 || flat <= 0 {
		return flat
	}
	doy := MonthDays[m-1]
	ra := Extraterrestrial(lat, doy)
	if ra <= 0 {
		return flat
	}
	d := diffuseShare(flat/ra, sunsetAngle(lat, doy)) * flat
	cosβ := math.Cos(rad(s.TiltDeg))
	return (flat-d)*BeamRatio(lat, doy, s) + d*(1+cosβ)/2 + flat*Albedo*(1-cosβ)/2
}

// SunHours is the bright-sunshine hours of a day at latitude lat on day
// doy that brings flat MJ/m² to flat ground: Ångström's relation solved
// for the hours, within 0 and the day's length.
func SunHours(lat float64, doy int, flat float64) float64 {
	n := DayLength(lat, doy)
	ra := Extraterrestrial(lat, doy)
	if ra <= 0 || n == 0 {
		return 0
	}
	return n * max(0, min(1, (flat/ra-angA)/angB))
}

// ShadowNorth is where the shadows of things on flat ground fall, north
// or south, over the days days from day of the year from at latitude lat:
// the shadow's reach to the north per metre of height, weighted by how
// strong the sun is at each hour (negative: to the south). Hours are
// sampled from 7:00 to 17:00, a day a week.
func ShadowNorth(lat float64, from, days int) float64 {
	var north, weight float64
	for d := 0; d < max(days, 1); d += 7 {
		doy := (from+d-1)%365 + 1
		for hour := 7.0; hour <= 17; hour++ {
			_, n, u := Vector(lat, doy, hour)
			if u <= 0.05 {
				continue
			}
			// The shadow points away from the sun: its north reach is
			// -n/u per metre, weighted by the light u.
			north += -n
			weight += u
		}
	}
	if weight == 0 {
		return 0
	}
	return north / weight
}
