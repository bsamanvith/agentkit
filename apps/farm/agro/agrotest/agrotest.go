// Package agrotest has fixtures for the crop planning rules: a recorded
// climate and a fake climate source.
package agrotest

import (
	"context"
	"sync"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
)

// Farooqnagar is a point in the Deccan near Hyderabad (Ranga Reddy
// district, Telangana).
var Farooqnagar = geo.Point{Lat: 17.0716, Lng: 78.204}

// Hyderabad returns NASA POWER's climate normals at Farooqnagar, recorded
// 2026-10-31: a hot, dry April and May, the monsoon from June to
// September, and a mild, dry winter.
func Hyderabad() agro.Climate {
	return agro.Climate{
		Point:  Farooqnagar,
		Source: "NASA POWER climatology, recorded",
		Months: [12]agro.Month{
			{Temp: 21.9, TempMax: 35.48, TempMin: 9.06, RainMM: 6.2, Humidity: 51.83},     // Jan
			{Temp: 24.97, TempMax: 39.08, TempMin: 11.05, RainMM: 4.2, Humidity: 42.26},   // Feb
			{Temp: 28.58, TempMax: 41.14, TempMin: 15.24, RainMM: 19.2, Humidity: 36.29},  // Mar
			{Temp: 31.43, TempMax: 43.34, TempMin: 16.52, RainMM: 25.5, Humidity: 37.85},  // Apr
			{Temp: 32.63, TempMax: 43.98, TempMin: 20.96, RainMM: 36, Humidity: 39.48},    // May
			{Temp: 27.91, TempMax: 43.51, TempMin: 20.05, RainMM: 101.4, Humidity: 65.61}, // Jun
			{Temp: 25.96, TempMax: 37.34, TempMin: 19.9, RainMM: 151.9, Humidity: 74.94},  // Jul
			{Temp: 25.12, TempMax: 35.88, TempMin: 18.55, RainMM: 166.2, Humidity: 80.16}, // Aug
			{Temp: 24.83, TempMax: 35.17, TempMin: 18.48, RainMM: 154.5, Humidity: 82.62}, // Sep
			{Temp: 23.8, TempMax: 36.68, TempMin: 13.57, RainMM: 88, Humidity: 78.53},     // Oct
			{Temp: 22.04, TempMax: 33.91, TempMin: 9.5, RainMM: 14.1, Humidity: 71.49},    // Nov
			{Temp: 20.95, TempMax: 34.91, TempMin: 8.56, RainMM: 2.2, Humidity: 61.85},    // Dec
		},
	}
}

// Source is a fake agro.ClimateSource: Climate returns Clim (with the
// point asked for), or Err when set. It counts the calls.
type Source struct {
	Clim agro.Climate
	Err  error

	mu    sync.Mutex
	calls int
}

// Climate implements agro.ClimateSource.
func (s *Source) Climate(_ context.Context, p geo.Point) (agro.Climate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.Err != nil {
		return agro.Climate{}, s.Err
	}
	c := s.Clim
	c.Point = p
	return c, nil
}

// Calls returns how many times Climate was called.
func (s *Source) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}
