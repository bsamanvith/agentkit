package toolsettest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// RecordEnv is the environment variable that, set to 1, makes connectors'
// live recording tests run: `make record-cassettes` sets it.
const RecordEnv = "AGENTKIT_RECORD_CASSETTES"

// SkipUnlessRecording skips the test unless [RecordEnv] is 1.
func SkipUnlessRecording(tb testing.TB) {
	tb.Helper()
	if os.Getenv(RecordEnv) != "1" {
		tb.Skipf("set %s=1 to record cassettes from the real API (make record-cassettes)", RecordEnv)
	}
}

// Recording says where [Record] and [Drift] find the real API and its key.
type Recording struct {
	// BaseURL is the real API's base URL.
	BaseURL string
	// LookupEnv reads the real key: os.LookupEnv in a live test.
	LookupEnv func(string) (string, bool)
	// Client reaches BaseURL (default: a client with a 2-minute timeout).
	Client *http.Client
	// Dir is where cassettes are written (default: Connector.Cassettes).
	Dir string
	// Scrub, if set, rewrites every response body before the connector or a
	// cassette sees it: replace account IDs, emails and the like with
	// stand-ins. The key itself needs no scrubbing: a recording that would
	// store it is refused.
	Scrub func(body []byte) []byte
}

// keptHeaders are the only response headers a cassette keeps.
var keptHeaders = []string{"Content-Type", "Retry-After"}

// exchange is one recorded request and response.
type exchange struct {
	req    CassetteRequest
	status int
	header http.Header
	body   []byte
}

// liveCall is one happy call made against the real API.
type liveCall struct {
	call      Call
	out       json.RawMessage
	schemaErr error      // the output fails the tool's output schema
	exchanges []exchange // the requests it sent, Cassettes[i] for the i-th
}

// live is a run of a connector's happy calls against the real API through a
// recording proxy.
type live struct {
	key   string // "" when keyless
	calls []liveCall
	logs  *logSink
}

// runLive makes c's happy calls against rec.BaseURL through a recording
// proxy. It skips the test when the key is not set (and not optional).
func runLive(tb testing.TB, c Connector, rec Recording, what string) *live {
	tb.Helper()
	if rec.Client == nil {
		rec.Client = &http.Client{Timeout: 2 * time.Minute}
	}
	if rec.LookupEnv == nil {
		tb.Fatalf("toolsettest: %s: Recording.LookupEnv is nil", what)
	}
	l := &live{logs: &logSink{}}
	if c.KeyEnv != "" {
		k, ok := rec.LookupEnv(c.KeyEnv)
		switch {
		case ok && k != "":
			l.key = k
		case !c.KeyOptional:
			tb.Skipf("toolsettest: %s: %s is not set", what, c.KeyEnv)
		}
	}

	p := &proxy{tb: tb, base: strings.TrimSuffix(rec.BaseURL, "/"), client: rec.Client, scrub: rec.Scrub}
	srv := httptest.NewServer(p)
	tb.Cleanup(srv.Close)
	h := newHermetic(tb, newToken(), srv.Listener.Addr().String())
	tb.Cleanup(h.close)
	env := Env{
		BaseURL:    srv.URL,
		HTTPClient: &http.Client{Transport: h},
		LookupEnv:  rec.LookupEnv,
		Clock:      clock.Real(),
		Logger:     slog.New(slog.NewTextHandler(l.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	ctx, cancel := context.WithTimeout(tb.Context(), 5*time.Minute)
	defer cancel()
	built := map[string][]tool.Tool{}
	for _, call := range c.Calls {
		settings := mergeSettings(c.Settings, call.Settings)
		tools, ok := built[string(settings)]
		if !ok {
			var err error
			if tools, err = c.New(env).Build(ctx, toolset.BuildInput{Settings: settings}); err != nil {
				tb.Fatalf("toolsettest: %s: Build(%s): %v", what, settings, err)
			}
			built[string(settings)] = tools
		}
		before := len(p.recorded())
		tl := find(tools, call.Tool)
		if tl == nil {
			tb.Fatalf("toolsettest: %s: no tool %q", what, call.Tool)
		}
		out, err := tl.Invoke(ctx, call.Input)
		if err != nil {
			tb.Fatalf("toolsettest: %s: %s: %v", what, call.Name, err)
		}
		got := p.recorded()[before:]
		if len(got) != len(call.Cassettes) {
			tb.Fatalf("toolsettest: %s: %s sent %d request(s), but its Cassettes lists %d", what, call.Name, len(got), len(call.Cassettes))
		}
		l.calls = append(l.calls, liveCall{
			call: call, out: out, exchanges: got,
			schemaErr: validate(tl.Definition().OutputSchema, out),
		})
	}
	return l
}

// Record runs each of c's happy calls against the real API through a
// recording proxy and writes their cassettes: the i-th request a call sends
// becomes Cassettes[i]. Then it replays the calls from the files it wrote and
// checks that they give the same output.
//
// It scrubs as it writes: no request header is stored, and only Content-Type
// and Retry-After of the response; [Recording.Scrub] rewrites response
// bodies. Nothing is written, and the test fails, if the key appears in a
// request's path, query or body, in a response or in a log line, or if a
// cassette would hold anything shaped like a secret (a known key format, a
// credential-named field with a random value, a cookie).
//
// A call whose output fails its tool's output schema fails the test and its
// cassettes are not written: a cassette must show the connector working.
// Each request file records the day it was recorded, for [Drift].
//
// A connector's live test calls it, gated by [SkipUnlessRecording]:
//
//	//go:build live
//	func TestRecordCassettes(t *testing.T) {
//		toolsettest.SkipUnlessRecording(t)
//		toolsettest.Record(t, connector(), toolsettest.Recording{BaseURL: "https://api.tavily.com", LookupEnv: os.LookupEnv})
//	}
//
// Golden files are not touched: run `make golden` after recording and review
// both diffs.
func Record(tb testing.TB, c Connector, rec Recording) {
	tb.Helper()
	c = normalize(tb, c)
	if rec.Dir == "" {
		rec.Dir = c.Cassettes
	}
	l := runLive(tb, c, rec, "Record")

	files := map[string]exchange{}
	var kept []liveCall
	for _, lc := range l.calls {
		if lc.schemaErr != nil {
			tb.Errorf("toolsettest: Record: %s: the output from the real API fails the output schema, so its cassettes are not written: %v\n%s", lc.call.Name, lc.schemaErr, lc.out)
			continue
		}
		kept = append(kept, lc)
		for i, ex := range lc.exchanges {
			name := lc.call.Cassettes[i]
			if prev, ok := files[name]; ok && !sameExchange(prev, ex) {
				tb.Fatalf("toolsettest: Record: two different exchanges are both named %s", name)
			}
			files[name] = ex
		}
	}
	names := slices.Sorted(maps.Keys(files))
	for _, name := range names {
		ex := files[name]
		if l.key != "" {
			if err := ex.leaks(l.key); err != nil {
				tb.Fatalf("toolsettest: Record: refusing to write cassette %s: %v", name, err)
			}
		}
		req, res, err := ex.render()
		if err != nil {
			tb.Fatalf("toolsettest: Record: %s: %v", name, err)
		}
		if found := findSecrets(append(req, res...)); len(found) > 0 {
			tb.Fatalf("toolsettest: Record: refusing to write cassette %s: it holds %s (scrub it with Recording.Scrub)", name, strings.Join(found, "; "))
		}
	}
	if l.key != "" && strings.Contains(l.logs.String(), l.key) {
		tb.Fatalf("toolsettest: Record: the API key appears in a log line; nothing written")
	}

	if err := os.MkdirAll(rec.Dir, 0o755); err != nil {
		tb.Fatal(err)
	}
	for _, name := range names {
		if err := files[name].write(rec.Dir, name); err != nil {
			tb.Fatalf("toolsettest: Record: %v", err)
		}
	}

	// The cassettes must replay to what the real API gave.
	if len(kept) == 0 {
		return
	}
	replayed := c
	replayed.Cassettes = rec.Dir
	replayed.Calls = nil
	for _, lc := range kept {
		replayed.Calls = append(replayed.Calls, lc.call)
	}
	s := newSuite(tb, replayed, false)
	r := s.newRig(tb, modeKeyed)
	for _, lc := range kept {
		out, err := r.invoke(tb.Context(), r.toolsFor(lc.call), lc.call.Tool, lc.call.Input)
		if err != nil {
			tb.Errorf("toolsettest: Record: %s replayed from the cassettes: %v", lc.call.Name, err)
			continue
		}
		if !jsonEqual(out, lc.out) {
			tb.Errorf("toolsettest: Record: %s replayed from the cassettes gives\n%s\nbut the real API gave\n%s", lc.call.Name, out, lc.out)
		}
	}
}

// proxy forwards requests to the real API and records the exchanges.
type proxy struct {
	tb     testing.TB
	base   string
	client *http.Client
	scrub  func([]byte) []byte

	mu        sync.Mutex
	exchanges []exchange
}

func (p *proxy) recorded() []exchange {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]exchange(nil), p.exchanges...)
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		p.tb.Errorf("toolsettest: Record: reading request: %v", err)
		return
	}
	if len(bytes.TrimSpace(body)) > 0 && !json.Valid(body) {
		p.tb.Errorf("toolsettest: Record: %s %s has a body that is not JSON; cassettes store JSON bodies", r.Method, r.URL.Path)
		http.Error(w, "toolsettest: not JSON", http.StatusBadGateway)
		return
	}
	target := p.base + r.URL.Path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	out, err := http.NewRequestWithContext(r.Context(), r.Method, target, bytes.NewReader(body))
	if err != nil {
		p.tb.Errorf("toolsettest: Record: %v", err)
		return
	}
	out.Header = r.Header.Clone()
	for _, h := range []string{viaHeader, "Accept-Encoding", "Content-Length", "Connection"} {
		out.Header.Del(h)
	}
	resp, err := p.client.Do(out)
	if err != nil {
		p.tb.Errorf("toolsettest: Record: %s %s: %v", r.Method, r.URL.Path, err)
		http.Error(w, "toolsettest: upstream unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		p.tb.Errorf("toolsettest: Record: reading response: %v", err)
		return
	}
	if p.scrub != nil {
		respBody = p.scrub(respBody)
	}
	req := stored(Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Body: body})
	req.RecordedAt = time.Now().UTC().Format(time.DateOnly)
	ex := exchange{req: req, status: resp.StatusCode, header: http.Header{}, body: respBody}
	for _, h := range keptHeaders {
		if vs := resp.Header.Values(h); len(vs) > 0 {
			ex.header[h] = vs
		}
	}
	p.mu.Lock()
	p.exchanges = append(p.exchanges, ex)
	p.mu.Unlock()
	// The connector sees what a replay will serve: the kept headers and the
	// scrubbed body.
	for k, vs := range ex.header {
		w.Header()[k] = vs
	}
	w.WriteHeader(ex.status)
	_, _ = w.Write(ex.body)
}

// leaks reports where an exchange carries the key, if anywhere.
func (ex exchange) leaks(key string) error {
	switch {
	case strings.Contains(ex.req.Path, key):
		return fmt.Errorf("the key is in the request path")
	case strings.Contains(fmt.Sprint(ex.req.Query), key):
		return fmt.Errorf("the key is in the request query")
	case bytes.Contains(ex.req.Body, []byte(key)):
		return fmt.Errorf("the key is in the request body")
	case bytes.Contains(ex.body, []byte(key)):
		return fmt.Errorf("the key is in the response body")
	case strings.Contains(fmt.Sprint(ex.header), key):
		return fmt.Errorf("the key is in a response header")
	}
	return nil
}

func sameExchange(a, b exchange) bool {
	ja, _ := json.Marshal(a.req)
	jb, _ := json.Marshal(b.req)
	return bytes.Equal(ja, jb) && a.status == b.status && bytes.Equal(a.body, b.body)
}

// render returns the exchange's two files: <name>.req.json and <name>.res.http.
func (ex exchange) render() (req, res []byte, err error) {
	req, err = json.MarshalIndent(ex.req, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "HTTP/1.1 %d %s\n", ex.status, http.StatusText(ex.status))
	for _, h := range keptHeaders {
		for _, v := range ex.header.Values(h) {
			fmt.Fprintf(&b, "%s: %s\n", h, v)
		}
	}
	b.WriteString("\n")
	b.Write(ex.body)
	return append(req, '\n'), b.Bytes(), nil
}

// write stores the exchange as <name>.req.json and <name>.res.http.
func (ex exchange) write(dir, name string) error {
	req, res, err := ex.render()
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, name+reqSuffix), req, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name+resSuffix), res, 0o644)
}
