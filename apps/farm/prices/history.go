package prices

import (
	"context"
	"log/slog"
	"slices"
	"sync"

	"github.com/bsamanvith/agentkit/apps/farm/feeds"
)

// History keeps quotes. A quote is one per day, market, commodity,
// variety and grade: saving it again replaces it. Implementations are safe
// for concurrent use; apps/farm/prices/pricesqlite keeps them in a file.
type History interface {
	// Save keeps the valid quotes and returns how many were new.
	Save(ctx context.Context, qs []Quote) (int, error)
	// Quotes returns the quotes that match q, newest first, at most
	// q.Limit of them.
	Quotes(ctx context.Context, q Query) ([]Quote, error)
}

// QuoteKey is what makes a quote one of a kind in a [History].
func QuoteKey(x Quote) string {
	return x.Date.String() + "|" + Fold(x.State) + "|" + Fold(x.District) + "|" + Fold(x.Market) + "|" + Fold(x.Commodity) + "|" + Fold(x.Variety) + "|" + Fold(x.Grade)
}

// NewMemoryHistory returns a History in memory, for tests and for running
// without a file.
func NewMemoryHistory() *MemoryHistory { return &MemoryHistory{m: map[string]Quote{}} }

// MemoryHistory is a History in memory.
type MemoryHistory struct {
	mu sync.Mutex
	m  map[string]Quote
}

// Save implements History.
func (h *MemoryHistory) Save(_ context.Context, qs []Quote) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, x := range qs {
		if x.Valid() != nil {
			continue
		}
		k := QuoteKey(x)
		if _, ok := h.m[k]; !ok {
			n++
		}
		h.m[k] = x
	}
	return n, nil
}

// Quotes implements History.
func (h *MemoryHistory) Quotes(_ context.Context, q Query) ([]Quote, error) {
	q, err := q.Normalized()
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	var out []Quote
	for _, x := range h.m {
		if q.Matches(x) {
			out = append(out, x)
		}
	}
	h.mu.Unlock()
	slices.SortFunc(out, newestFirst)
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// Saved is a Source on a History: the prices any provider gave before.
func Saved(h History) Source {
	return feeds.Func[Query, []Quote]{N: "saved prices", F: func(ctx context.Context, q Query) ([]Quote, error) {
		qs, err := h.Quotes(ctx, q)
		if err != nil {
			return nil, err
		}
		if len(qs) == 0 {
			return nil, feeds.ErrNoData
		}
		return qs, nil
	}}
}

// Recorded wraps src so that every answer it gives is saved in h, which is
// how the history grows. A failed save is logged (log may be nil), not
// returned: the farmer still gets today's prices.
func Recorded(src Source, h History, log *slog.Logger) Source {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return feeds.Func[Query, []Quote]{N: src.Name(), F: func(ctx context.Context, q Query) ([]Quote, error) {
		qs, err := src.Fetch(ctx, q)
		if err != nil {
			return qs, err
		}
		if _, serr := h.Save(ctx, qs); serr != nil {
			log.WarnContext(ctx, "prices: saving quotes", "source", src.Name(), "err", serr)
		}
		return qs, nil
	}}
}
