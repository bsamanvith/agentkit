package feeds_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/feeds"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

func fixed(name string, r string, err error, calls *atomic.Int32) feeds.Provider[string, string] {
	return feeds.Func[string, string]{N: name, F: func(_ context.Context, q string) (string, error) {
		if calls != nil {
			calls.Add(1)
		}
		if err != nil {
			return "", err
		}
		return r + ":" + q, nil
	}}
}

func TestChainReturnsTheFirstAnswerAndPassesOverFailures(t *testing.T) {
	var second atomic.Int32
	c := feeds.Chain(nil,
		fixed("down", "", fmt.Errorf("%w: HTTP 503", feeds.ErrUnavailable), nil),
		fixed("empty", "", feeds.ErrNoData, nil),
		fixed("files", "f", nil, &second),
		fixed("never", "n", nil, nil),
	)
	got, err := c.Fetch(context.Background(), "q")
	if err != nil || got != "f:q" {
		t.Fatalf("Fetch = %q, %v; want f:q", got, err)
	}
	if c.Name() != "down, then empty, then files, then never" {
		t.Errorf("Name = %q", c.Name())
	}
}

func TestChainSaysNoDataOnlyWhenEveryProviderSaidSo(t *testing.T) {
	c := feeds.Chain(nil, fixed("a", "", feeds.ErrNoData, nil), fixed("b", "", feeds.ErrNoData, nil))
	_, err := c.Fetch(context.Background(), "q")
	if !errors.Is(err, feeds.ErrNoData) || !strings.Contains(err.Error(), "a:") || !strings.Contains(err.Error(), "b:") {
		t.Fatalf("err = %v; want ErrNoData naming both", err)
	}
	c = feeds.Chain(nil, fixed("a", "", feeds.ErrNoData, nil), fixed("b", "", feeds.ErrUnavailable, nil))
	if _, err := c.Fetch(context.Background(), "q"); errors.Is(err, feeds.ErrNoData) && !errors.Is(err, feeds.ErrUnavailable) {
		t.Fatalf("err = %v; want not only ErrNoData", err)
	}
	_, err = c.Fetch(context.Background(), "q")
	if strings.HasPrefix(err.Error(), feeds.ErrNoData.Error()) {
		t.Fatalf("err = %v; must not claim no data when one provider was down", err)
	}
}

func TestChainStopsOnACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var after atomic.Int32
	c := feeds.Chain(nil,
		feeds.Func[string, string]{N: "slow", F: func(context.Context, string) (string, error) {
			cancel()
			return "", context.Canceled
		}},
		fixed("next", "n", nil, &after))
	if _, err := c.Fetch(ctx, "q"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if after.Load() != 0 {
		t.Fatal("the next provider was asked after the context was cancelled")
	}
}

func TestPausedStopsAskingUntilTheRateLimitPasses(t *testing.T) {
	k := clocktest.NewFake(time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC))
	var calls atomic.Int32
	limited := true
	p := feeds.Paused[string, string](feeds.Func[string, string]{N: "api", F: func(context.Context, string) (string, error) {
		calls.Add(1)
		if limited {
			return "", &feeds.RateLimitError{RetryAfter: 10 * time.Minute}
		}
		return "ok", nil
	}}, k)
	ctx := context.Background()
	if _, err := p.Fetch(ctx, "q"); !errors.Is(err, feeds.ErrUnavailable) {
		t.Fatalf("err = %v; a rate limit is ErrUnavailable", err)
	}
	limited = false
	k.Advance(9 * time.Minute)
	if _, err := p.Fetch(ctx, "q"); err == nil || calls.Load() != 1 {
		t.Fatalf("asked again inside the pause: err %v, calls %d", err, calls.Load())
	}
	k.Advance(2 * time.Minute)
	if got, err := p.Fetch(ctx, "q"); err != nil || got != "ok" {
		t.Fatalf("after the pause: %q, %v", got, err)
	}
}

func TestPausedUsesTheDefaultPauseWhenNotTold(t *testing.T) {
	k := clocktest.NewFake(time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC))
	var calls atomic.Int32
	p := feeds.Paused[string, string](feeds.Func[string, string]{N: "api", F: func(context.Context, string) (string, error) {
		calls.Add(1)
		return "", &feeds.RateLimitError{}
	}}, k)
	_, _ = p.Fetch(context.Background(), "q")
	k.Advance(feeds.DefaultPause - time.Second)
	_, _ = p.Fetch(context.Background(), "q")
	if calls.Load() != 1 {
		t.Fatalf("calls = %d; want 1 inside the default pause", calls.Load())
	}
	k.Advance(2 * time.Second)
	_, _ = p.Fetch(context.Background(), "q")
	if calls.Load() != 2 {
		t.Fatalf("calls = %d; want 2 after it", calls.Load())
	}
}

func TestCachedReusesAnswersForTheirTTLAndNotErrors(t *testing.T) {
	k := clocktest.NewFake(time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC))
	var calls atomic.Int32
	fail := true
	p := feeds.Cached[string, string](feeds.Func[string, string]{N: "api", F: func(_ context.Context, q string) (string, error) {
		calls.Add(1)
		if fail {
			return "", feeds.ErrUnavailable
		}
		return "v" + q, nil
	}}, strings.ToLower, time.Hour, 2, k)
	ctx := context.Background()
	if _, err := p.Fetch(ctx, "a"); err == nil {
		t.Fatal("want the error")
	}
	fail = false
	if got, _ := p.Fetch(ctx, "a"); got != "va" || calls.Load() != 2 {
		t.Fatalf("an error was remembered: %q, calls %d", got, calls.Load())
	}
	if got, _ := p.Fetch(ctx, "A"); got != "va" || calls.Load() != 2 {
		t.Fatalf("same key asked again: %q, calls %d", got, calls.Load())
	}
	k.Advance(time.Hour)
	if _, _ = p.Fetch(ctx, "a"); calls.Load() != 3 {
		t.Fatalf("expired answer reused: calls %d", calls.Load())
	}
	// Full at 2: a third key pushes out the oldest, not the newest.
	k.Advance(time.Minute)
	_, _ = p.Fetch(ctx, "b")
	k.Advance(time.Minute)
	_, _ = p.Fetch(ctx, "c")
	before := calls.Load()
	_, _ = p.Fetch(ctx, "c")
	_, _ = p.Fetch(ctx, "b")
	if calls.Load() != before {
		t.Fatalf("the newest answers were evicted: calls %d, want %d", calls.Load(), before)
	}
	_, _ = p.Fetch(ctx, "a")
	if calls.Load() != before+1 {
		t.Fatalf("the oldest answer was kept: calls %d", calls.Load())
	}
}

func TestPickBuildsInOrderSkipsUnconfiguredAndRejectsUnknown(t *testing.T) {
	makers := map[string]func() (string, error){
		"datagov": func() (string, error) { return "", fmt.Errorf("%w: no key", feeds.ErrNotConfigured) },
		"files":   func() (string, error) { return "files", nil },
		"history": func() (string, error) { return "history", nil },
		"broken":  func() (string, error) { return "", errors.New("bad folder") },
	}
	got, err := feeds.Pick(nil, " history, datagov ,FILES,history", makers)
	if err != nil || strings.Join(got, ",") != "history,files" {
		t.Fatalf("Pick = %v, %v; want history,files", got, err)
	}
	if got, err := feeds.Pick(nil, "off", makers); err != nil || got != nil {
		t.Fatalf("off = %v, %v", got, err)
	}
	if _, err := feeds.Pick(nil, "files,agmark", makers); err == nil || !strings.Contains(err.Error(), "broken, datagov, files, history") {
		t.Fatalf("unknown name: err = %v; want the known names", err)
	}
	if _, err := feeds.Pick(nil, "broken", makers); err == nil || !strings.Contains(err.Error(), "bad folder") {
		t.Fatalf("broken: err = %v", err)
	}
}

func TestGetBodySortsFailuresForAChain(t *testing.T) {
	cases := []struct {
		status int
		header string
		is     error
		wait   time.Duration
	}{
		{http.StatusServiceUnavailable, "", feeds.ErrUnavailable, 0},
		{http.StatusForbidden, "", feeds.ErrUnavailable, 0},
		{http.StatusNotFound, "", feeds.ErrNoData, 0},
		{http.StatusTooManyRequests, "30", feeds.ErrUnavailable, 30 * time.Second},
		{http.StatusTooManyRequests, "", feeds.ErrUnavailable, 0},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Key") != "k" {
				t.Errorf("header not sent")
			}
			if tc.header != "" {
				w.Header().Set("Retry-After", tc.header)
			}
			w.WriteHeader(tc.status)
		}))
		_, err := feeds.GetBody(context.Background(), srv.Client(), srv.URL, http.Header{"X-Key": {"k"}})
		srv.Close()
		if !errors.Is(err, tc.is) {
			t.Errorf("%d: err = %v; want %v", tc.status, err, tc.is)
		}
		if tc.status == http.StatusTooManyRequests {
			rl, ok := errors.AsType[*feeds.RateLimitError](err)
			if !ok || rl.RetryAfter != tc.wait {
				t.Errorf("429 %q: %v; want wait %s", tc.header, err, tc.wait)
			}
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"a":1}`)) }))
	defer srv.Close()
	var v struct{ A int }
	if err := feeds.GetJSON(context.Background(), srv.Client(), srv.URL, nil, &v); err != nil || v.A != 1 {
		t.Fatalf("GetJSON = %v, %+v", err, v)
	}
	if err := feeds.GetJSON(context.Background(), srv.Client(), srv.URL, nil, &[]int{}); !errors.Is(err, feeds.ErrUnavailable) {
		t.Fatalf("bad JSON: err = %v", err)
	}
	if _, err := feeds.GetBody(context.Background(), srv.Client(), "http://127.0.0.1:1/", nil); !errors.Is(err, feeds.ErrUnavailable) {
		t.Fatalf("refused: err = %v", err)
	}
}
