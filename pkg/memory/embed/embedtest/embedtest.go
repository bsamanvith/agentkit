// Package embedtest is the conformance check of embedding API clients
// (memory.Embedder over HTTP): every client runs [Run] against its cassettes,
// served by toolsettest.Upstream through a client that reaches nothing
// else, so no test pays for an embedding.
package embedtest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest"
)

// Canary is the key every check gives the client: it must reach the
// provider in the Authorization header only.
const Canary = "embedtest-canary-key-5b1e0c"

// Env is what a check wires a client to.
type Env struct {
	// BaseURL is the upstream's URL (the client adds its API's path).
	BaseURL    string
	HTTPClient *http.Client
	Key        string
	Clock      *clocktest.Fake
	Logger     *slog.Logger
	// BatchSize and MaxBodyBytes are the client's limits (0: its default).
	BatchSize    int
	MaxBodyBytes int64
}

// Case is a client under test.
type Case struct {
	// New makes the client wired to env, with the model and dimension the
	// cassettes were made with.
	New func(env Env) (memory.Embedder, error)
	// Model and Dim are what New's client's Model must say.
	Model string
	Dim   int
	// Cassettes is the cassette directory (testdata/cassettes).
	Cassettes string
	// Calls are the happy paths, each answered from cassettes.
	Calls []Call
	// ErrorBody is the provider's error body carrying message.
	ErrorBody func(message string) []byte
}

// Call is one Embed call and the cassettes it must request, in order.
type Call struct {
	Name      string
	Texts     []string
	Purpose   memory.Purpose
	BatchSize int
	Cassettes []string
}

// Run checks c: the model, batching against the cassettes, the key (in the
// Authorization header and nowhere else, no error or log line), one retry of
// a 429 after its Retry-After on the injected clock and no retry of
// anything else, the body limit, malformed responses, and cancellation.
func Run(t *testing.T, c Case) {
	t.Helper()
	t.Run("Model", func(t *testing.T) { t.Parallel(); checkModel(t, c) })
	t.Run("Batches", func(t *testing.T) { t.Parallel(); checkBatches(t, c) })
	t.Run("Retry", func(t *testing.T) { t.Parallel(); checkRetry(t, c) })
	t.Run("NoRetry", func(t *testing.T) { t.Parallel(); checkNoRetry(t, c) })
	t.Run("BodyLimit", func(t *testing.T) { t.Parallel(); checkBodyLimit(t, c) })
	t.Run("Canary", func(t *testing.T) { t.Parallel(); checkCanary(t, c) })
	t.Run("BadResponses", func(t *testing.T) { t.Parallel(); checkBadResponses(t, c) })
	t.Run("Canceled", func(t *testing.T) { t.Parallel(); checkCanceled(t, c) })
}

// rig is one client on one upstream, with its log captured.
type rig struct {
	t   *testing.T
	up  *toolsettest.Upstream
	env Env
	e   memory.Embedder
	log *bytes.Buffer
	// read counts the response body bytes the client read.
	read *atomic.Int64
}

func newRig(t *testing.T, c Case, batch int, maxBody int64) *rig {
	t.Helper()
	up := toolsettest.NewUpstream(t, c.Cassettes)
	r := &rig{t: t, up: up, log: &bytes.Buffer{}, read: &atomic.Int64{}}
	hc := up.Client()
	counted := *hc
	counted.Transport = countingTransport{next: hc.Transport, n: r.read}
	r.env = Env{
		BaseURL: up.URL(), HTTPClient: &counted, Key: Canary,
		Clock:     clocktest.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		Logger:    slog.New(slog.NewTextHandler(&lockedWriter{w: r.log}, &slog.HandlerOptions{Level: slog.LevelDebug})),
		BatchSize: batch, MaxBodyBytes: maxBody,
	}
	e, err := c.New(r.env)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	r.e = e
	return r
}

type lockedWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

type countingTransport struct {
	next http.RoundTripper
	n    *atomic.Int64
}

func (c countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := c.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	resp.Body = countingBody{resp.Body, c.n}
	return resp, nil
}

type countingBody struct {
	io.ReadCloser
	n *atomic.Int64
}

func (b countingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.n.Add(int64(n))
	return n, err
}

func (r *rig) first() Call {
	return Call{Texts: []string{"probe"}, Purpose: memory.PurposeDocument}
}

func checkModel(t *testing.T, c Case) {
	r := newRig(t, c, 0, 0)
	if name, dim := r.e.Model(); name != c.Model || dim != c.Dim {
		t.Errorf("Model = %q, %d; want %q, %d", name, dim, c.Model, c.Dim)
	}
	if n := len(r.up.Requests()); n != 0 {
		t.Errorf("New and Model made %d requests, want none", n)
	}
	if vs, err := r.e.Embed(t.Context(), nil, memory.PurposeQuery); err != nil || len(vs) != 0 || len(r.up.Requests()) != 0 {
		t.Errorf("Embed of no text = %v, %v with %d requests; want nothing and no request", vs, err, len(r.up.Requests()))
	}
}

// checkBatches: each call gives a vector of Dim per text, in order, from
// exactly its cassettes; every request carries the key once, in
// Authorization, and nowhere else.
func checkBatches(t *testing.T, c Case) {
	if len(c.Calls) == 0 {
		t.Fatal("no Calls: a client needs at least one happy path")
	}
	batched := false
	for _, call := range c.Calls {
		if len(call.Cassettes) > 1 {
			batched = true
		}
		r := newRig(t, c, call.BatchSize, 0)
		vs, err := r.e.Embed(t.Context(), call.Texts, call.Purpose)
		if err != nil {
			t.Errorf("%s: Embed: %v", call.Name, err)
			continue
		}
		if len(vs) != len(call.Texts) {
			t.Errorf("%s: %d vectors for %d texts", call.Name, len(vs), len(call.Texts))
		}
		for i, v := range vs {
			if len(v) != c.Dim {
				t.Errorf("%s: vector %d has %d dimensions, want %d", call.Name, i, len(v), c.Dim)
			}
		}
		var got []string
		for _, req := range r.up.Requests() {
			got = append(got, req.Cassette)
			checkKey(t, call.Name, req)
		}
		if !slices.Equal(got, call.Cassettes) {
			t.Errorf("%s: requested cassettes %v, want %v", call.Name, got, call.Cassettes)
		}
	}
	if !batched {
		t.Error("no Call spans more than one request: batching is not checked")
	}
}

func checkKey(t *testing.T, name string, req toolsettest.Request) {
	t.Helper()
	if got := req.Header.Values("Authorization"); !slices.Equal(got, []string{"Bearer " + Canary}) {
		t.Errorf("%s: Authorization %q, want the key once", name, got)
	}
	for k, vs := range req.Header {
		for _, v := range vs {
			if k != "Authorization" && strings.Contains(v, Canary) {
				t.Errorf("%s: the key is in header %s", name, k)
			}
		}
	}
	if bytes.Contains(req.Body, []byte(Canary)) || strings.Contains(req.Path, Canary) || strings.Contains(req.Query.Encode(), Canary) {
		t.Errorf("%s: the key is in the URL or the body", name)
	}
}

// embedAsync runs Embed of the first call in a goroutine.
func (r *rig) embedAsync(ctx context.Context, call Call) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := r.e.Embed(ctx, call.Texts, call.Purpose)
		done <- err
	}()
	return done
}

// waitTimer waits (10 s at most, so a client that never waits fails rather
// than hangs) until the client arms a timer on the injected clock.
func (r *rig) waitTimer(t *testing.T) error {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := r.env.Clock.WaitForTimers(ctx, 1); err != nil {
		return errors.New("[retry] no wait on the injected clock after a 429: wait with it, never time.Sleep or not at all")
	}
	return nil
}

func (r *rig) rateLimited(retryAfter string) toolsettest.Response {
	h := http.Header{"Content-Type": {"application/json"}}
	if retryAfter != "" {
		h.Set("Retry-After", retryAfter)
	}
	return toolsettest.Response{Status: http.StatusTooManyRequests, Header: h, Body: []byte(`{}`)}
}

// checkRetry: a 429 is retried once, after Retry-After on the injected
// clock; a second 429 fails with ErrRateLimited; a cancel during the wait
// returns at once; a Retry-After over 10 s fails at once.
func checkRetry(t *testing.T, c Case) {
	call := c.Calls[0]
	t.Run("then200", func(t *testing.T) {
		r := newRig(t, c, call.BatchSize, 0)
		r.up.Script(r.rateLimited("2"))
		done := r.embedAsync(t.Context(), call)
		if err := r.waitTimer(t); err != nil {
			t.Fatalf("no wait on the injected clock after a 429: %v", err)
		}
		if n := len(r.up.Requests()); n != 1 {
			t.Errorf("%d requests before Retry-After passed, want 1", n)
		}
		r.env.Clock.Advance(time.Second)
		select {
		case err := <-done:
			t.Fatalf("returned (%v) before Retry-After (2 s) passed", err)
		default:
		}
		r.env.Clock.Advance(time.Second)
		if err := <-done; err != nil {
			t.Errorf("429 then the cassette: %v", err)
		}
	})
	t.Run("twice", func(t *testing.T) {
		r := newRig(t, c, call.BatchSize, 0)
		r.up.Script(r.rateLimited(""), r.rateLimited(""))
		done := r.embedAsync(t.Context(), call)
		if err := r.waitTimer(t); err != nil {
			t.Fatal(err)
		}
		r.env.Clock.Advance(time.Second)
		if err := <-done; err == nil {
			t.Errorf("two 429s succeeded")
		}
		if n := len(r.up.Requests()); n != 2 {
			t.Errorf("two 429s: %d requests, want 2 (one retry)", n)
		}
	})
	t.Run("canceled", func(t *testing.T) {
		r := newRig(t, c, call.BatchSize, 0)
		r.up.Script(r.rateLimited("5"))
		ctx, cancel := context.WithCancel(t.Context())
		done := r.embedAsync(ctx, call)
		if err := r.waitTimer(t); err != nil {
			t.Fatal(err)
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("canceled during the wait: %v, want context.Canceled", err)
		}
	})
	t.Run("tooLong", func(t *testing.T) {
		r := newRig(t, c, call.BatchSize, 0)
		r.up.Script(r.rateLimited("60"))
		if _, err := r.e.Embed(t.Context(), call.Texts, call.Purpose); err == nil {
			t.Error("a Retry-After of 60 s succeeded")
		}
		if n := len(r.up.Requests()); n != 1 {
			t.Errorf("a Retry-After of 60 s: %d requests, want 1 (no wait)", n)
		}
	})
}

// checkNoRetry: 400, 401, 403, 500, 502 and 503 fail after one request.
func checkNoRetry(t *testing.T, c Case) {
	for _, status := range []int{400, 401, 403, 404, 500, 502, 503} {
		r := newRig(t, c, 0, 0)
		r.up.Script(toolsettest.Response{Status: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: c.ErrorBody("nope")})
		if _, err := r.e.Embed(t.Context(), r.first().Texts, memory.PurposeDocument); err == nil {
			t.Errorf("status %d succeeded", status)
		}
		if n := len(r.up.Requests()); n != 1 {
			t.Errorf("status %d: %d requests, want 1", status, n)
		}
	}
}

// checkBodyLimit: a response over MaxBodyBytes fails, and at most the limit
// and one byte of it are read.
func checkBodyLimit(t *testing.T, c Case) {
	const limit = 1 << 10
	r := newRig(t, c, 0, limit)
	r.up.Script(toolsettest.Response{Body: append([]byte(`{"data": "`), bytes.Repeat([]byte("x"), 8*limit)...)})
	if _, err := r.e.Embed(t.Context(), r.first().Texts, memory.PurposeDocument); err == nil {
		t.Error("a body over the limit succeeded")
	}
	if n := r.read.Load(); n > limit+1 {
		t.Errorf("read %d bytes of a body over the %d-byte limit, want at most %d", n, limit, limit+1)
	}
}

// checkCanary: the key, echoed in error bodies, is in no error and no log
// line, at any level.
func checkCanary(t *testing.T, c Case) {
	r := newRig(t, c, 0, 0)
	for _, status := range []int{400, 401, 422, 500} {
		r.up.Script(toolsettest.Response{
			Status: status, Header: http.Header{"Content-Type": {"application/json"}},
			Body: c.ErrorBody("invalid request: key " + Canary + " is not valid"),
		})
		_, err := r.e.Embed(t.Context(), r.first().Texts, memory.PurposeDocument)
		if err == nil {
			t.Errorf("status %d succeeded", status)
		} else if strings.Contains(err.Error(), Canary) {
			t.Errorf("status %d: the key is in the error: %v", status, err)
		}
	}
	// A transport failure, whose text could name the request.
	r.up.Script(toolsettest.Response{Hang: true})
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := r.e.Embed(ctx, r.first().Texts, memory.PurposeDocument); err == nil || strings.Contains(err.Error(), Canary) {
		t.Errorf("hanging upstream: %v", err)
	}
	for _, req := range r.up.Requests() {
		checkKey(t, "canary", req)
	}
	if strings.Contains(r.log.String(), Canary) {
		t.Errorf("the key is in the log:\n%s", r.log.String())
	}
}

// checkBadResponses: malformed JSON and the wrong count of vectors are
// errors; a vector of another dimension is memory.ErrEmbedderMismatch.
func checkBadResponses(t *testing.T, c Case) {
	vec := func(n int) string { return "[" + strings.TrimSuffix(strings.Repeat("0.5,", n), ",") + "]" }
	for name, tc := range map[string]struct {
		body     string
		mismatch bool
	}{
		"notJSON":   {body: `{"data": [`},
		"none":      {body: `{"data": []}`},
		"two":       {body: `{"data": [{"index": 0, "embedding": ` + vec(c.Dim) + `}, {"index": 1, "embedding": ` + vec(c.Dim) + `}]}`},
		"badIndex":  {body: `{"data": [{"index": 7, "embedding": ` + vec(c.Dim) + `}]}`},
		"shortVec":  {body: `{"data": [{"index": 0, "embedding": ` + vec(c.Dim-1) + `}]}`, mismatch: true},
		"stringVec": {body: `{"data": [{"index": 0, "embedding": "AAAA"}]}`},
	} {
		r := newRig(t, c, 0, 0)
		r.up.Script(toolsettest.Response{Header: http.Header{"Content-Type": {"application/json"}}, Body: []byte(tc.body)})
		vs, err := r.e.Embed(t.Context(), r.first().Texts, memory.PurposeDocument)
		if err == nil || vs != nil || tc.mismatch != errors.Is(err, memory.ErrEmbedderMismatch) {
			t.Errorf("%s: %v, %v; want an error (ErrEmbedderMismatch: %v)", name, vs, err, tc.mismatch)
		}
	}
}

// checkCanceled: a hanging provider and a deadline give the context's
// error.
func checkCanceled(t *testing.T, c Case) {
	r := newRig(t, c, 0, 0)
	r.up.Script(toolsettest.Response{Hang: true})
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := r.e.Embed(ctx, r.first().Texts, memory.PurposeDocument); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("hanging upstream with a deadline: %v, want context.DeadlineExceeded", err)
	}
}
