package toolsettest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
)

// viaHeader is stamped on every request the hermetic client sends, so the
// upstream can tell a request that bypassed the injected client.
const viaHeader = "X-Toolsettest-Client"

// errRefused is the cause of a request the hermetic client refused.
var errRefused = errors.New("toolsettest: hermetic client refused a request to a host other than the upstream")

// hermetic is the RoundTripper of the only HTTP client the kit hands out. It
// lets requests reach the allowed hosts (host:port) and refuses the rest,
// failing the test; it counts the response body bytes the connector reads.
//
// With a handler, it serves allowed requests in this process instead of over
// a socket: a socket per request (a fuzz iteration makes several) leaves
// ports in TIME_WAIT, and a fuzz run exhausts the machine's ephemeral ports.
type hermetic struct {
	tb      testing.TB
	via     string
	allowed []string
	base    *http.Transport
	handler http.Handler   // nil: over the network, through base
	serving sync.WaitGroup // in-process handlers still running

	mu       sync.Mutex
	maxRead  int64 // most bytes read from one response body
	requests int
}

func newHermetic(tb testing.TB, via string, allowed ...string) *hermetic {
	return &hermetic{tb: tb, via: via, allowed: allowed, base: &http.Transport{}}
}

// probeKey marks a request the suite sends to check that the client refuses
// it; a refused probe does not fail the test.
type probeKey struct{}

func (h *hermetic) RoundTrip(req *http.Request) (*http.Response, error) {
	if !slices.Contains(h.allowed, req.URL.Host) {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		if req.Context().Value(probeKey{}) == nil {
			h.tb.Errorf("[hermetic] refused %s %s: Env.HTTPClient reaches only the upstream (%v); use Env.BaseURL", req.Method, req.URL.Redacted(), h.allowed)
		}
		return nil, fmt.Errorf("%w: %s", errRefused, req.URL.Host)
	}
	out := req.Clone(req.Context())
	out.Header.Set(viaHeader, h.via)
	h.mu.Lock()
	h.requests++
	h.mu.Unlock()
	var resp *http.Response
	var err error
	if h.handler != nil {
		resp, err = h.serveInProcess(out)
	} else {
		resp, err = h.base.RoundTrip(out)
	}
	if err != nil {
		return nil, err
	}
	resp.Body = &countingBody{ReadCloser: resp.Body, h: h}
	return resp, nil
}

// close waits for the in-process handlers (the caller ends held requests
// first) and closes idle connections.
func (h *hermetic) close() {
	h.serving.Wait()
	h.base.CloseIdleConnections()
}

// serveInProcess runs the handler on req as a server would, and returns its
// response once the handler is done, or the context's error if that comes
// first (the handler ends with the context: it is the request's). The body
// fails with the context's error once the context ends, as a connection's
// does.
func (h *hermetic) serveInProcess(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	var body []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
		body = b
	}
	sreq := req.Clone(ctx)
	sreq.Body = io.NopCloser(bytes.NewReader(body))
	sreq.ContentLength = int64(len(body))
	sreq.Host = req.URL.Host
	sreq.RequestURI = req.URL.RequestURI()
	sreq.RemoteAddr = "127.0.0.1:1"
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	h.serving.Add(1)
	go func() {
		defer h.serving.Done()
		defer close(done)
		h.handler.ServeHTTP(rec, sreq)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	resp := rec.Result()
	resp.Request = req
	resp.Body = &ctxBody{ctx: ctx, ReadCloser: resp.Body}
	return resp, nil
}

// ctxBody fails reads once ctx is done.
type ctxBody struct {
	ctx context.Context
	io.ReadCloser
}

func (b *ctxBody) Read(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	return b.ReadCloser.Read(p)
}

// stats returns how many requests went out and the most bytes the connector
// read from one response body.
func (h *hermetic) stats() (requests int, maxRead int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.requests, h.maxRead
}

type countingBody struct {
	io.ReadCloser
	h *hermetic
	n atomic.Int64
}

func (c *countingBody) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	total := c.n.Add(int64(n))
	c.h.mu.Lock()
	c.h.maxRead = max(c.h.maxRead, total)
	c.h.mu.Unlock()
	return n, err
}

// probe checks that the client refuses a host other than the upstream.
func probe(ctx context.Context, c *http.Client) error {
	req, err := http.NewRequestWithContext(context.WithValue(ctx, probeKey{}, true), http.MethodGet, "https://api.toolsettest.invalid/probe", nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		return errors.New("[hermetic] Env.HTTPClient let a request to another host through")
	}
	if !errors.Is(err, errRefused) {
		return fmt.Errorf("[hermetic] Env.HTTPClient tried to reach another host instead of refusing: %w", err)
	}
	return nil
}

func newToken() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
