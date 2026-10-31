package agro

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/geo"
)

// ErrNoClimate is a climate source that has nothing for a point, or is
// down.
var ErrNoClimate = errors.New("agro: no climate for the point")

// Month is a month's climate normals.
type Month struct {
	// Temp, TempMax and TempMin are the mean, mean daily high and mean
	// daily low temperature, in °C.
	Temp    float64 `json:"temp"`
	TempMax float64 `json:"temp_max"`
	TempMin float64 `json:"temp_min"`
	// RainMM is the month's total rain, in millimetres.
	RainMM float64 `json:"rain_mm"`
	// Humidity is the mean relative humidity, in percent.
	Humidity float64 `json:"humidity"`
}

// Climate is a place's climate normals: twelve months, January first.
type Climate struct {
	Point  geo.Point `json:"point"`
	Months [12]Month `json:"months"`
	// Source names where the normals come from, and their years.
	Source string `json:"source"`
}

// RainMM returns the year's total rain.
func (c Climate) RainMM() float64 {
	t := 0.0
	for _, m := range c.Months {
		t += m.RainMM
	}
	return t
}

// Check reports a climate that cannot be right: rain below zero, or
// temperatures off the earth's range.
func (c Climate) Check() error {
	for i, m := range c.Months {
		for _, t := range []float64{m.Temp, m.TempMax, m.TempMin} {
			if math.IsNaN(t) || t < -60 || t > 60 {
				return fmt.Errorf("%w: climate: %s temperature %v", ErrInvalid, time.Month(i+1), t)
			}
		}
		if math.IsNaN(m.RainMM) || m.RainMM < 0 || m.RainMM > 5000 {
			return fmt.Errorf("%w: climate: %s rain %v mm", ErrInvalid, time.Month(i+1), m.RainMM)
		}
	}
	return nil
}

// tempOn returns the mean temperature of day t, interpolated between the
// months' mid points.
func (c Climate) tempOn(t time.Time) float64 {
	m := int(t.Month()) - 1
	mid := float64(daysIn(t)) / 2
	d := float64(t.Day()) - 0.5
	other := m + 1
	if d < mid {
		other = m - 1
	}
	other = (other + 12) % 12
	f := math.Abs(d-mid) / float64(daysIn(t))
	return c.Months[m].Temp*(1-f) + c.Months[other].Temp*f
}

// rainOn returns the rain of day t: its month's total spread evenly.
func (c Climate) rainOn(t time.Time) float64 {
	return c.Months[t.Month()-1].RainMM / float64(daysIn(t))
}

func daysIn(t time.Time) int {
	return time.Date(t.Year(), t.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// ClimateSource gives the climate normals at a point. A source that has
// nothing for the point, or is down, returns an error that matches
// ErrNoClimate.
type ClimateSource interface {
	Climate(ctx context.Context, p geo.Point) (Climate, error)
}

// The Indian seasons.
const (
	Kharif = "kharif" // sown with the monsoon, June to September
	Rabi   = "rabi"   // sown after it, October to February
	Zaid   = "zaid"   // the short summer season, March to May
)

// SeasonOf returns the Indian season a sowing on date t falls in.
func SeasonOf(t time.Time) string {
	switch m := t.Month(); {
	case m >= time.June && m <= time.September:
		return Kharif
	case m >= time.March && m <= time.May:
		return Zaid
	default:
		return Rabi
	}
}
