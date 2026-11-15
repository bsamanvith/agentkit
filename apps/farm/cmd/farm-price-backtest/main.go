// Command farm-price-backtest replays the farm app's timing advice on years
// of mandi prices and prints how it did against the naive baselines, crop
// by crop and area by area: the sell-or-hold rule from the harvest month,
// and the harvest-month rule for the sowing date.
//
//	go run ./apps/farm/cmd/farm-price-backtest -files <folder>
//	go run ./apps/farm/cmd/farm-price-backtest -synthetic
//
// The folder holds price files the farm app reads (Agmarknet's export,
// data.gov.in's CSV, India Data Portal's and CEDA's Agmarknet files), of
// any size. With -synthetic it runs on made-up series with a known season,
// noise and regime breaks instead (make farm-price-backtest without
// FARM_PRICE_FILES), which proves the method and the gate, not the
// markets.
package main

import (
	"cmp"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/prices"
	"github.com/bsamanvith/agentkit/apps/farm/prices/synthetic"
)

// crop is a crop the backtest runs, with a typical sowing window in the
// southern states (kharif unless said): a starter for the backtest only;
// the app takes the window from the recommender.
type crop struct {
	id       string
	from, to [2]int // month, day
}

var crops = []crop{
	{"groundnut", [2]int{6, 15}, [2]int{7, 31}},
	{"pigeon_pea", [2]int{6, 15}, [2]int{7, 31}},
	{"cotton", [2]int{6, 1}, [2]int{7, 15}},
	{"onion", [2]int{6, 15}, [2]int{7, 31}},
	{"tomato", [2]int{10, 1}, [2]int{11, 15}}, // rabi
	{"maize", [2]int{6, 15}, [2]int{7, 31}},
	{"chilli", [2]int{7, 15}, [2]int{8, 31}},
	{"turmeric", [2]int{6, 1}, [2]int{7, 15}},
	{"chickpea", [2]int{10, 15}, [2]int{11, 30}}, // rabi
}

func main() {
	files := flag.String("files", os.Getenv("FARM_PRICE_FILES"), "folder of price files (FARM_PRICE_FILES)")
	synthetic := flag.Bool("synthetic", false, "run on synthetic series instead of files")
	states := flag.String("states", "", "comma list of states to keep (empty: all)")
	minMonths := flag.Int("min-months", 30, "least months of prices an area needs to be tested")
	minYears := flag.Int("min-years", 0, "years a rule must be tested on to pass the gate (0: the default)")
	flag.Parse()
	t := prices.Tuning{MinBacktestYears: *minYears}
	if err := run(context.Background(), os.Stdout, *files, *synthetic, *states, *minMonths, t); err != nil {
		fmt.Fprintln(os.Stderr, "farm-price-backtest:", err)
		os.Exit(1)
	}
}

type key struct{ commodity, state, district string }

func run(ctx context.Context, w io.Writer, dir string, synthetic bool, states string, minMonths int, t prices.Tuning) error {
	lib, err := agro.Default()
	if err != nil {
		return err
	}
	byName := map[string]crop{}
	for _, c := range crops {
		byName[prices.Fold(prices.Commodity(c.id))] = c
	}
	values := map[key]map[prices.Month][]float64{}
	add := func(k key, x prices.Quote) {
		if values[k] == nil {
			values[k] = map[prices.Month][]float64{}
		}
		m := prices.MonthOf(x.Date)
		values[k][m] = append(values[k][m], x.Modal)
	}
	keep := map[string]bool{}
	for s := range strings.SplitSeq(states, ",") {
		if s = prices.Fold(s); s != "" {
			keep[s] = true
		}
	}
	each := func(x prices.Quote) error {
		c := prices.Fold(x.Commodity)
		if _, ok := byName[c]; !ok || len(keep) > 0 && !keep[prices.Fold(x.State)] {
			return nil
		}
		if !synthetic {
			add(key{c, x.State, ""}, x)
		}
		add(key{c, x.State, x.District}, x)
		return nil
	}
	source := "synthetic series (made up: a known season, noise and regime breaks)"
	switch {
	case synthetic:
		for _, s := range syntheticSeries() {
			for _, x := range s.Quotes() {
				_ = each(x)
			}
		}
	case dir == "":
		return fmt.Errorf("no folder: set FARM_PRICE_FILES or -files, or pass -synthetic")
	default:
		stats, err := prices.ScanFiles(ctx, dir, each)
		if err != nil {
			return err
		}
		source = fmt.Sprintf("%s: %d file(s), %d quotes read, %d rows skipped", dir, stats.Files, stats.Quotes, stats.Skipped)
		for _, e := range stats.Errors {
			fmt.Fprintln(os.Stderr, "farm-price-backtest:", e)
		}
	}
	keys := make([]key, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b key) int {
		return cmp.Or(strings.Compare(a.commodity, b.commodity), strings.Compare(a.state, b.state), strings.Compare(a.district, b.district))
	})
	fmt.Fprintf(w, "Walk-forward backtest of the farm app's timing advice on %s.\n", source)
	fmt.Fprintln(w, "Gains are in % of the harvest month's price, a year's mean; hit = share of years strictly better; MAPE of the later months' prices forecast from the harvest month's.")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "crop\tarea\tmonths\trule\tfrom\tyears\tvs harvest\tvs last year\thit vs harvest\thit vs last year\tMAPE\tnaive MAPE\tgate\t")
	tested, enough, passed := 0, 0, 0
	minYears := cmp.Or(t.MinBacktestYears, 3)
	for _, k := range keys {
		series := monthly(values[k])
		if len(series) < minMonths {
			continue
		}
		c := byName[k.commodity]
		cr, err := lib.Crop(c.id)
		if err != nil {
			return err
		}
		lo, hi := int(cr.Days[0]), int(cr.Days[1])
		year := series[0].Year
		win := prices.SowingWindow{From: prices.NewDay(year, time.Month(c.from[0]), c.from[1]), To: prices.NewDay(year, time.Month(c.to[0]), c.to[1])}
		sow, err := prices.BacktestSowing(series, lo, hi, win, t)
		if err != nil {
			return err
		}
		area := k.state
		if k.district != "" {
			area = k.district + ", " + k.state
		}
		results := []prices.BacktestResult{sow}
		if st := prices.StorageFor(k.commodity); st.Storable() {
			results = append(results, prices.BacktestSell(series, sow.Reference, st, t))
		}
		for _, r := range results {
			gate := "no advice"
			if r.Passed {
				gate = "SHOWN"
				passed++
			}
			tested++
			if r.N >= minYears {
				enough++
			}
			fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%d\t%+.1f%%\t%+.1f%%\t%.0f%%\t%.0f%%\t%.1f%%\t%.1f%%\t%s\t\n",
				cr.ID, area, len(series), r.Decision, r.Reference.String()[:3], r.N, r.MeanGainVsHarvestPct, r.MeanGainVsNaivePct,
				r.HitRateVsHarvest*100, r.HitRateVsNaive*100, r.MAPEPct, r.NaiveMAPEPct, gate)
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(w, "%d rule(s) tried on an area, %d with %d or more years to test, %d passed the gate (beat both baselines over at least %d years).\n", tested, enough, minYears, passed, minYears)
	return nil
}

func monthly(m map[prices.Month][]float64) []prices.MonthlyPrice {
	out := make([]prices.MonthlyPrice, 0, len(m))
	for mo, vs := range m {
		out = append(out, prices.MonthlyPrice{Month: mo, Price: prices.Median(vs), Quotes: len(vs)})
	}
	slices.SortFunc(out, func(a, b prices.MonthlyPrice) int { return a.Since(b.Month) })
	return out
}

// syntheticSeries are the made-up histories -synthetic runs: a clear
// post-harvest dip, the same buried in noise, no season at all, and a
// season that turns round halfway (a regime break).
func syntheticSeries() []synthetic.Series {
	rev := synthetic.Reversed
	base := synthetic.Series{State: "Synthetic", Markets: 3, From: synthetic.Start, Years: 15, TrendPct: 4, MarketNoise: 0.03}
	mk := func(commodity, district string, f func(*synthetic.Series)) synthetic.Series {
		s := base
		s.Commodity, s.District = commodity, district
		f(&s)
		return s
	}
	return []synthetic.Series{
		mk("Groundnut", "clear season", func(s *synthetic.Series) {
			s.Base, s.Season, s.Noise, s.Seed = 5000, synthetic.PostHarvestDip, 0.04, 1
		}),
		mk("Groundnut", "noisy season", func(s *synthetic.Series) {
			s.Base, s.Season, s.Noise, s.Seed = 5000, synthetic.PostHarvestDip, 0.15, 2
		}),
		mk("Arhar (Tur/Red Gram)(Whole)", "no season", func(s *synthetic.Series) {
			s.Base, s.Noise, s.Seed = 7000, 0.05, 3
		}),
		mk("Arhar (Tur/Red Gram)(Whole)", "season turns round", func(s *synthetic.Series) {
			s.Base, s.Season, s.Noise, s.Seed = 7000, synthetic.PostHarvestDip, 0.04, 4
			s.Breaks = []synthetic.Break{{At: s.From.Add(7 * 12), Level: 1.3, Season: &rev}}
		}),
		mk("Cotton", "level breaks", func(s *synthetic.Series) {
			s.Base, s.Season, s.Noise, s.Seed = 6000, synthetic.PostHarvestDip, 0.05, 5
			s.Breaks = []synthetic.Break{{At: s.From.Add(4 * 12), Level: 1.4}, {At: s.From.Add(9*12 + 3), Level: 0.7}}
		}),
		mk("Tomato", "clear season", func(s *synthetic.Series) {
			s.Base, s.Season, s.Noise, s.Seed = 1500, synthetic.PostHarvestDip, 0.25, 6
		}),
	}
}
