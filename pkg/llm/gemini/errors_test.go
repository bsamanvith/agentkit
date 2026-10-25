package gemini_test

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/gemini"
)

// TestErrorMapping checks that the API's errors, before and inside a
// stream, map to the llm sentinels, with the API's error reachable as an
// *gemini.Error.
func TestErrorMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		fixture string
		want    error
		apiErr  *gemini.Error // the *Error reachable, if any
	}{
		{"conformance/error_rate_limited", llm.ErrRateLimited, &gemini.Error{StatusCode: 429, Status: "RESOURCE_EXHAUSTED", Delay: 20 * time.Second}},
		{"conformance/error_overloaded", llm.ErrOverloaded, &gemini.Error{StatusCode: 503, Status: "UNAVAILABLE"}},
		{"conformance/error_invalid_request", llm.ErrInvalidRequest, &gemini.Error{StatusCode: 400, Status: "INVALID_ARGUMENT"}},
		{"conformance/error_auth", llm.ErrAuth, &gemini.Error{StatusCode: 400, Status: "INVALID_ARGUMENT", Reason: "API_KEY_INVALID"}},
		{"conformance/error_context_too_long", llm.ErrContextTooLong, &gemini.Error{StatusCode: 400, Status: "INVALID_ARGUMENT"}},
		{"conformance/error_mid_stream", llm.ErrOverloaded, &gemini.Error{StatusCode: 500, Status: "INTERNAL", InStream: true}},
		{"errors/quota_daily", llm.ErrRateLimited, &gemini.Error{StatusCode: 429, Status: "RESOURCE_EXHAUSTED", Delay: 82732 * time.Second}},
		{"errors/no_key", llm.ErrAuth, &gemini.Error{StatusCode: 403, Status: "PERMISSION_DENIED"}},
		{"errors/model_gone", llm.ErrInvalidRequest, &gemini.Error{StatusCode: 404, Status: "NOT_FOUND"}},
		{"errors/missing_signature", llm.ErrInvalidRequest, &gemini.Error{StatusCode: 400, Status: "INVALID_ARGUMENT"}},
		{"errors/bad_gateway", llm.ErrOverloaded, &gemini.Error{StatusCode: 502}},
		{"errors/mid_stream_rate_limit", llm.ErrRateLimited, &gemini.Error{StatusCode: 429, Status: "RESOURCE_EXHAUSTED", InStream: true}},
		{"errors/no_finish", llm.ErrOverloaded, nil},
	}
	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			t.Parallel()
			_, srv := newReplay(t, "testdata/"+c.fixture+".http")
			_, err := llm.Collect(newModel(t, srv).Generate(t.Context(), userText("Hello.")))
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			for _, other := range []error{llm.ErrRateLimited, llm.ErrOverloaded, llm.ErrInvalidRequest, llm.ErrAuth, llm.ErrContextTooLong} {
				if !errors.Is(c.want, other) && errors.Is(err, other) {
					t.Errorf("err also matches %v", other)
				}
			}
			apiErr, ok := errors.AsType[*gemini.Error](err)
			if ok != (c.apiErr != nil) {
				t.Fatalf("errors.As(*Error) = %v, want %v", ok, c.apiErr != nil)
			}
			if c.apiErr == nil {
				return
			}
			got := *apiErr
			if got.Message == "" && c.fixture != "errors/bad_gateway" {
				t.Errorf("no message read: %+v", got)
			}
			got.Message = ""
			if got != *c.apiErr {
				t.Errorf("error = %+v, want %+v", got, *c.apiErr)
			}
		})
	}
}

// header is a transport that answers every request with a reply and records
// the requests.
type header struct {
	mu   sync.Mutex
	sent []*http.Request
}

func (h *header) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
	}
	h.mu.Lock()
	h.sent = append(h.sent, r)
	h.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(chunks(parts("STOP", `{"text":"Hi."}`)))),
		Request:    r,
	}, nil
}

// TestKeyFromEnvironment checks the key New reads, GEMINI_API_KEY before
// GOOGLE_API_KEY, the default model and server, and that WithAPIKey("")
// sends none.
func TestKeyFromEnvironment(t *testing.T) {
	cases := []struct {
		name, gemini, google string
		opts                 []gemini.Option
		want                 string // "" for no header
	}{
		{"gemini first", "from-gemini", "from-google", nil, "from-gemini"},
		{"then google", "", "from-google", nil, "from-google"},
		{"option wins", "from-gemini", "", []gemini.Option{gemini.WithAPIKey("from-option")}, "from-option"},
		{"empty option sends none", "from-gemini", "", []gemini.Option{gemini.WithAPIKey("")}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("GEMINI_API_KEY", c.gemini)
			t.Setenv("GOOGLE_API_KEY", c.google)
			tr := &header{}
			m, err := gemini.New(append([]gemini.Option{gemini.WithHTTPClient(&http.Client{Transport: tr})}, c.opts...)...)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := llm.Collect(m.Generate(t.Context(), userText("Hello."))); err != nil {
				t.Fatal(err)
			}
			r := tr.sent[0]
			if got, ok := r.Header["X-Goog-Api-Key"]; c.want == "" && ok || c.want != "" && r.Header.Get("X-Goog-Api-Key") != c.want {
				t.Errorf("X-Goog-Api-Key = %q, want %q", got, c.want)
			}
			if want := gemini.DefaultBaseURL + "/models/" + gemini.DefaultModel + ":streamGenerateContent?alt=sse"; r.URL.String() != want {
				t.Errorf("URL = %s, want %s", r.URL, want)
			}
			if strings.Contains(r.URL.RawQuery, "key=") {
				t.Error("the key is in the URL")
			}
		})
	}
}

// TestConnectionRefusedIsOverloaded checks that a server that cannot be
// reached is a transient failure.
func TestConnectionRefusedIsOverloaded(t *testing.T) {
	t.Parallel()
	_, srv := newReplay(t)
	m := newModel(t, srv)
	srv.Close()
	if _, err := llm.Collect(m.Generate(t.Context(), userText("Hello."))); !errors.Is(err, llm.ErrOverloaded) {
		t.Errorf("err = %v, want ErrOverloaded", err)
	}
}

// TestRetries checks that retries are on by default and wait for the
// error's RetryInfo delay, and that a delay of hours (a daily quota) is not
// waited for.
func TestRetries(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name     string
		first    string
		requests int
		ok       bool
	}{
		{"overloaded, then a reply", "testdata/retry.http", 2, true},
		{"daily quota", "testdata/errors/quota_daily.http", 1, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			rp, srv := newReplay(t, c.first, "testdata/conformance/text.http")
			m, err := gemini.New(gemini.WithModel("gemini-replay"), gemini.WithBaseURL(srv.URL+"/v1beta"), gemini.WithAPIKey("test-key"))
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			_, err = llm.Collect(m.Generate(t.Context(), userText("Hello.")))
			if (err == nil) != c.ok || len(rp.sent()) != c.requests {
				t.Fatalf("err = %v after %d requests, want success %v after %d", err, len(rp.sent()), c.ok, c.requests)
			}
			if c.ok && time.Since(start) < 50*time.Millisecond {
				t.Errorf("retried after %v, before the RetryInfo delay of 50ms", time.Since(start))
			}
		})
	}
}
