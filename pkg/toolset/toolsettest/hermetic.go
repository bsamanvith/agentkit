package toolsettest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
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
type hermetic struct {
	tb      testing.TB
	via     string
	allowed []string
	base    *http.Transport

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
	resp, err := h.base.RoundTrip(out)
	if err != nil {
		return nil, err
	}
	resp.Body = &countingBody{ReadCloser: resp.Body, h: h}
	return resp, nil
}

func (h *hermetic) close() { h.base.CloseIdleConnections() }

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
