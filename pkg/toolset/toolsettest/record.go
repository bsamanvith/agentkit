package toolsettest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
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

// Recording says where [Record] finds the real API and its key.
type Recording struct {
	// BaseURL is the real API's base URL.
	BaseURL string
	// LookupEnv reads the real key: os.LookupEnv in a live test.
	LookupEnv func(string) (string, bool)
	// Client reaches BaseURL (default: a client with a 2-minute timeout).
	Client *http.Client
	// Dir is where cassettes are written (default: Connector.Cassettes).
	Dir string
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

// Record runs each of c's happy calls against the real API through a
// recording proxy and writes their cassettes: the i-th request a call sends
// becomes Cassettes[i]. Then it replays the calls from the files it wrote and
// checks that they give the same output.
//
// It scrubs as it writes: no request header is stored, and only Content-Type
// and Retry-After of the response. If the key appears in a request's URL or
// body, or in a response, nothing is written and the test fails.
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
func Record(t *testing.T, c Connector, rec Recording) {
	t.Helper()
	c = normalize(t, c)
	if rec.Dir == "" {
		rec.Dir = c.Cassettes
	}
	if rec.Client == nil {
		rec.Client = &http.Client{Timeout: 2 * time.Minute}
	}
	if rec.LookupEnv == nil {
		t.Fatal("toolsettest: Record: Recording.LookupEnv is nil")
	}
	key := ""
	if c.KeyEnv != "" {
		k, ok := rec.LookupEnv(c.KeyEnv)
		switch {
		case ok && k != "":
			key = k
		case !c.KeyOptional:
			t.Skipf("toolsettest: Record: %s is not set", c.KeyEnv)
		}
	}

	p := &proxy{t: t, base: strings.TrimSuffix(rec.BaseURL, "/"), client: rec.Client}
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	h := newHermetic(t, newToken(), srv.Listener.Addr().String())
	t.Cleanup(h.close)
	logs := &logSink{}
	env := Env{
		BaseURL:    srv.URL,
		HTTPClient: &http.Client{Transport: h},
		LookupEnv:  rec.LookupEnv,
		Clock:      clock.Real(),
		Logger:     slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	tools, err := c.New(env).Build(ctx, c.Settings)
	if err != nil {
		t.Fatalf("toolsettest: Record: Build: %v", err)
	}

	files := map[string]exchange{}
	live := map[string]json.RawMessage{}
	for _, call := range c.Calls {
		before := len(p.recorded())
		tl := find(tools, call.Tool)
		if tl == nil {
			t.Fatalf("toolsettest: Record: no tool %q", call.Tool)
		}
		out, err := tl.Invoke(ctx, call.Input)
		if err != nil {
			t.Fatalf("toolsettest: Record: %s: %v", call.Name, err)
		}
		live[call.Name] = out
		got := p.recorded()[before:]
		if len(got) != len(call.Cassettes) {
			t.Fatalf("toolsettest: Record: %s sent %d request(s), but its Cassettes lists %d", call.Name, len(got), len(call.Cassettes))
		}
		for i, ex := range got {
			name := call.Cassettes[i]
			if prev, ok := files[name]; ok && !sameExchange(prev, ex) {
				t.Fatalf("toolsettest: Record: two different exchanges are both named %s", name)
			}
			files[name] = ex
		}
	}
	if key != "" {
		for name, ex := range files {
			if err := ex.leaks(key); err != nil {
				t.Fatalf("toolsettest: Record: refusing to write cassette %s: %v", name, err)
			}
		}
		if strings.Contains(logs.String(), key) {
			t.Fatalf("toolsettest: Record: the API key appears in a log line; nothing written")
		}
	}

	if err := os.MkdirAll(rec.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, ex := range files {
		if err := ex.write(rec.Dir, name); err != nil {
			t.Fatalf("toolsettest: Record: %v", err)
		}
	}

	// The cassettes must replay to what the real API gave.
	replayed := c
	replayed.Cassettes = rec.Dir
	s := newSuite(t, replayed, false)
	r := s.newRig(t, modeKeyed)
	rtools := r.mustBuild()
	for _, call := range c.Calls {
		out, err := r.invoke(t.Context(), rtools, call.Tool, call.Input)
		if err != nil {
			t.Errorf("toolsettest: Record: %s replayed from the cassettes: %v", call.Name, err)
			continue
		}
		if !jsonEqual(out, live[call.Name]) {
			t.Errorf("toolsettest: Record: %s replayed from the cassettes gives\n%s\nbut the real API gave\n%s", call.Name, out, live[call.Name])
		}
	}
}

// proxy forwards requests to the real API and records the exchanges.
type proxy struct {
	t      testing.TB
	base   string
	client *http.Client

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
		p.t.Errorf("toolsettest: Record: reading request: %v", err)
		return
	}
	if len(bytes.TrimSpace(body)) > 0 && !json.Valid(body) {
		p.t.Errorf("toolsettest: Record: %s %s has a body that is not JSON; cassettes store JSON bodies", r.Method, r.URL.Path)
		http.Error(w, "toolsettest: not JSON", http.StatusBadGateway)
		return
	}
	target := p.base + r.URL.Path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	out, err := http.NewRequestWithContext(r.Context(), r.Method, target, bytes.NewReader(body))
	if err != nil {
		p.t.Errorf("toolsettest: Record: %v", err)
		return
	}
	out.Header = r.Header.Clone()
	for _, h := range []string{viaHeader, "Accept-Encoding", "Content-Length", "Connection"} {
		out.Header.Del(h)
	}
	resp, err := p.client.Do(out)
	if err != nil {
		p.t.Errorf("toolsettest: Record: %s %s: %v", r.Method, r.URL.Path, err)
		http.Error(w, "toolsettest: upstream unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		p.t.Errorf("toolsettest: Record: reading response: %v", err)
		return
	}
	ex := exchange{req: stored(Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Body: body}), status: resp.StatusCode, header: http.Header{}, body: respBody}
	for _, h := range keptHeaders {
		if vs := resp.Header.Values(h); len(vs) > 0 {
			ex.header[h] = vs
		}
	}
	p.mu.Lock()
	p.exchanges = append(p.exchanges, ex)
	p.mu.Unlock()
	// The connector sees what a replay will serve: the kept headers only.
	for k, vs := range ex.header {
		w.Header()[k] = vs
	}
	w.WriteHeader(ex.status)
	_, _ = w.Write(ex.body)
}

// leaks reports where an exchange carries the key, if anywhere.
func (ex exchange) leaks(key string) error {
	switch {
	case strings.Contains(ex.req.Path, key) || strings.Contains(fmt.Sprint(ex.req.Query), key):
		return fmt.Errorf("the key is in the request URL")
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

// write stores the exchange as <name>.req.json and <name>.res.http.
func (ex exchange) write(dir, name string) error {
	req, err := json.MarshalIndent(ex.req, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, name+reqSuffix), append(req, '\n'), 0o644); err != nil {
		return err
	}
	var res bytes.Buffer
	fmt.Fprintf(&res, "HTTP/1.1 %d %s\n", ex.status, http.StatusText(ex.status))
	for _, h := range keptHeaders {
		for _, v := range ex.header.Values(h) {
			fmt.Fprintf(&res, "%s: %s\n", h, v)
		}
	}
	res.WriteString("\n")
	res.Write(ex.body)
	return os.WriteFile(filepath.Join(dir, name+resSuffix), res.Bytes(), 0o644)
}
