package common_test

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
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/connectors/google/common"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

const canary = "ya29.canary-access-token"

// rig is a client against a scripted server. The server answers each request
// with the next response of the script (the last one repeats) and records what
// it received.
type rig struct {
	client *common.Client
	clock  *clocktest.Fake
	srv    *httptest.Server
	hits   atomic.Int32
	last   atomic.Pointer[http.Request]
}

type reply struct {
	status int
	header http.Header
	body   string
}

func newRig(t *testing.T, tokens common.TokenSource, script ...reply) *rig {
	t.Helper()
	r := &rig{clock: clocktest.NewFake(time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC))}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		n := int(r.hits.Add(1)) - 1
		r.last.Store(req)
		rep := script[min(n, len(script)-1)]
		for k, v := range rep.header {
			w.Header()[k] = v
		}
		w.WriteHeader(rep.status)
		_, _ = w.Write([]byte(rep.body))
	}))
	t.Cleanup(r.srv.Close)
	if tokens == nil {
		tokens = common.TokenSourceFunc(func(context.Context) (string, error) { return canary, nil })
	}
	env := common.Env{HTTPClient: r.srv.Client(), Clock: r.clock}
	env.Defaults()
	r.client = common.NewClient(common.ClientConfig{Service: "gmail", BaseURL: r.srv.URL, Tokens: tokens, Timeout: 2 * time.Second, Env: env})
	return r
}

func ok(body string) reply { return reply{status: 200, body: body} }

func apiErr(status int, message, reason string) reply {
	return reply{status: status, body: fmt.Sprintf(`{"error":{"code":%d,"message":%q,"errors":[{"reason":%q}]}}`, status, message, reason)}
}

// advanceRetry waits until the client sleeps before its retry, then wakes it.
func (r *rig) advanceRetry(t *testing.T, d time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.clock.WaitForTimers(ctx, 1); err != nil {
		t.Fatalf("the client never waited to retry: %v", err)
	}
	r.clock.Advance(d)
}

func TestRequestCarriesTheTokenInOneHeaderOnly(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil, ok(`{"id":"m1"}`))
	var out struct{ ID string }
	err := r.client.DoJSON(t.Context(), common.Request{
		Path:  common.Path("/gmail/v1/users/me/messages", "a/b?c"),
		Query: map[string][]string{"format": {"full"}},
	}, &out)
	if err != nil {
		t.Fatal(err)
	}
	req := r.last.Load()
	if got := req.Header.Get("Authorization"); got != "Bearer "+canary {
		t.Errorf("Authorization = %q", got)
	}
	if strings.Contains(req.URL.String(), canary) {
		t.Errorf("token in the URL: %s", req.URL)
	}
	// An id from the model stays one path segment.
	if want := "/gmail/v1/users/me/messages/a%2Fb%3Fc"; req.URL.EscapedPath() != want {
		t.Errorf("path = %s, want %s", req.URL.EscapedPath(), want)
	}
	if out.ID != "m1" {
		t.Errorf("decoded %+v", out)
	}
}

func TestFailuresAreClassed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		reply     reply
		invalid   bool
		contains  string
		wantCalls int32
	}{
		{"bad request keeps google's message", apiErr(400, "Invalid value for: label", "invalid"), true, "Invalid value for: label", 1},
		{"unprocessable is the model's too", apiErr(422, "Bad thing", "x"), true, "Bad thing", 1},
		{"token rejected says to reconnect", apiErr(401, "Invalid Credentials", "authError"), false, "reconnected", 1},
		{"forbidden says what google said", apiErr(403, "Insufficient Permission", "insufficientPermissions"), false, "Insufficient Permission", 1},
		{"not found", apiErr(404, "Requested entity was not found.", "notFound"), false, "not found", 1},
		{"server error is the status only", reply{status: 503, body: `{"error":{"message":"internal detail"}}`}, false, "status 503", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, nil, tt.reply)
			err := r.client.DoJSON(t.Context(), common.Request{Path: "/x"}, nil)
			if err == nil {
				t.Fatal("no error")
			}
			var ie *common.InputError
			if got := errors.As(err, &ie); got != tt.invalid {
				t.Errorf("model-fixable = %v, want %v (%v)", got, tt.invalid, err)
			}
			if !strings.Contains(err.Error(), tt.contains) {
				t.Errorf("error %q lacks %q", err, tt.contains)
			}
			if tt.reply.status >= 500 && strings.Contains(err.Error(), "internal detail") {
				t.Errorf("5xx error shows the body: %v", err)
			}
			if got := r.hits.Load(); got != tt.wantCalls {
				t.Errorf("requests = %d, want %d (only a rate limit is retried)", got, tt.wantCalls)
			}
		})
	}
}

func TestErrorTextNeverShowsTheTokenAndIsCapped(t *testing.T) {
	t.Parallel()
	msg := "bad " + canary + " " + strings.Repeat("é", 1000)
	r := newRig(t, nil, apiErr(400, msg, "invalid"))
	err := r.client.DoJSON(t.Context(), common.Request{Path: "/x"}, nil)
	if err == nil || strings.Contains(err.Error(), canary) {
		t.Fatalf("error = %v", err)
	}
	if n := utf8.RuneCountInString(err.Error()); n > 500 {
		t.Errorf("error is %d characters", n)
	}
}

func TestRateLimitIsRetriedOnceAfterRetryAfter(t *testing.T) {
	t.Parallel()
	for name, first := range map[string]reply{
		"429": {status: 429, header: http.Header{"Retry-After": {"3"}}, body: `{}`},
		"403 rateLimitExceeded": {
			status: 403, header: http.Header{"Retry-After": {"3"}},
			body: `{"error":{"message":"Rate Limit Exceeded","errors":[{"reason":"rateLimitExceeded"}]}}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, nil, first, ok(`{}`))
			done := make(chan error, 1)
			go func() { done <- r.client.DoJSON(t.Context(), common.Request{Path: "/x"}, nil) }()
			r.advanceRetry(t, 3*time.Second)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if got := r.hits.Load(); got != 2 {
				t.Errorf("requests = %d, want 2", got)
			}
		})
	}
}

func TestRateLimitIsNotRetriedTwiceOrAfterALongWait(t *testing.T) {
	t.Parallel()
	t.Run("still limited after the retry", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, nil, reply{status: 429, body: `{}`})
		done := make(chan error, 1)
		go func() { done <- r.client.DoJSON(t.Context(), common.Request{Path: "/x"}, nil) }()
		r.advanceRetry(t, time.Second)
		err := <-done
		if err == nil || !strings.Contains(err.Error(), "rate limited") {
			t.Errorf("error = %v", err)
		}
		if got := r.hits.Load(); got != 2 {
			t.Errorf("requests = %d, want 2", got)
		}
	})
	t.Run("asked to wait over 10s", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, nil, reply{status: 429, header: http.Header{"Retry-After": {"60"}}, body: `{}`})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- r.client.DoJSON(ctx, common.Request{Path: "/x"}, nil) }()
		// A client that waits 60 s on the fake clock never returns; real time
		// here only lets a broken client fail instead of hanging the test.
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "rate limited") || r.hits.Load() != 1 {
				t.Errorf("error = %v after %d requests, want a rate limit error after 1", err, r.hits.Load())
			}
		case <-time.After(5 * time.Second):
			t.Error("the client waits for a Retry-After over 10 s instead of giving up")
			cancel()
		}
	})
}

func TestRetryWaitStopsWithTheContext(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil, reply{status: 429, header: http.Header{"Retry-After": {"5"}}, body: `{}`})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- r.client.DoJSON(ctx, common.Request{Path: "/x"}, nil) }()
	wctx, wcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer wcancel()
	if err := r.clock.WaitForTimers(wctx, 1); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

type refreshing struct {
	tokens  []string
	next    atomic.Int32
	dropped atomic.Pointer[string]
}

func (s *refreshing) Token(context.Context) (string, error) {
	return s.tokens[min(int(s.next.Load()), len(s.tokens)-1)], nil
}
func (s *refreshing) Invalidate(token string) { s.dropped.Store(&token); s.next.Add(1) }

func TestUnauthorizedRefreshesTheTokenOnce(t *testing.T) {
	t.Parallel()
	src := &refreshing{tokens: []string{"old", "new"}}
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		seen = append(seen, req.Header.Get("Authorization"))
		if req.Header.Get("Authorization") == "Bearer old" {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	env := common.Env{HTTPClient: srv.Client()}
	env.Defaults()
	c := common.NewClient(common.ClientConfig{Service: "gmail", BaseURL: srv.URL, Tokens: src, Env: env})
	if err := c.DoJSON(t.Context(), common.Request{Path: "/x"}, nil); err != nil {
		t.Fatal(err)
	}
	if want := []string{"Bearer old", "Bearer new"}; fmt.Sprint(seen) != fmt.Sprint(want) {
		t.Errorf("requests carried %v, want %v", seen, want)
	}
	if d := src.dropped.Load(); d == nil || *d != "old" {
		t.Errorf("invalidated %v, want old", d)
	}
}

func TestNoTokenIsASayWhatToDoError(t *testing.T) {
	t.Parallel()
	r := newRig(t, common.TokenSourceFunc(func(context.Context) (string, error) { return "", common.ErrNoToken }))
	err := r.client.DoJSON(t.Context(), common.Request{Path: "/x"}, nil)
	if !errors.Is(err, common.ErrNoToken) || r.hits.Load() != 0 {
		t.Errorf("error = %v after %d requests, want ErrNoToken and none", err, r.hits.Load())
	}
}

func TestTokenSourceFailureHidesItsCause(t *testing.T) {
	t.Parallel()
	r := newRig(t, common.TokenSourceFunc(func(context.Context) (string, error) { return "", errors.New("vault at 10.0.0.5 down") }))
	err := r.client.DoJSON(t.Context(), common.Request{Path: "/x"}, nil)
	if err == nil || strings.Contains(err.Error(), "10.0.0.5") {
		t.Errorf("error = %v", err)
	}
}

func TestOversizedBodyIsRefused(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil, ok(strings.Repeat("a", common.MaxBodyBytes+1)))
	if _, err := r.client.Do(t.Context(), common.Request{Path: "/x"}); err == nil || !strings.Contains(err.Error(), "8 MB") {
		t.Errorf("error = %v", err)
	}
}

func TestMalformedJSONIsAToolError(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil, ok(`{"id":`))
	var out struct{}
	if err := r.client.DoJSON(t.Context(), common.Request{Path: "/x"}, &out); err == nil {
		t.Error("no error")
	}
}

func TestPaginate(t *testing.T) {
	t.Parallel()
	pages := map[string][]int{"": {1, 2, 3}, "p2": {4, 5, 6}, "p3": {7}}
	next := map[string]string{"": "p2", "p2": "p3", "p3": ""}
	fetch := func(_ context.Context, token string, want int) ([]int, string, error) {
		items := pages[token]
		return items[:min(len(items), want)], next[token], nil
	}
	tests := []struct {
		limit    int
		start    string
		want     []int
		wantNext string
	}{
		{2, "", []int{1, 2}, "p2"}, // a page cut short: continue at the next page's token is the caller's call
		{3, "", []int{1, 2, 3}, "p2"},
		{5, "", []int{1, 2, 3, 4, 5}, "p3"},
		{50, "", []int{1, 2, 3, 4, 5, 6, 7}, ""},
		{50, "p3", []int{7}, ""},
	}
	for _, tt := range tests {
		got, gotNext, err := common.Paginate(t.Context(), tt.limit, tt.start, fetch)
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(got) != fmt.Sprint(tt.want) || gotNext != tt.wantNext {
			t.Errorf("limit %d from %q = %v, next %q; want %v, next %q", tt.limit, tt.start, got, gotNext, tt.want, tt.wantNext)
		}
	}
}

func TestPaginateStopsOnErrorCancellationAndEndlessPages(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	if _, _, err := common.Paginate(t.Context(), 5, "", func(context.Context, string, int) ([]int, string, error) {
		return nil, "", boom
	}); !errors.Is(err, boom) {
		t.Errorf("error = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := common.Paginate(ctx, 5, "", func(context.Context, string, int) ([]int, string, error) {
		return []int{1}, "n", nil
	}); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v", err)
	}
	// A server that always has a next page and sends nothing cannot hold a call.
	var calls int
	got, _, err := common.Paginate(t.Context(), 5, "", func(context.Context, string, int) ([]int, string, error) {
		calls++
		return nil, "more", nil
	})
	if err != nil || len(got) != 0 || calls > 100 {
		t.Errorf("got %v, %v after %d pages", got, err, calls)
	}
}

func TestClampLimit(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		n       int
		want    int
		invalid bool
	}{{0, 10, false}, {1, 1, false}, {50, 50, false}, {51, 0, true}, {-1, 0, true}} {
		got, err := common.ClampLimit("max_results", tt.n, 10, 50)
		var ie *common.InputError
		if errors.As(err, &ie) != tt.invalid || got != tt.want {
			t.Errorf("ClampLimit(%d) = %d, %v", tt.n, got, err)
		}
	}
}

func TestTruncateCutsAtARuneBoundaryAndSaysHowToGetMore(t *testing.T) {
	t.Parallel()
	s := strings.Repeat("日本語", 100)
	got := common.TruncateContent(s, 10, "call x again")
	if !utf8.ValidString(got) {
		t.Fatalf("invalid UTF-8: %q", got)
	}
	if want := strings.Repeat("日本語", 3) + "日…[truncated 290 characters; call x again]"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := common.TruncateContent("short", 10, "x"); got != "short" {
		t.Errorf("short text changed: %q", got)
	}
	if got := common.Truncate("abcdef", 3); got != "abc"+common.TruncatedMarker {
		t.Errorf("Truncate = %q", got)
	}
	if got := common.TruncateContent(strings.Repeat("a", 1001), 1000, "m"); !strings.Contains(got, "truncated 1 character;") {
		t.Errorf("singular unit: %q", got)
	}
	if got := common.TruncateContent(strings.Repeat("a", 13400), 1000, "m"); !strings.Contains(got, "12,400 characters") {
		t.Errorf("thousands: %q", got)
	}
}

func TestSettings(t *testing.T) {
	t.Parallel()
	t.Run("defaults", func(t *testing.T) {
		t.Parallel()
		var s common.Settings
		if err := s.Validate("gmail"); err != nil {
			t.Fatal(err)
		}
		if s.AccessTokenEnv != common.DefaultTokenEnv || s.RequestTimeout() != 30*time.Second {
			t.Errorf("settings = %+v, timeout %v", s, s.RequestTimeout())
		}
	})
	for name, s := range map[string]common.Settings{
		"a token pasted as the variable name": {AccessTokenEnv: "ya29.a0Af-secret"},
		"http base url":                       {BaseURL: "http://example.com"},
		"base url with a user":                {BaseURL: "https://u:p@example.com"},
		"base url with a query":               {BaseURL: "https://example.com?x=1"},
		"zero timeout":                        {Timeout: "0s"},
		"huge timeout":                        {Timeout: "1h"},
		"timeout not a duration":              {Timeout: "soon"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := s.Validate("gmail")
			if !errors.Is(err, toolset.ErrInvalidSettings) {
				t.Fatalf("error = %v, want ErrInvalidSettings", err)
			}
			for _, secret := range []string{"ya29", "u:p"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error echoes %q: %v", secret, err)
				}
			}
		})
	}
	t.Run("loopback http is a replay server", func(t *testing.T) {
		t.Parallel()
		s := common.Settings{BaseURL: "http://127.0.0.1:8080/"}
		if err := s.Validate("gmail"); err != nil || s.BaseURL != "http://127.0.0.1:8080" {
			t.Errorf("%+v, %v", s, err)
		}
	})
}

func TestLiteralTokensAreRefusedWithoutBeingEchoed(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"api_key", "access_token", "token", "Refresh_Token", "client_secret"} {
		err := common.RefuseLiteralToken("gmail", []byte(fmt.Sprintf(`{%q: "s3cr3t-value"}`, field)))
		if !errors.Is(err, toolset.ErrInvalidSettings) || strings.Contains(err.Error(), "s3cr3t-value") ||
			!strings.Contains(err.Error(), "access_token_env") {
			t.Errorf("%s: error = %v", field, err)
		}
	}
	for _, ok := range []string{``, `null`, `{}`, `{"access_token_env":"X"}`, `[1]`} {
		if err := common.RefuseLiteralToken("gmail", []byte(ok)); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
}

func TestEnvTokens(t *testing.T) {
	t.Parallel()
	lookup := func(k string) (string, bool) {
		return map[string]string{"SET": " tok ", "EMPTY": "  "}[k], k != "UNSET"
	}
	src, err := common.EnvTokens(lookup, "SET")
	if err != nil {
		t.Fatal(err)
	}
	if tok, _ := src.Token(t.Context()); tok != "tok" {
		t.Errorf("token = %q", tok)
	}
	for _, name := range []string{"UNSET", "EMPTY"} {
		if _, err := common.EnvTokens(lookup, name); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: error = %v, want one naming the variable", name, err)
		}
	}
	if _, err := common.EnvTokens(nil, "X"); err == nil {
		t.Error("nil lookup gave no error")
	}
}
