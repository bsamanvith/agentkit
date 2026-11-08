// Package weathertest has a fake [weather.Source] for tests.
package weathertest

import (
	"context"
	"sync"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/weather"
)

// Fake answers every point with Days, or Err. It counts its calls. Set its
// fields before use, or with Set while in use.
type Fake struct {
	mu    sync.Mutex
	days  []weather.Day
	err   error
	calls int
}

// New returns a fake that answers with days.
func New(days ...weather.Day) *Fake { return &Fake{days: days} }

// Set replaces the answer.
func (f *Fake) Set(err error, days ...weather.Day) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.days, f.err = days, err
}

// Calls returns how many forecasts were asked for.
func (f *Fake) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// Name implements weather.Source.
func (f *Fake) Name() string { return "fake" }

// Forecast implements weather.Source.
func (f *Fake) Forecast(_ context.Context, p geo.Point, days int) (weather.Forecast, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return weather.Forecast{}, f.err
	}
	ds := f.days
	if len(ds) > days {
		ds = ds[:days]
	}
	return weather.Forecast{Point: p, Timezone: "Asia/Kolkata", Days: append([]weather.Day(nil), ds...), Source: "fake", Fetched: time.Time{}}, nil
}

// Week returns seven calm days from start (YYYY-MM-DD): 30/20 °C, no rain,
// light wind, 60 % humidity, 4 mm ET0. Change the days that matter.
func Week(start string) []weather.Day {
	t, err := time.Parse(weather.DateLayout, start)
	if err != nil {
		panic(err)
	}
	out := make([]weather.Day, 7)
	for i := range out {
		out[i] = weather.Day{Date: t.AddDate(0, 0, i).Format(weather.DateLayout), TMaxC: 30, TMinC: 20, WindKmh: 10, GustKmh: 20, HumidityPct: 60, ET0MM: 4}
	}
	return out
}

var _ weather.Source = (*Fake)(nil)
