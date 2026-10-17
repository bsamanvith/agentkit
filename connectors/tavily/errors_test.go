package tavily_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/connectors/tavily"
)

// TestInvalidInput: input the model got wrong is ErrInvalidInput (and not
// ErrToolFailed), and nothing is sent to Tavily.
func TestInvalidInput(t *testing.T) {
	t.Parallel()
	many := func(n int, f func(i int) string) string {
		var parts []string
		for i := range n {
			parts = append(parts, f(i))
		}
		return strings.Join(parts, ", ")
	}
	for _, c := range []struct {
		name, settings, tool, input string
	}{
		{"empty query", "", "tavily_search", `{"query": ""}`},
		{"blank query", "", "tavily_search", `{"query": "   "}`},
		{"no query", "", "tavily_search", `{}`},
		{"query too long", "", "tavily_search", `{"query": "` + strings.Repeat("é", 2001) + `"}`},
		{"max_results 0", "", "tavily_search", `{"query": "go", "max_results": 0}`},
		{"max_results 21", "", "tavily_search", `{"query": "go", "max_results": 21}`},
		{"max_results negative", "", "tavily_search", `{"query": "go", "max_results": -1}`},
		{"max_results not a number", "", "tavily_search", `{"query": "go", "max_results": "5"}`},
		{"bad topic", "", "tavily_search", `{"query": "go", "topic": "sports"}`},
		{"bad time_range", "", "tavily_search", `{"query": "go", "time_range": "d"}`},
		{"search_depth not offered", "", "tavily_search", `{"query": "go", "search_depth": "advanced"}`},
		{"bad search_depth", `{"search_defaults": {"model_chooses_depth": true}}`, "tavily_search", `{"query": "go", "search_depth": "deep"}`},
		{"bad domain", "", "tavily_search", `{"query": "go", "include_domains": ["https://go.dev/doc"]}`},
		{"too many domains", "", "tavily_search", `{"query": "go", "exclude_domains": [` + many(51, func(i int) string { return fmt.Sprintf(`"d%d.com"`, i) }) + `]}`},
		{"unknown field", "", "tavily_search", `{"query": "go", "country": "france"}`},
		{"not JSON", "", "tavily_search", `{"query": `},
		{"no urls", "", "tavily_extract", `{"urls": []}`},
		{"urls missing", "", "tavily_extract", `{}`},
		{"11 urls", "", "tavily_extract", `{"urls": [` + many(11, func(i int) string { return fmt.Sprintf(`"https://example.com/%d"`, i) }) + `]}`},
		{"ftp url", "", "tavily_extract", `{"urls": ["ftp://example.com/file"]}`},
		{"javascript url", "", "tavily_extract", `{"urls": ["https://go.dev", "javascript:alert(1)"]}`},
		{"relative url", "", "tavily_extract", `{"urls": ["/doc/devel/release"]}`},
		{"url without host", "", "tavily_extract", `{"urls": ["https:///path"]}`},
		{"url with credentials", "", "tavily_extract", `{"urls": ["https://user:pass@example.com/"]}`},
		{"url too long", "", "tavily_extract", `{"urls": ["https://example.com/` + strings.Repeat("a", 2048) + `"]}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, rigOpts{settings: c.settings})
			_, err := r.call(c.tool, c.input)
			if !isInvalidInput(err) {
				t.Errorf("err = %v, want ErrInvalidInput only", err)
			}
			if n := len(r.up.requests()); n != 0 {
				t.Errorf("%d requests sent, want none", n)
			}
		})
	}
}

// TestUpstreamErrors: Tavily's own error bodies (recorded) give the error
// class E10 says, with the text the model needs and nothing else, and
// without a retry. The generic matrix (403, 5xx, garbage, the 300-character
// cap, refused connections, a hang) is TestConformance/errors.
func TestUpstreamErrors(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name     string
		settings string
		env      map[string]string
		reply    reply
		invalid  bool   // ErrInvalidInput, else ErrToolFailed
		mentions string // in the message the model sees
	}{
		{"400, recorded", "", nil, reply{status: 400, body: fixture(t, "search-query-too-short")}, true, "Query is too short. Min query length is 2 characters."},
		{"400 keyless", `{"auth": "keyless"}`, nil, reply{status: 400, body: fixture(t, "search-query-too-short")}, true, "Query is too short. Min query length is 2 characters."},
		{"422, recorded", "", nil, reply{status: 422, body: fixture(t, "raw-search-422-query-not-a-string")}, true, "body.query: Input should be a valid string"},
		{"400 extract, recorded", "", nil, reply{status: 400, body: fixture(t, "raw-extract-400-all-urls-invalid")}, true, "not-a-url: Validation Error: Invalid URL format"},
		{"400 without a body", "", nil, reply{status: 400}, true, "no details"},
		{"401, recorded", "", nil, reply{status: 401, body: fixture(t, "search-bad-key")}, false, "rejected the API key"},
		{"432 plan limit", "", nil, reply{status: 432, body: []byte(`{"detail": {"error": "This request exceeds your plan's set usage limit."}}`)}, false, "status 432"},
		{"200 wrong types", "", nil, reply{status: 200, body: []byte(`{"results": [{"title": 7}]}`)}, false, "not valid search results"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, rigOpts{settings: c.settings, env: c.env})
			r.up.queue(c.reply)
			_, err := r.call("tavily_search", `{"query": "go release"}`)
			if c.invalid && !isInvalidInput(err) || !c.invalid && !isToolFailed(err) {
				t.Fatalf("err = %v, want invalid input %v", err, c.invalid)
			}
			// The message is the tool result the model reads.
			if !strings.Contains(err.Error(), c.mentions) {
				t.Errorf("error %q does not carry %q", err, c.mentions)
			}
			if n := len(r.up.requests()); n != 1 {
				t.Errorf("%d requests, want 1 (no retry)", n)
			}
		})
	}

	t.Run("timeout", func(t *testing.T) {
		t.Parallel()
		release := make(chan struct{})
		slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			select {
			case <-release:
			case <-req.Context().Done():
			}
		}))
		t.Cleanup(slow.Close)
		t.Cleanup(func() { close(release) }) // runs before slow.Close
		r := newRig(t, rigOpts{settings: `{"timeout": "50ms"}`, extra: []tavily.Option{tavily.WithBaseURL(slow.URL)}})
		_, err := r.call("tavily_search", `{"query": "go release"}`)
		if !isToolFailed(err) || errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v, want ErrToolFailed (the tool's own limit, not the caller's)", err)
		}
	})
}

// rateLimited is a recorded-shape 429.
func rateLimited(t *testing.T, retryAfter string) reply {
	h := http.Header{}
	if retryAfter != "" {
		h.Set("Retry-After", retryAfter)
	}
	return reply{status: http.StatusTooManyRequests, header: h, body: fixture(t, "docs-429")}
}

// callAsync runs a call in the background.
func (r *rig) callAsync(ctx context.Context, name, input string) <-chan error {
	done := make(chan error, 1)
	go func() {
		out, err := r.reg.Invoke(ctx, name, json.RawMessage(input))
		r.mu.Lock()
		r.seen = append(r.seen, string(out))
		if err != nil {
			r.seen = append(r.seen, err.Error())
		}
		r.mu.Unlock()
		done <- err
	}()
	return done
}

// armed blocks until the call has armed its retry timer (true), or has
// returned without arming one (false, and its error). Neither wait has a time
// limit: a call does one or the other, so how loaded the machine is cannot
// change the answer.
func (r *rig) armed(t *testing.T, done <-chan error) (bool, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	timer := make(chan error, 1)
	go func() { timer <- r.clock.WaitForTimers(ctx, 1) }()
	select {
	case err := <-timer:
		if err != nil {
			t.Fatalf("waiting for the retry timer: %v", err) // the test is ending
		}
		return true, nil
	case err := <-done:
		return false, err
	}
}

// waitTimer is armed for a call that must wait to retry.
func (r *rig) waitTimer(t *testing.T, done <-chan error) {
	t.Helper()
	if ok, err := r.armed(t, done); !ok {
		t.Fatalf("the call returned (%v) without waiting to retry", err)
	}
}

// pending reports, without waiting, whether a timer is armed now:
// WaitForTimers checks before it looks at the context.
func (r *rig) pending() bool {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return r.clock.WaitForTimers(ctx, 1) == nil
}

// noRetry runs a call that must fail at once, without waiting to retry.
func (r *rig) noRetry(ctx context.Context, t *testing.T) error {
	t.Helper()
	done := r.callAsync(ctx, "tavily_search", `{"query": "go release"}`)
	ok, err := r.armed(t, done)
	if ok {
		t.Errorf("the call armed a retry timer, want an error at once")
		r.clock.Advance(time.Hour) // let it finish
		err = <-done
	}
	return err
}

// deadlineCtx reports a deadline but never expires, so the time the test
// takes cannot change what the code decides from the deadline.
type deadlineCtx struct {
	context.Context
	at time.Time
}

func (c deadlineCtx) Deadline() (time.Time, bool) { return c.at, true }

// TestRateLimit: what E10 adds to the suite's retry check
// (TestConformance/errors/429_*): the hint to set the key in keyless mode, a
// missing or unparseable Retry-After, and no retry past 10 s, past the
// deadline or after a cancel.
func TestRateLimit(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name       string
		settings   string
		retryAfter string
		wait       time.Duration
		second     reply
		hint       bool // the error names TAVILY_API_KEY
	}{
		{"keyless, limited twice", `{"auth": "keyless"}`, "3", 3 * time.Second, reply{}, true},
		{"keyless, then ok", `{"auth": "keyless"}`, "10", 10 * time.Second, reply{status: 200}, false},
		{"no Retry-After", "", "", time.Second, reply{status: 200}, false},
		{"unparseable Retry-After", "", "soon", time.Second, reply{status: 200}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, rigOpts{settings: c.settings})
			second := c.second
			if second.status == 0 {
				second = rateLimited(t, c.retryAfter)
			} else {
				second.body = fixture(t, "search-go-release")
			}
			r.up.queue(rateLimited(t, c.retryAfter), second)

			done := r.callAsync(t.Context(), "tavily_search", `{"query": "go release"}`)
			r.waitTimer(t, done)
			// Just short of the wait the timer must still be pending: Advance
			// fires due timers before it returns, so a shorter wait would
			// already have fired.
			r.clock.Advance(c.wait - time.Millisecond)
			if !r.pending() {
				r.clock.Advance(time.Hour)
				t.Fatalf("the retry timer fired before Retry-After (%v): %v", c.wait, <-done)
			}
			if n := len(r.up.requests()); n != 1 {
				t.Fatalf("%d requests before the wait was over, want 1", n)
			}
			r.clock.Advance(time.Millisecond)
			err := <-done

			if n := len(r.up.requests()); n != 2 {
				t.Errorf("%d requests, want 2", n)
			}
			if second.status == 200 {
				if err != nil {
					t.Errorf("err = %v after a successful retry", err)
				}
				return
			}
			if !isToolFailed(err) || !strings.Contains(err.Error(), "rate limited") {
				t.Fatalf("err = %v, want ErrToolFailed \"rate limited\"", err)
			}
			if got := strings.Contains(err.Error(), "set TAVILY_API_KEY"); got != c.hint {
				t.Errorf("error %q: hint to set TAVILY_API_KEY %v, want %v", err, got, c.hint)
			}
		})
	}

	t.Run("Retry-After over 10s: no retry", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, rigOpts{settings: `{"auth": "keyless"}`})
		r.up.queue(rateLimited(t, "11"))
		err := r.noRetry(t.Context(), t)
		if !isToolFailed(err) || !strings.Contains(err.Error(), "set TAVILY_API_KEY") {
			t.Errorf("err = %v, want rate limited with the hint", err)
		}
		if n := len(r.up.requests()); n != 1 {
			t.Errorf("%d requests, want 1", n)
		}
	})

	t.Run("deadline before the wait ends: no retry", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, rigOpts{})
		r.up.queue(rateLimited(t, "5"))
		// The deadline is 2 s away whenever the code looks, however slow the
		// request was, and it never expires on its own.
		err := r.noRetry(deadlineCtx{t.Context(), time.Now().Add(2 * time.Second)}, t)
		if !isToolFailed(err) || !strings.Contains(err.Error(), "rate limited") {
			t.Errorf("err = %v, want rate limited at once", err)
		}
		if n := len(r.up.requests()); n != 1 {
			t.Errorf("%d requests, want 1", n)
		}
	})

	t.Run("cancelled during the wait", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, rigOpts{})
		r.up.queue(rateLimited(t, "3"))
		ctx, cancel := context.WithCancel(t.Context())
		done := r.callAsync(ctx, "tavily_search", `{"query": "go release"}`)
		r.waitTimer(t, done)
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
		if n := len(r.up.requests()); n != 1 {
			t.Errorf("%d requests, want 1", n)
		}
	})
}
