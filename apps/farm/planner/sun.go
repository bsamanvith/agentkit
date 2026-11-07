package planner

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/sun"
)

// SlopeOf returns how a farm's ground lies, from its survey's heights:
// nil when it is not surveyed, the heights are missing, or the fall is
// below what the elevation model can tell.
func SlopeOf(f farm.Farm) *sun.Slope {
	if f.Survey == nil || f.Survey.Terrain == nil {
		return nil
	}
	t := f.Survey.Terrain
	if t.Flat || t.SlopePct <= 0 || t.Lowest >= len(t.Points) || t.Highest >= len(t.Points) {
		return nil
	}
	s := sun.FromPercent(t.SlopePct, geo.Bearing(t.Points[t.Highest], t.Points[t.Lowest]))
	s.TiltDeg, s.AspectDeg = math.Round(s.TiltDeg*10)/10, math.Round(s.AspectDeg)
	if s.AspectDeg == 360 {
		s.AspectDeg = 0
	}
	if s.Check() != nil {
		return nil
	}
	return &s
}

// SunReport is the sun at a farm through the year.
type SunReport struct {
	Farm  string    `json:"farm"`
	Point geo.Point `json:"point"`
	// Slope is how the ground lies, from the survey; nil when flat or not
	// surveyed (SlopeNote says which).
	Slope     *sun.Slope `json:"slope,omitempty"`
	SlopeNote string     `json:"slope_note"`
	// Months are January first.
	Months [12]sun.Month `json:"months"`
	// Seasons are the Indian seasons' months, for a calendar.
	Seasons []SeasonSpan `json:"seasons"`
	// Notes say what matters in plain words.
	Notes  []string `json:"notes"`
	Source string   `json:"source"`
	Today  string   `json:"today"`
}

// SeasonSpan is an Indian season's usual sowing months.
type SeasonSpan struct {
	Season string `json:"season"`
	From   int    `json:"from"` // month, 1 to 12
	To     int    `json:"to"`
}

// Seasons are the usual sowing months of the Indian seasons.
var Seasons = []SeasonSpan{{agro.Kharif, 6, 9}, {agro.Rabi, 10, 2}, {agro.Zaid, 3, 5}}

// Sun returns the sun at a farm month by month: day length, bright sun
// hours, the noon sun's height and side, the angle its rays strike the
// ground at, and the light on the plot's slope; with notes on what
// matters for crops.
func (p *Planner) Sun(ctx context.Context, id string) (SunReport, error) {
	f, err := p.farms.Get(ctx, id)
	if err != nil {
		return SunReport{}, err
	}
	c, err := p.climateAt(ctx, f)
	if err != nil {
		return SunReport{}, err
	}
	rep := SunReport{Farm: f.ID, Point: c.Point, Slope: SlopeOf(f), Seasons: Seasons, Source: c.Source, Today: p.Today().Format(agro.DateLayout)}
	switch {
	case rep.Slope != nil:
		rep.SlopeNote = fmt.Sprintf("The survey's heights: the ground falls %.0f%% towards the %s.", f.Survey.Terrain.SlopePct, geo.CompassWords(rep.Slope.AspectDeg))
	case f.Survey == nil || f.Survey.Terrain == nil:
		rep.SlopeNote = "Not surveyed yet: taken as flat. Survey the land to count its slope."
	default:
		rep.SlopeNote = "Flat, as far as the elevation model can tell."
	}
	var s sun.Slope
	if rep.Slope != nil {
		s = *rep.Slope
	}
	rep.Months = c.Sun(s)
	rep.Notes = sunNotes(c.Point.Lat, rep.Months, rep.Slope, c.HasSun())
	return rep, nil
}

// sunNotes says in plain words what the year's sun means for crops.
func sunNotes(lat float64, ms [12]sun.Month, slope *sun.Slope, light bool) []string {
	var notes []string
	var north []int
	for _, m := range ms {
		if m.Noon.Azimuth < 90 || m.Noon.Azimuth > 270 {
			north = append(north, m.Month)
		}
	}
	short, long := ms[0], ms[0]
	for _, m := range ms {
		if m.DayLengthH < short.DayLengthH {
			short = m
		}
		if m.DayLengthH > long.DayLengthH {
			long = m
		}
	}
	side, other := "south", "north"
	if lat < 0 {
		side, other = "north", "south"
	}
	switch {
	case len(north) == 0 || len(north) == 12:
		notes = append(notes, fmt.Sprintf("The noon sun is always to the %s: tall crops and trees go on the %s side, so their shade falls away from the rest.", side, other))
	default:
		notes = append(notes, fmt.Sprintf("Inside the tropics the noon sun is to the %s of the plot in %s and to the %s the rest of the year: north to south rows shade each other least, and shade falls on both sides of tall crops over a year.",
			compass(ms[north[0]-1].Noon.Azimuth), monthList(north), side))
	}
	notes = append(notes, fmt.Sprintf("Days run from %.1f h in %s to %.1f h in %s.", short.DayLengthH, time.Month(short.Month), long.DayLengthH, time.Month(long.Month)))
	if light {
		dull, bright := ms[0], ms[0]
		for _, m := range ms {
			if m.FlatMJ < dull.FlatMJ {
				dull = m
			}
			if m.FlatMJ > bright.FlatMJ {
				bright = m
			}
		}
		notes = append(notes, fmt.Sprintf("Sunniest in %s (about %.0f MJ/m² a day, %.1f hours of bright sun); dullest in %s (%.0f MJ/m², %.1f hours).",
			time.Month(bright.Month), bright.FlatMJ, bright.SunHours, time.Month(dull.Month), dull.FlatMJ, dull.SunHours))
		var cloudy []int
		for _, m := range ms {
			if m.CloudLoss >= 0.3 {
				cloudy = append(cloudy, m.Month)
			}
		}
		if len(cloudy) > 0 {
			notes = append(notes, fmt.Sprintf("Cloud takes a third or more of the sun in %s: sun-loving crops grow slower then and fungus spreads; it suits crops that bear part shade.", monthList(cloudy)))
		}
	} else {
		notes = append(notes, "The climate has no sunlight figures for this place: day length and the sun's angle only.")
	}
	if slope != nil {
		lo, hi := ms[0], ms[0]
		for _, m := range ms {
			if m.SlopeFactor < lo.SlopeFactor {
				lo = m
			}
			if m.SlopeFactor > hi.SlopeFactor {
				hi = m
			}
		}
		facing := geo.CompassWords(slope.AspectDeg)
		switch {
		case !light:
		case lo.SlopeFactor < 0.95:
			notes = append(notes, fmt.Sprintf("The slope faces %s: in %s it gets about %.0f%% less sun than flat ground; sow sun-loving crops on its sunnier parts or when the sun is high.", facing, time.Month(lo.Month), (1-lo.SlopeFactor)*100))
		case hi.SlopeFactor > 1.05:
			notes = append(notes, fmt.Sprintf("The slope faces %s: in %s it gets about %.0f%% more sun than flat ground, and dries faster.", facing, time.Month(hi.Month), (hi.SlopeFactor-1)*100))
		default:
			notes = append(notes, fmt.Sprintf("The slope faces %s, but it is gentle: the sun on it is within 5%% of flat ground's all year.", facing))
		}
	}
	return notes
}

func compass(az float64) string { return geo.CompassWords(az) }

// monthList names months as runs: "May to August".
func monthList(ms []int) string {
	var parts []string
	for i := 0; i < len(ms); {
		j := i
		for j+1 < len(ms) && ms[j+1] == ms[j]+1 {
			j++
		}
		switch j - i {
		case 0:
			parts = append(parts, time.Month(ms[i]).String())
		case 1:
			parts = append(parts, time.Month(ms[i]).String(), time.Month(ms[j]).String())
		default:
			parts = append(parts, time.Month(ms[i]).String()+" to "+time.Month(ms[j]).String())
		}
		i = j + 1
	}
	return andList(parts)
}

// andList joins words as "a, b and c".
func andList(ws []string) string {
	if len(ws) < 2 {
		return strings.Join(ws, "")
	}
	return strings.Join(ws[:len(ws)-1], ", ") + " and " + ws[len(ws)-1]
}
