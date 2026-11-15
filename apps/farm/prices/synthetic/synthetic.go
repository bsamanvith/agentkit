// Package synthetic makes synthetic mandi prices with a known seasonal
// pattern, noise, a trend and regime breaks, for testing the seasonal
// index, the timing advice and its backtest, and for running the backtest
// command without real files. Every price it makes is synthetic: its source
// is "synthetic" and its markets are named so.
package synthetic

import (
	"math"
	"math/rand/v2"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/prices"
)

// Break is a regime break: from At on, prices are Level times what they
// were, and, when Season is set, follow that seasonal pattern instead.
type Break struct {
	At     prices.Month
	Level  float64
	Season *[12]float64
}

// Series describes a synthetic price history.
type Series struct {
	Commodity string
	State     string
	District  string
	// Markets is how many markets report (default 2); each quotes on the
	// 5th, 15th and 25th of every month.
	Markets int
	// From is the first month and Years how many years follow.
	From  prices.Month
	Years int
	// Base is the price level at From, rupees a quintal.
	Base float64
	// Season is each month's price over the year's average, January
	// first; zero for no seasonality (all 1).
	Season [12]float64
	// TrendPct is the level's growth a year, in percent.
	TrendPct float64
	// Noise is the standard deviation of each month's log price shock
	// (shared by its markets), and MarketNoise that of each quote.
	Noise       float64
	MarketNoise float64
	Breaks      []Break
	// Gaps are months with no quotes.
	Gaps []prices.Month
	Seed uint64
}

// Quotes returns the series' quotes, oldest first.
func (s Series) Quotes() []prices.Quote {
	if s.Markets <= 0 {
		s.Markets = 2
	}
	rng := rand.New(rand.NewPCG(s.Seed, s.Seed^0x9e3779b97f4a7c15))
	season := s.Season
	if season == ([12]float64{}) {
		season = flat()
	}
	level := s.Base
	gaps := map[prices.Month]bool{}
	for _, g := range s.Gaps {
		gaps[g] = true
	}
	var out []prices.Quote
	for i := range s.Years * 12 {
		m := s.From.Add(i)
		for _, b := range s.Breaks {
			if m == b.At {
				level *= b.Level
				if b.Season != nil {
					season = *b.Season
				}
			}
		}
		shock := rng.NormFloat64() * s.Noise
		trend := math.Pow(1+s.TrendPct/100, float64(i)/12)
		if gaps[m] {
			continue
		}
		for mk := range s.Markets {
			for _, d := range []int{5, 15, 25} {
				p := level * trend * season[m.Month-1] * math.Exp(shock+rng.NormFloat64()*s.MarketNoise)
				p = math.Round(p)
				out = append(out, prices.Quote{
					Date: prices.NewDay(m.Year, m.Month, d), State: s.State, District: s.District,
					Market: "Synthetic market " + string(rune('A'+mk)), Commodity: s.Commodity,
					Min: math.Round(p * 0.9), Max: math.Round(p * 1.1), Modal: p, Source: "synthetic",
				})
			}
		}
	}
	return out
}

// Monthly returns the series' monthly prices.
func (s Series) Monthly() []prices.MonthlyPrice { return prices.Monthly(s.Quotes()) }

func flat() [12]float64 {
	var f [12]float64
	for i := range f {
		f[i] = 1
	}
	return f
}

// PostHarvestDip is a storable kharif crop's pattern: prices lowest in the
// harvest months (October to December, 0.84 to 0.88) and highest before
// the next harvest (June to August, up to 1.15). Its months average 1.
var PostHarvestDip = [12]float64{0.95, 0.98, 1.02, 1.05, 1.08, 1.12, 1.15, 1.10, 0.97, 0.88, 0.84, 0.86}

// Reversed is PostHarvestDip turned around: what a regime break to a new
// season (an import window, a second crop) can do.
var Reversed = func() [12]float64 {
	var r [12]float64
	for i, v := range PostHarvestDip {
		r[(i+6)%12] = v
	}
	return r
}()

// Start is a month to start series at.
var Start = prices.Month{Year: 2010, Month: time.January}
