package openaicompat_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/openaicompat"
)

// TestErrorMapping checks that the errors of OpenAI and of the servers that
// imitate it, before and inside a stream, map to the llm sentinels, with the
// server's error reachable as an *openaicompat.Error.
func TestErrorMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		fixture string
		want    error
		apiErr  *openaicompat.Error // the *Error reachable, if any
	}{
		{"conformance/error_rate_limited", llm.ErrRateLimited, &openaicompat.Error{StatusCode: 429, Type: "tokens", Code: "rate_limit_exceeded", RequestID: "req_replay"}},
		{"conformance/error_overloaded", llm.ErrOverloaded, &openaicompat.Error{StatusCode: 503, Type: "server_error", RequestID: "req_replay"}},
		{"conformance/error_invalid_request", llm.ErrInvalidRequest, &openaicompat.Error{StatusCode: 400, Type: "invalid_request_error", Code: "invalid_value", RequestID: "req_replay"}},
		{"conformance/error_auth", llm.ErrAuth, &openaicompat.Error{StatusCode: 401, Type: "invalid_request_error", Code: "invalid_api_key", RequestID: "req_replay"}},
		{"conformance/error_context_too_long", llm.ErrContextTooLong, &openaicompat.Error{StatusCode: 400, Type: "invalid_request_error", Code: "context_length_exceeded", RequestID: "req_replay"}},
		{"conformance/error_mid_stream", llm.ErrOverloaded, &openaicompat.Error{Type: "server_error", InStream: true}},
		{"errors/insufficient_quota", llm.ErrAuth, &openaicompat.Error{StatusCode: 429, Type: "insufficient_quota", Code: "insufficient_quota", RequestID: "req_replay"}},
		{"errors/plain_429", llm.ErrRateLimited, &openaicompat.Error{StatusCode: 429, Type: "api_error"}},
		{"errors/forbidden", llm.ErrInvalidRequest, &openaicompat.Error{StatusCode: 403, Type: "invalid_request_error", Code: "model_not_found", RequestID: "req_replay"}},
		{"errors/ollama_not_found", llm.ErrInvalidRequest, &openaicompat.Error{StatusCode: 404, Type: "not_found_error", RequestID: "req_replay"}},
		{"errors/vllm_context", llm.ErrContextTooLong, &openaicompat.Error{StatusCode: 400, Type: "BadRequestError", RequestID: "req_replay"}},
		{"errors/vllm_not_found", llm.ErrInvalidRequest, &openaicompat.Error{StatusCode: 404, Type: "NotFoundError", RequestID: "req_replay"}},
		{"errors/string_error", llm.ErrInvalidRequest, &openaicompat.Error{StatusCode: 404}},
		{"errors/bad_gateway", llm.ErrOverloaded, &openaicompat.Error{StatusCode: 502}},
		{"errors/mid_stream_rate_limit", llm.ErrRateLimited, &openaicompat.Error{Type: "tokens", Code: "rate_limit_exceeded", InStream: true}},
		{"errors/mid_stream_vllm", llm.ErrContextTooLong, &openaicompat.Error{StatusCode: 400, Type: "BadRequestError", InStream: true}},
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
			var apiErr *openaicompat.Error
			if errors.As(err, &apiErr) != (c.apiErr != nil) {
				t.Fatalf("errors.As(*Error) = %v, want %v", c.apiErr == nil, c.apiErr != nil)
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
// the request headers and URL.
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
		Body:       io.NopCloser(strings.NewReader(chunks(`{"index":0,"delta":{"content":"Hi."},"finish_reason":"stop"}`))),
		Request:    r,
	}, nil
}

// TestKeyOverPlainHTTPIsSent checks that a key goes to a plain http server
// that is not loopback (a vLLM inside a cluster), unlike the OpenAI SDK,
// and that extra headers go too without replacing the key.
func TestKeyOverPlainHTTPIsSent(t *testing.T) {
	t.Parallel()
	tr := &header{}
	m, err := openaicompat.New(
		openaicompat.WithModel("m"),
		openaicompat.WithBaseURL("http://vllm.internal:8000/v1/"),
		openaicompat.WithAPIKey("test-key"),
		openaicompat.WithHeader("OpenAI-Project", "proj_1"),
		openaicompat.WithHeader("Authorization", "Bearer not-this"),
		openaicompat.WithHTTPClient(&http.Client{Transport: tr}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := llm.Collect(m.Generate(t.Context(), userText("Hello."))); err != nil {
		t.Fatal(err)
	}
	r := tr.sent[0]
	if got := r.URL.String(); got != "http://vllm.internal:8000/v1/chat/completions" {
		t.Errorf("URL = %s", got)
	}
	if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
		t.Errorf("Authorization = %q", got)
	}
	if got := r.Header.Get("OpenAI-Project"); got != "proj_1" {
		t.Errorf("OpenAI-Project = %q", got)
	}
	if got := r.Header.Get("Accept"); got != "text/event-stream" {
		t.Errorf("Accept = %q", got)
	}
}

// TestNoKeyNoAuthorization checks that an empty key sends no Authorization
// header, even with OPENAI_API_KEY set, and that a base URL keeps its query.
func TestNoKeyNoAuthorization(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "from-env")
	tr := &header{}
	m, err := openaicompat.New(
		openaicompat.WithModel("m"),
		openaicompat.WithBaseURL("https://gateway.invalid/openai?api-version=1"),
		openaicompat.WithAPIKey(""),
		openaicompat.WithHTTPClient(&http.Client{Transport: tr}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := llm.Collect(m.Generate(t.Context(), userText("Hello."))); err != nil {
		t.Fatal(err)
	}
	r := tr.sent[0]
	if got, ok := r.Header["Authorization"]; ok {
		t.Errorf("Authorization = %q, want none", got)
	}
	if got := r.URL.String(); got != "https://gateway.invalid/openai/chat/completions?api-version=1" {
		t.Errorf("URL = %s", got)
	}
}

// TestKeyAndBaseURLFromEnvironment checks the defaults New reads.
func TestKeyAndBaseURLFromEnvironment(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "from-env")
	t.Setenv("OPENAI_BASE_URL", "http://localhost:11434/v1")
	tr := &header{}
	m, err := openaicompat.New(openaicompat.WithModel("m"), openaicompat.WithHTTPClient(&http.Client{Transport: tr}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := llm.Collect(m.Generate(t.Context(), userText("Hello."))); err != nil {
		t.Fatal(err)
	}
	r := tr.sent[0]
	if r.Header.Get("Authorization") != "Bearer from-env" || r.URL.String() != "http://localhost:11434/v1/chat/completions" {
		t.Errorf("sent %s with %q", r.URL, r.Header.Get("Authorization"))
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

// TestRetriesStayOn checks that retries are on by default: a 503 followed by
// a reply succeeds.
func TestRetriesStayOn(t *testing.T) {
	t.Parallel()
	rp, srv := newReplay(t, "testdata/retry.http", "testdata/conformance/text.http")
	m, err := openaicompat.New(
		openaicompat.WithModel("gpt-replay"),
		openaicompat.WithBaseURL(srv.URL+"/v1"),
		openaicompat.WithAPIKey("test-key"),
	)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := llm.Collect(m.Generate(t.Context(), userText("Hello.")))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Message.Text() == "" || len(rp.sent()) != 2 {
		t.Errorf("got %q after %d requests, want the reply after 2", resp.Message.Text(), len(rp.sent()))
	}
}

// statuses is a transport that answers with the given responses in turn, then
// with a reply, counting the requests and when they came.
type statuses struct {
	mu        sync.Mutex
	responses []*http.Response
	at        []time.Time
}

func (s *statuses) RoundTrip(r *http.Request) (*http.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Body != nil {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
	}
	s.at = append(s.at, time.Now())
	if n := len(s.at); n <= len(s.responses) {
		resp := s.responses[n-1]
		if resp == nil {
			return nil, errors.New("connection reset")
		}
		return resp, nil
	}
	return (&header{}).RoundTrip(r)
}

func status(code int, h ...string) *http.Response {
	hdr := http.Header{}
	for i := 0; i+1 < len(h); i += 2 {
		hdr.Set(h[i], h[i+1])
	}
	return &http.Response{StatusCode: code, Header: hdr, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"x"}}`))}
}

// TestRetries checks which failures are retried, how many times, and the
// waits the server asks for.
func TestRetries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		responses []*http.Response // nil: a connection failure
		retries   int
		requests  int  // how many are sent
		ok        bool // whether the reply comes through
		minWait   time.Duration
		maxWait   time.Duration // 0: no limit
	}{
		{"429 then reply", []*http.Response{status(429, "Retry-After-Ms", "20")}, 2, 2, true, 20 * time.Millisecond, 300 * time.Millisecond},
		{"retry-after seconds", []*http.Response{status(503, "Retry-After", "0.05")}, 2, 2, true, 50 * time.Millisecond, 0},
		{"backoff", []*http.Response{status(503)}, 2, 2, true, 375 * time.Millisecond, 0},
		{"connection failure", []*http.Response{nil}, 2, 2, true, 0, 0},
		{"408 and 409", []*http.Response{status(408, "Retry-After-Ms", "1"), status(409, "Retry-After-Ms", "1")}, 2, 3, true, 0, 0},
		{"used up", []*http.Response{status(500, "Retry-After-Ms", "1"), status(500, "Retry-After-Ms", "1")}, 1, 2, false, 0, 0},
		{"off", []*http.Response{status(503)}, 0, 1, false, 0, 0},
		{"400 not retried", []*http.Response{status(400)}, 2, 1, false, 0, 0},
		{"server says no", []*http.Response{status(503, "X-Should-Retry", "false")}, 2, 1, false, 0, 0},
		{"server says yes", []*http.Response{status(400, "X-Should-Retry", "true", "Retry-After-Ms", "1")}, 2, 2, true, 0, 0},
		{"wait too long", []*http.Response{status(429, "Retry-After", "3600")}, 2, 1, false, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			tr := &statuses{responses: c.responses}
			m, err := openaicompat.New(
				openaicompat.WithModel("m"),
				openaicompat.WithBaseURL("https://replay.invalid/v1"),
				openaicompat.WithMaxRetries(c.retries),
				openaicompat.WithHTTPClient(&http.Client{Transport: tr}),
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = llm.Collect(m.Generate(t.Context(), userText("Hello.")))
			if (err == nil) != c.ok {
				t.Errorf("err = %v, want success %v", err, c.ok)
			}
			if len(tr.at) != c.requests {
				t.Fatalf("%d requests, want %d", len(tr.at), c.requests)
			}
			if len(tr.at) > 1 {
				wait := tr.at[1].Sub(tr.at[0])
				if wait < c.minWait || c.maxWait > 0 && wait > c.maxWait {
					t.Errorf("waited %v, want %v to %v", wait, c.minWait, c.maxWait)
				}
			}
		})
	}
}

// TestRetryWaitEndsWithContext checks that a cancelled context ends the wait
// for a retry.
func TestRetryWaitEndsWithContext(t *testing.T) {
	t.Parallel()
	tr := &statuses{responses: []*http.Response{status(503, "Retry-After", "100")}}
	m, err := openaicompat.New(openaicompat.WithModel("m"), openaicompat.WithBaseURL("https://replay.invalid/v1"),
		openaicompat.WithHTTPClient(&http.Client{Transport: tr}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = llm.Collect(m.Generate(ctx, userText("Hello.")))
	if !errors.Is(err, ctx.Err()) || ctx.Err() == nil || time.Since(start) > 10*time.Second {
		t.Errorf("err = %v after %v, want the context's error", err, time.Since(start))
	}
}
