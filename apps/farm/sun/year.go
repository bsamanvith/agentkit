package sun

import "math"

// Month is the sun at a plot over a typical day of one month.
type Month struct {
	Month int `json:"month"` // 1 to 12
	// DayLengthH is the hours from sunrise to sunset.
	DayLengthH float64 `json:"day_length_h"`
	// SunHours is the hours of bright sun: fewer than the day's under
	// cloud.
	SunHours float64 `json:"sun_hours"`
	// Noon is where the sun is at solar noon: its height, and whether it
	// is to the south (azimuth 180) or the north (0) of the plot.
	Noon Position `json:"noon"`
	// NoonIncidence is the angle between the noon sun's rays and the
	// perpendicular of the ground: the flat ground's when the plot is
	// flat. The smaller, the stronger the light on each square metre.
	NoonIncidence float64 `json:"noon_incidence"`
	// FlatMJ is the light a day brings to flat ground, in MJ/m², and
	// ClearMJ what it would under a clear sky; 0 when not known.
	FlatMJ  float64 `json:"flat_mj"`
	ClearMJ float64 `json:"clear_mj,omitempty"`
	// SlopeMJ is the light on the plot's own slope, and SlopeFactor
	// SlopeMJ over FlatMJ (1 on flat ground).
	SlopeMJ     float64 `json:"slope_mj"`
	SlopeFactor float64 `json:"slope_factor"`
	// CloudLoss is the share of a clear sky's light that cloud takes,
	// from 0 to 1; 0 when not known.
	CloudLoss float64 `json:"cloud_loss"`
}

// Year returns the sun at latitude lat on ground lying as s, month by
// month, January first. flat and clear are each month's light on flat
// ground under the real and a clear sky (MJ/m² a day), from climate
// normals; zeros where not known, which leave the light out but keep the
// geometry.
func Year(lat float64, flat, clear [12]float64, s Slope) [12]Month {
	var out [12]Month
	for i := range 12 {
		doy := MonthDays[i]
		m := Month{
			Month:       i + 1,
			DayLengthH:  r1(DayLength(lat, doy)),
			Noon:        r1p(At(lat, doy, 12)),
			SlopeFactor: 1,
		}
		m.NoonIncidence = r1(Incidence(lat, doy, 12, s))
		if flat[i] > 0 {
			m.FlatMJ = r1(flat[i])
			m.SunHours = r1(SunHours(lat, doy, flat[i]))
			on := OnSlope(lat, i+1, flat[i], s)
			m.SlopeMJ = r1(on)
			m.SlopeFactor = math.Round(on/flat[i]*100) / 100
		}
		if clear[i] > 0 {
			m.ClearMJ = r1(clear[i])
			if flat[i] > 0 {
				m.CloudLoss = math.Round(max(0, 1-flat[i]/clear[i])*100) / 100
			}
		}
		out[i] = m
	}
	return out
}

func r1(v float64) float64 { return math.Round(v*10) / 10 }

func r1p(p Position) Position { return Position{Elevation: r1(p.Elevation), Azimuth: r1(p.Azimuth)} }
