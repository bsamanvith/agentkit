package toolsettest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

// Cassette file suffixes: one exchange is <name>.req.json and <name>.res.http
// in a cassette directory.
const (
	reqSuffix = ".req.json"
	resSuffix = ".res.http"
)

// CassetteRequest is the stored form of a recorded request, <name>.req.json.
// Headers are not stored: the suite checks authentication itself.
type CassetteRequest struct {
	Method string              `json:"method"`
	Path   string              `json:"path"`
	Query  map[string][]string `json:"query,omitempty"`
	Body   json.RawMessage     `json:"body,omitempty"`
	// RecordedAt is the day [Record] wrote the cassette (2006-01-02); [Drift]
	// warns when it is over 90 days old.
	RecordedAt string `json:"recorded_at,omitempty"`
	// Synthetic marks a cassette written by hand or generated, not recorded
	// from the real API. The suite lists synthetic cassettes; a connector
	// with any stays in review until they are recorded.
	Synthetic bool `json:"synthetic,omitempty"`
}

type cassette struct {
	name string
	req  CassetteRequest
	body any // req.Body decoded, nil if none
	res  []byte
}

// Request is one request an [Upstream] received.
type Request struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header // as sent, without the kit's own marker header
	Body   []byte
	// Cassette names the cassette that answered, "" for a scripted response
	// or a request no cassette matched.
	Cassette string
}

// Response is a scripted answer of an [Upstream].
type Response struct {
	Status int // default 200
	Header http.Header
	Body   []byte
	// Delay holds the response back this long (real time) unless the client
	// gives up first.
	Delay time.Duration
	// Hang holds the request until the client gives up or the test ends: a
	// provider that stopped answering.
	Hang bool
}

// Upstream is a stand-in for a provider's API: an httptest server that
// answers from cassettes, or from scripted responses queued with
// [Upstream.Script], and records every request.
//
// A request matches a cassette when method, path and query are equal and the
// bodies are equal as JSON (key order and spacing ignored). A request no
// cassette matches fails the test with a diff against the closest one.
//
// Requests must come through [Upstream.Client]: one that does not (a
// connector that ignores the injected client) fails the test.
type Upstream struct {
	tb        testing.TB
	srv       *httptest.Server
	dir       string
	cassettes []cassette
	via       string // the value the hermetic client stamps on every request
	release   chan struct{}
	closeOnce sync.Once
	client    *http.Client
	hermetic  *hermetic

	mu       sync.Mutex
	script   []Response
	requests []Request
}

// NewUpstream starts an upstream serving the cassettes in dir ("" for none, a
// purely scripted upstream). It is closed when the test ends.
func NewUpstream(tb testing.TB, dir string) *Upstream {
	tb.Helper()
	u := &Upstream{tb: tb, dir: dir, via: newToken(), release: make(chan struct{})}
	if dir != "" {
		cs, err := loadCassettes(dir)
		if err != nil {
			tb.Fatalf("toolsettest: %v", err)
		}
		u.cassettes = cs
	}
	u.srv = httptest.NewServer(http.HandlerFunc(u.serve))
	u.hermetic = newHermetic(tb, u.via, u.srv.Listener.Addr().String())
	u.client = &http.Client{Transport: u.hermetic}
	tb.Cleanup(func() {
		u.releaseHangs() // before Close, which waits for handlers
		u.srv.Close()
		u.hermetic.close()
	})
	return u
}

// URL is the upstream's base URL, to hand to a connector as its base URL.
func (u *Upstream) URL() string { return u.srv.URL }

// Client returns the hermetic client: it reaches this upstream and refuses
// every other host, failing the test. It is the only client the suite hands
// out, so a unit test can never reach a paid API.
func (u *Upstream) Client() *http.Client { return u.client }

// Script queues responses. While the queue is not empty, each request gets
// the next one whatever it asks; then cassettes answer again.
func (u *Upstream) Script(rs ...Response) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.script = append(u.script, rs...)
}

// Requests returns copies of the requests received so far, oldest first.
func (u *Upstream) Requests() []Request {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := make([]Request, len(u.requests))
	for i, r := range u.requests {
		r.Header = r.Header.Clone()
		r.Query = cloneValues(r.Query)
		r.Body = slices.Clone(r.Body)
		out[i] = r
	}
	return out
}

// releaseHangs ends every held request, so a client that ignores its context
// still gets an answer and the server can close.
func (u *Upstream) releaseHangs() { u.closeOnce.Do(func() { close(u.release) }) }

func (u *Upstream) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		u.tb.Errorf("toolsettest: upstream: reading request: %v", err)
		return
	}
	header := r.Header.Clone()
	via := header.Get(viaHeader)
	header.Del(viaHeader)
	if via != u.via {
		u.tb.Errorf("[hermetic] %s %s did not come through Env.HTTPClient: a connector must send every request with the injected client", r.Method, r.URL.Path)
	}
	req := Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Header: header, Body: body}

	u.mu.Lock()
	var scripted *Response
	if len(u.script) > 0 {
		s := u.script[0]
		scripted = &s
		u.script = u.script[1:]
	}
	var match *cassette
	if scripted == nil {
		match = u.match(req)
		if match != nil {
			req.Cassette = match.name
		}
	}
	u.requests = append(u.requests, req)
	u.mu.Unlock()

	switch {
	case scripted != nil:
		u.writeScripted(w, r, *scripted)
	case match != nil:
		u.writeCassette(w, r, match)
	default:
		u.tb.Errorf("[upstream] no cassette in %s matches the request\n%s", u.dir, u.closest(req))
		http.Error(w, "toolsettest: no cassette matches this request", http.StatusTeapot)
	}
}

func (u *Upstream) writeScripted(w http.ResponseWriter, r *http.Request, s Response) {
	if s.Delay > 0 {
		t := time.NewTimer(s.Delay)
		defer t.Stop()
		select {
		case <-t.C:
		case <-r.Context().Done():
			return
		case <-u.release:
		}
	}
	if s.Hang {
		select {
		case <-r.Context().Done():
		case <-u.release:
		}
		return
	}
	for k, vs := range s.Header {
		w.Header()[k] = vs
	}
	status := s.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(s.Body)
}

func (u *Upstream) writeCassette(w http.ResponseWriter, r *http.Request, c *cassette) {
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(c.res)), r)
	if err != nil {
		u.tb.Errorf("toolsettest: cassette %s: %v", c.name, err)
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		u.tb.Errorf("toolsettest: cassette %s body: %v", c.name, err)
		return
	}
	for k, vs := range resp.Header {
		w.Header()[k] = vs
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

// match returns the cassette that answers req, or nil.
func (u *Upstream) match(req Request) *cassette {
	for i := range u.cassettes {
		if u.cassettes[i].matches(req) {
			return &u.cassettes[i]
		}
	}
	return nil
}

func (c *cassette) matches(req Request) bool {
	if c.req.Method != req.Method || c.req.Path != req.Path || !equalValues(c.req.Query, req.Query) {
		return false
	}
	got, ok := decodeBody(req.Body)
	return ok && reflect.DeepEqual(c.body, got)
}

// closest describes how req differs from the most similar cassette.
func (u *Upstream) closest(req Request) string {
	got := stored(req)
	if len(u.cassettes) == 0 {
		b, _ := json.MarshalIndent(got, "", "  ")
		return fmt.Sprintf("there are no cassettes; the request was:\n%s", b)
	}
	// The closest is the smallest diff, preferring the same endpoint.
	best, bestDiff, bestOther := "", "", true
	for _, c := range u.cassettes {
		d := cmp.Diff(comparable(c.req), comparable(got))
		other := c.req.Method != req.Method || c.req.Path != req.Path
		if best == "" || (!other && bestOther) || (other == bestOther && len(d) < len(bestDiff)) {
			best, bestDiff, bestOther = c.name, d, other
		}
	}
	return fmt.Sprintf("closest cassette %s (-cassette +request):\n%s", best, strings.TrimSpace(bestDiff))
}

// comparable turns a stored request into plain values for a readable diff.
func comparable(r CassetteRequest) map[string]any {
	m := map[string]any{"method": r.Method, "path": r.Path}
	if len(r.Query) > 0 {
		m["query"] = r.Query
	}
	if b, ok := decodeBody(r.Body); ok && b != nil {
		m["body"] = b
	} else if len(r.Body) > 0 {
		m["body"] = string(r.Body)
	}
	return m
}

// stored is req as a cassette would store it.
func stored(req Request) CassetteRequest {
	c := CassetteRequest{Method: req.Method, Path: req.Path}
	if len(req.Query) > 0 {
		c.Query = req.Query
	}
	if len(bytes.TrimSpace(req.Body)) > 0 {
		if json.Valid(req.Body) {
			c.Body = json.RawMessage(req.Body)
		} else {
			c.Body, _ = json.Marshal(string(req.Body)) // keeps the diff readable
		}
	}
	return c
}

// decodeBody decodes a JSON body; an empty body is nil. ok is false for a
// body that is not JSON.
func decodeBody(b []byte) (any, bool) {
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, true
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, false
	}
	return v, true
}

func equalValues(a, b map[string][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		if !slices.Equal(va, b[k]) {
			return false
		}
	}
	return true
}

func cloneValues(v url.Values) url.Values {
	if v == nil {
		return nil
	}
	out := make(url.Values, len(v))
	for k, vs := range v {
		out[k] = slices.Clone(vs)
	}
	return out
}

// loadCassettes reads every <name>.req.json and its <name>.res.http in dir.
func loadCassettes(dir string) ([]cassette, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("cassettes: %w", err)
	}
	var out []cassette
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), reqSuffix)
		if !ok || e.IsDir() {
			continue
		}
		c, err := loadCassette(dir, name)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

func loadCassette(dir, name string) (cassette, error) {
	c := cassette{name: name}
	raw, err := os.ReadFile(filepath.Join(dir, name+reqSuffix))
	if err != nil {
		return c, fmt.Errorf("cassette %s: %w", name, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c.req); err != nil {
		return c, fmt.Errorf("cassette %s%s: %w", name, reqSuffix, err)
	}
	if c.req.Method == "" || !strings.HasPrefix(c.req.Path, "/") {
		return c, fmt.Errorf("cassette %s%s: method and an absolute path are required", name, reqSuffix)
	}
	if c.body, _ = decodeBody(c.req.Body); len(c.req.Body) > 0 && !json.Valid(c.req.Body) {
		return c, fmt.Errorf("cassette %s%s: body is not JSON", name, reqSuffix)
	}
	if c.res, err = os.ReadFile(filepath.Join(dir, name+resSuffix)); err != nil {
		return c, fmt.Errorf("cassette %s: %w", name, err)
	}
	if _, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(c.res)), nil); err != nil {
		return c, fmt.Errorf("cassette %s%s: not an HTTP response: %w", name, resSuffix, err)
	}
	return c, nil
}
