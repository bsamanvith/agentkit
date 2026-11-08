package prices

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/feeds"
	"github.com/bsamanvith/agentkit/pkg/clock"
)

// CacheTTL is how long [Service] reuses a live answer. Mandis report once
// a day, mostly in the afternoon and evening.
const CacheTTL = 3 * time.Hour

// Service is the farm app's prices: live providers in order, the history
// every answer is saved in, and price files imported into it.
type Service struct {
	live    []Source
	history History
	files   *FileSource
	source  Source // the live providers then the history, cached
	log     *slog.Logger
	clock   clock.Clock
}

// Option configures [New].
type Option func(*Service)

// WithFiles imports f's price files into the history ([Service.Import]).
func WithFiles(f *FileSource) Option { return func(s *Service) { s.files = f } }

// WithLogger logs passed-over providers and failed saves.
func WithLogger(l *slog.Logger) Option { return func(s *Service) { s.log = l } }

// WithClock sets the clock of the cache, the rate-limit pauses and "today".
func WithClock(k clock.Clock) Option { return func(s *Service) { s.clock = k } }

// New returns a Service that asks live in order (each paused after a rate
// limit and its answers saved in h), then h itself.
func New(h History, live []Source, opts ...Option) *Service {
	s := &Service{history: h, log: slog.New(slog.DiscardHandler), clock: clock.Real()}
	for _, o := range opts {
		o(s)
	}
	chain := make([]Source, 0, len(live)+1)
	for _, src := range live {
		p := feeds.Paused(src, s.clock)
		s.live = append(s.live, p)
		chain = append(chain, Recorded(p, h, s.log))
	}
	chain = append(chain, Saved(h))
	s.source = feeds.Cached(feeds.Chain(s.log, chain...), Query.Key, CacheTTL, 500, s.clock)
	return s
}

// Today returns the latest prices for q, live if a provider answers, else
// the newest saved. With a district or market that has none, it widens to
// the state and says so.
func (s *Service) Today(ctx context.Context, q Query) (Answer, error) {
	q, err := q.Normalized()
	if err != nil {
		return Answer{}, err
	}
	qs, err := s.source.Fetch(ctx, q)
	a := Answer{Query: q}
	if errors.Is(err, feeds.ErrNoData) && (q.District != "" || q.Market != "") && q.State != "" {
		wide := q
		wide.District, wide.Market = "", ""
		if qs, err = s.source.Fetch(ctx, wide); err == nil {
			a.Widened = fmt.Sprintf("No prices for %s in %s; these are from across %s.", q.Commodity, place(q), stateName(q.State))
		}
	}
	if errors.Is(err, feeds.ErrNoData) {
		return Answer{Query: q}, &NoPricesError{What: describe(q), err: err}
	}
	if err != nil {
		return Answer{}, err
	}
	a.Quotes = qs
	a.AsOf = qs[0].Date
	a.Source = qs[0].Source
	a.DaysOld = DayOf(s.clock.Now()).DaysSince(a.AsOf)
	a.Markets = Latest(qs)
	if a.Widened == "" && (q.District != "" || q.Market != "") && q.State != "" {
		// The farmer may sell across a district line: the rest of the
		// state's markets, best effort.
		wide := q
		wide.District, wide.Market = "", ""
		if all, err := s.source.Fetch(ctx, wide); err == nil {
			near := map[string]bool{}
			for _, x := range a.Markets {
				near[Fold(x.Market)] = true
			}
			for _, x := range Latest(all) {
				if !near[Fold(x.Market)] && !x.Date.Before(a.AsOf.AddDays(-7)) && len(a.Elsewhere) < 10 {
					a.Elsewhere = append(a.Elsewhere, x)
				}
			}
		}
	}
	return a, nil
}

// Answer is [Service.Today]'s answer.
type Answer struct {
	Query  Query   `json:"query"`
	Quotes []Quote `json:"quotes"`
	// Markets is the newest quote of each market and variety, highest
	// modal price first.
	Markets []Quote `json:"markets"`
	AsOf    Day     `json:"as_of"`
	DaysOld int     `json:"days_old"`
	Source  string  `json:"source"`
	// Widened says when the answer is from a wider area than asked.
	Widened string `json:"widened,omitempty"`
	// Elsewhere is the newest quote of up to ten other markets in the
	// state (from the week before AsOf or later), highest first, when a
	// district or market was asked.
	Elsewhere []Quote `json:"elsewhere,omitempty"`
}

// Latest keeps the newest quote of each market, commodity and variety,
// highest modal price first.
func Latest(qs []Quote) []Quote {
	seen := map[string]bool{}
	var out []Quote
	for _, x := range qs {
		k := Fold(x.Market) + "|" + Fold(x.Commodity) + "|" + Fold(x.Variety) + "|" + Fold(x.Grade)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, x)
	}
	slices.SortStableFunc(out, func(a, b Quote) int {
		if a.Modal != b.Modal {
			if a.Modal > b.Modal {
				return -1
			}
			return 1
		}
		return newestFirst(a, b)
	})
	return out
}

// History returns saved quotes for q, newest first: what the price did.
func (s *Service) History(ctx context.Context, q Query) ([]Quote, error) {
	return s.history.Quotes(ctx, q)
}

// Estimate estimates what a crop may sell for. It first asks the live
// providers for the commodity's latest prices in the state, so the
// estimate starts from today's level when a provider answers.
func (s *Service) Estimate(ctx context.Context, q EstimateQuery) (Estimate, error) {
	if q.Commodity != "" && q.State != "" && len(s.live) > 0 {
		if _, err := s.source.Fetch(ctx, Query{State: q.State, Commodity: Commodity(q.Commodity), Limit: DefaultLimit}); err != nil && ctx.Err() == nil {
			s.log.InfoContext(ctx, "prices: no live prices before the estimate", "err", err)
		}
	}
	return EstimatePrice(ctx, s.history, q, DayOf(s.clock.Now()))
}

// Import reads the price files into the history and says what it found
// and how many quotes were new. Without files it does nothing.
func (s *Service) Import(ctx context.Context) (FileStats, int, error) {
	if s.files == nil {
		return FileStats{}, 0, nil
	}
	all, stats, err := s.files.All(ctx)
	if err != nil {
		return stats, 0, err
	}
	n, err := s.history.Save(ctx, all)
	return stats, n, err
}

// Providers names the live providers in the order they are asked, then
// where the history comes from.
func (s *Service) Providers() []string {
	out := make([]string, 0, len(s.live)+2)
	for _, p := range s.live {
		out = append(out, p.Name())
	}
	if s.files != nil && !slices.Contains(out, s.files.Name()) {
		out = append(out, s.files.Name())
	}
	return append(out, "saved prices")
}

func place(q Query) string {
	switch {
	case q.Market != "":
		return q.Market
	case q.District != "":
		return q.District + " district"
	}
	return stateName(q.State)
}

func describe(q Query) string {
	what := q.Commodity
	if what == "" {
		what = "any crop"
	}
	if q.State == "" && q.District == "" && q.Market == "" {
		return what
	}
	where := place(q)
	if q.State != "" && (q.District != "" || q.Market != "") {
		where += ", " + stateName(q.State)
	}
	return what + " in " + where
}

// NoPricesError is a query no provider had prices for. It is a
// feeds.ErrNoData, and its text is fit for the farmer.
type NoPricesError struct {
	What string // "Tomato in Kolar district, Karnataka"
	err  error
}

func (e *NoPricesError) Error() string { return "no mandi prices found for " + e.What }

// Is makes a NoPricesError a feeds.ErrNoData.
func (e *NoPricesError) Is(target error) bool { return target == feeds.ErrNoData }

// Unwrap gives the providers' own errors.
func (e *NoPricesError) Unwrap() error { return e.err }
