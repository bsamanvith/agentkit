// Package weather gives the week's forecast at a plot: a [Source], from
// one of several free providers ([OpenMeteo], [METNorway]) or the first of
// them that answers ([Fallback]).
//
// Forecasts are kept per grid cell of about 11 km (0.1°) for a few hours:
// every plot of a village shares one request, so a weekly check of
// thousands of plots asks the service once per village, not once per plot.
package weather

import (
	"context"
	"errors"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/geo"
)

// ErrUnavailable means the forecast could not be had: the service is down,
// paused after a rate limit, or answered something we cannot read.
var ErrUnavailable = errors.New("weather: forecast unavailable")

// DateLayout is how days are written.
const DateLayout = "2006-01-02"

// Day is one day's forecast, in the plot's local time.
type Day struct {
	Date string `json:"date"` // YYYY-MM-DD
	// TMaxC and TMinC are the day's high and low air temperature, °C.
	TMaxC float64 `json:"tmax_c"`
	TMinC float64 `json:"tmin_c"`
	// RainMM is the day's total rain (and other precipitation), mm;
	// RainChance its highest hourly probability, 0 to 100.
	RainMM     float64 `json:"rain_mm"`
	RainChance float64 `json:"rain_chance"`
	// WindKmh is the strongest sustained wind at 10 m, GustKmh the
	// strongest gust, km/h.
	WindKmh float64 `json:"wind_kmh"`
	GustKmh float64 `json:"gust_kmh"`
	// HumidityPct is the day's mean relative humidity.
	HumidityPct float64 `json:"humidity_pct"`
	// ET0MM is FAO-56 reference evapotranspiration: how much water a
	// grass crop would use that day, mm.
	ET0MM float64 `json:"et0_mm"`
}

// Forecast is the days ahead at a point, today first.
type Forecast struct {
	Point    geo.Point `json:"point"`
	Timezone string    `json:"timezone"`
	Days     []Day     `json:"days"`
	// Source names the service and its model.
	Source string `json:"source"`
	// Fetched is when the service answered.
	Fetched time.Time `json:"fetched"`
}

// Source gives forecasts: one provider ([OpenMeteo], [METNorway]), or a
// [Fallback] over several. Providers come and go, so the app depends on
// this interface only and picks the providers in one place (cmd/farm's
// -weather). Implementations are safe for concurrent use.
type Source interface {
	// Name names the provider, for logs and the forecast's Source.
	Name() string
	// Forecast returns up to days days (1 to 16) from today at p, today
	// first. Errors match [ErrUnavailable] when the service cannot answer.
	Forecast(ctx context.Context, p geo.Point, days int) (Forecast, error)
}

// Totals are a stretch of days summed up.
type Totals struct {
	RainMM  float64 `json:"rain_mm"`
	ET0MM   float64 `json:"et0_mm"`
	MaxC    float64 `json:"max_c"`
	MinC    float64 `json:"min_c"`
	GustKmh float64 `json:"gust_kmh"`
	// RainDays are days with 2.5 mm or more (the IMD's rainy day).
	RainDays int `json:"rain_days"`
}

// RainyDayMM is the India Meteorological Department's rainy day: 2.5 mm
// or more.
const RainyDayMM = 2.5

// Sum adds up days.
func Sum(days []Day) Totals {
	var t Totals
	for i, d := range days {
		t.RainMM += d.RainMM
		t.ET0MM += d.ET0MM
		if i == 0 || d.TMaxC > t.MaxC {
			t.MaxC = d.TMaxC
		}
		if i == 0 || d.TMinC < t.MinC {
			t.MinC = d.TMinC
		}
		t.GustKmh = max(t.GustKmh, d.GustKmh)
		if d.RainMM >= RainyDayMM {
			t.RainDays++
		}
	}
	return t
}
