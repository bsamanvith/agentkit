// Package feeds is the farm app's way to outside data that keeps changing
// hands: market prices, weather, news, knowledge. In India this space is
// fragmented, and providers come, go, change their terms or start refusing
// a server, so the app never depends on one. Every kind of data is a
// [Provider] of answers to a query, and a feature asks a [Chain] of them,
// picked by name from configuration ([Pick]).
//
// How a provider gets its data is its own business: an HTTP API ([GetJSON]),
// a file someone downloaded and dropped in a folder, a page read from a
// website, or a copy kept from earlier. The rest of the app only sees the
// query and the answer.
//
// The pieces:
//
//   - [Chain] asks providers in order and returns the first answer; a
//     provider with nothing for the query ([ErrNoData]) or that cannot
//     answer ([ErrUnavailable]) passes to the next, and the log says why.
//   - [Paused] stops asking a provider that said it is over its rate limit
//     ([RateLimitError]) until the limit has passed.
//   - [Cached] remembers answers for a while, so one page view or one agent
//     turn does not ask the same thing twice.
//   - [Pick] turns a comma list such as "datagov,files" into providers, in
//     that order, from a table of constructors.
//
// The package knows nothing about farms; apps/farm/prices is its first user.
package feeds

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
)

// ErrUnavailable is a provider that could not answer now: the network, a
// 5xx, a refused connection, a rate limit.
var ErrUnavailable = errors.New("feeds: the provider could not answer")

// ErrNoData is a provider that answered but has nothing for this query (a
// market it does not cover, a day with no trade).
var ErrNoData = errors.New("feeds: no data for this query")

// ErrNotConfigured is a provider that needs a setting it was not given (an
// API key, a folder).
var ErrNotConfigured = errors.New("feeds: the provider is not configured")

// RateLimitError is a provider over its rate limit, with how long to wait
// (0 when it did not say). It is an [ErrUnavailable].
type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("feeds: over the rate limit, retry after %s", e.RetryAfter)
	}
	return "feeds: over the rate limit"
}

// Is makes a RateLimitError an ErrUnavailable.
func (e *RateLimitError) Is(target error) bool { return target == ErrUnavailable }

// Provider answers queries of type Q with answers of type R. A provider is
// safe for concurrent use.
type Provider[Q, R any] interface {
	// Name says where the answers come from, for the farmer and the log.
	Name() string
	Fetch(ctx context.Context, q Q) (R, error)
}

// Func is a Provider from a function, for tests, fixed data and adapters.
type Func[Q, R any] struct {
	N string
	F func(context.Context, Q) (R, error)
}

// Name implements Provider.
func (f Func[Q, R]) Name() string { return f.N }

// Fetch implements Provider.
func (f Func[Q, R]) Fetch(ctx context.Context, q Q) (R, error) { return f.F(ctx, q) }

// Chain asks each provider in turn and returns the first answer. It passes
// to the next provider on any error but a cancelled context; when all fail
// the error joins theirs, each prefixed with its provider's name, and is an
// [ErrNoData] only when every provider said so. log (nil for none) hears
// why a provider was passed over.
func Chain[Q, R any](log *slog.Logger, ps ...Provider[Q, R]) Provider[Q, R] {
	if len(ps) == 1 {
		return ps[0]
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &chain[Q, R]{ps: ps, log: log}
}

type chain[Q, R any] struct {
	ps  []Provider[Q, R]
	log *slog.Logger
}

func (c *chain[Q, R]) Name() string {
	names := make([]string, len(c.ps))
	for i, p := range c.ps {
		names[i] = p.Name()
	}
	return strings.Join(names, ", then ")
}

func (c *chain[Q, R]) Fetch(ctx context.Context, q Q) (R, error) {
	var zero R
	if len(c.ps) == 0 {
		return zero, fmt.Errorf("%w: no providers", ErrNotConfigured)
	}
	errs := make([]error, 0, len(c.ps))
	noData := true
	for i, p := range c.ps {
		r, err := p.Fetch(ctx, q)
		if err == nil {
			return r, nil
		}
		if ctx.Err() != nil {
			return zero, err
		}
		if !errors.Is(err, ErrNoData) {
			noData = false
		}
		errs = append(errs, fmt.Errorf("%s: %w", p.Name(), err))
		if i+1 < len(c.ps) {
			c.log.InfoContext(ctx, "feeds: trying the next provider", "provider", p.Name(), "next", c.ps[i+1].Name(), "err", err)
		}
	}
	err := errors.Join(errs...)
	if noData {
		return zero, fmt.Errorf("%w: %w", ErrNoData, err)
	}
	return zero, err
}

// DefaultPause is how long [Paused] stops asking after a rate limit that
// did not say how long.
const DefaultPause = time.Hour

// Paused wraps p so that after it returns a [RateLimitError] it is not asked
// again until the limit has passed (its RetryAfter, or [DefaultPause]);
// meanwhile Fetch fails at once with a RateLimitError, so a [Chain] moves on
// without waiting.
func Paused[Q, R any](p Provider[Q, R], k clock.Clock) Provider[Q, R] {
	if k == nil {
		k = clock.Real()
	}
	return &paused[Q, R]{p: p, clock: k}
}

type paused[Q, R any] struct {
	p     Provider[Q, R]
	clock clock.Clock
	mu    sync.Mutex
	until time.Time
}

func (p *paused[Q, R]) Name() string { return p.p.Name() }

func (p *paused[Q, R]) Fetch(ctx context.Context, q Q) (R, error) {
	var zero R
	now := p.clock.Now()
	p.mu.Lock()
	until := p.until
	p.mu.Unlock()
	if now.Before(until) {
		return zero, &RateLimitError{RetryAfter: until.Sub(now)}
	}
	r, err := p.p.Fetch(ctx, q)
	if rl, ok := errors.AsType[*RateLimitError](err); ok {
		wait := rl.RetryAfter
		if wait <= 0 {
			wait = DefaultPause
		}
		p.mu.Lock()
		p.until = p.clock.Now().Add(wait)
		p.mu.Unlock()
	}
	return r, err
}

// Cached wraps p so that an answer is reused for ttl. key turns a query into
// the cache's key; queries with the same key get the same answer. Errors
// are not remembered. At most max answers are kept (0 for 1000); when full,
// the oldest goes.
func Cached[Q, R any](p Provider[Q, R], key func(Q) string, ttl time.Duration, max int, k clock.Clock) Provider[Q, R] {
	if k == nil {
		k = clock.Real()
	}
	if max <= 0 {
		max = 1000
	}
	return &cached[Q, R]{p: p, key: key, ttl: ttl, max: max, clock: k, m: map[string]entry[R]{}}
}

type entry[R any] struct {
	r  R
	at time.Time
}

type cached[Q, R any] struct {
	p     Provider[Q, R]
	key   func(Q) string
	ttl   time.Duration
	max   int
	clock clock.Clock
	mu    sync.Mutex
	m     map[string]entry[R]
}

func (c *cached[Q, R]) Name() string { return c.p.Name() }

func (c *cached[Q, R]) Fetch(ctx context.Context, q Q) (R, error) {
	k := c.key(q)
	now := c.clock.Now()
	c.mu.Lock()
	if e, ok := c.m[k]; ok && now.Sub(e.at) < c.ttl {
		c.mu.Unlock()
		return e.r, nil
	}
	c.mu.Unlock()
	r, err := c.p.Fetch(ctx, q)
	if err != nil {
		return r, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= c.max {
		c.evict(now)
	}
	c.m[k] = entry[R]{r: r, at: now}
	return r, nil
}

// evict drops expired answers, or the oldest one if none has expired.
func (c *cached[Q, R]) evict(now time.Time) {
	var oldest string
	var oldestAt time.Time
	for k, e := range c.m {
		if now.Sub(e.at) >= c.ttl {
			delete(c.m, k)
			continue
		}
		if oldest == "" || e.at.Before(oldestAt) {
			oldest, oldestAt = k, e.at
		}
	}
	if len(c.m) >= c.max && oldest != "" {
		delete(c.m, oldest)
	}
}

// Pick builds the providers a comma list names, in its order, from makers: a
// table of constructors by name. "off" or "" picks none. A name not in the
// table is an error that lists the known names. A constructor that returns
// [ErrNotConfigured] is skipped with a log line (a provider whose key is
// not set), so a default list can name providers an operator may not have
// set up.
func Pick[T any](log *slog.Logger, list string, makers map[string]func() (T, error)) ([]T, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	list = strings.TrimSpace(list)
	if list == "" || list == "off" {
		return nil, nil
	}
	var out []T
	seen := map[string]bool{}
	for _, name := range strings.Split(list, ",") {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		mk, ok := makers[name]
		if !ok {
			return nil, fmt.Errorf("feeds: unknown provider %q (known: %s)", name, strings.Join(slices.Sorted(maps.Keys(makers)), ", "))
		}
		t, err := mk()
		if errors.Is(err, ErrNotConfigured) {
			log.Info("feeds: provider not configured, skipped", "provider", name, "why", err)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("feeds: %s: %w", name, err)
		}
		out = append(out, t)
	}
	return out, nil
}
