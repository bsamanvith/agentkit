package parallel_test

// The rig every test uses: a fake Parallel upstream (cassettes or scripted
// responses), the connector built against it with a canary key, a fake clock
// and a captured debug log, and the invariants checked after every test.
//
// Invariants (rig.cleanup and rig.call):
//   - every request is a POST to /v1/search or /v1/extract with a JSON body;
//   - it carries the key in exactly one x-api-key header, and the key appears
//     nowhere else in the request (other headers, path, body);
//   - its body validates against the endpoint's request schema in Parallel's
//     OpenAPI spec (testdata/openapi.json; additionalProperties is false, so
//     nothing is sent where Parallel would refuse it);
//   - no output, error or log line contains the key;
//   - every output is E10's shape: results (and failed) are arrays, never
//     null; every result has a URL; content is within its cap or ends with
//     the truncation marker, cut exactly at the cap.
//
// Until toolsettest (T049) lands, this rig also stands in for its
// RunConnector checks; adopting the kit replaces the generic tests here.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/connectors/parallel"
	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest"
)

// canary is the API key in every test. It must never leave in a body, an
// output, an error or a log line.
const canary = "pk-canary-3f9a7c1e5b2d8046"

// truncated matches the marker that ends cut content.
var truncated = regexp.MustCompile(`…\[truncated [0-9]{1,3}(,[0-9]{3})* characters; [^\]]+\]$`)

// recorded is one request the upstream received.
type recorded struct {
	method, path string
	header       http.Header
	body         []byte
}

// reply is one scripted response; a cassette name serves a recorded one.
type reply struct {
	cassette string
	status   int
	header   map[string]string
	body     string
	hang     bool // hold the request until the client gives up
}

// upstream is a fake Parallel API serving replies in order.
type upstream struct {
	t   *testing.T
	srv *httptest.Server
	mu  sync.Mutex
	got []recorded
	q   []reply
}

func newUpstream(t *testing.T) *upstream {
	u := &upstream{t: t}
	u.srv = httptest.NewServer(http.HandlerFunc(u.serve))
	t.Cleanup(u.srv.Close)
	return u
}

// script queues replies, served one per request in order.
func (u *upstream) script(rs ...reply) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.q = append(u.q, rs...)
}

func (u *upstream) requests() []recorded {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.got)
}

func (u *upstream) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.got = append(u.got, recorded{method: r.Method, path: r.URL.Path, header: r.Header.Clone(), body: body})
	var next reply
	ok := len(u.q) > 0
	if ok {
		next, u.q = u.q[0], u.q[1:]
	}
	u.mu.Unlock()
	switch {
	case !ok:
		u.t.Errorf("unexpected request %s %s %s", r.Method, r.URL.Path, body)
		http.Error(w, "no reply scripted", http.StatusTeapot)
	case next.hang:
		<-r.Context().Done()
	case next.cassette != "":
		u.serveCassette(w, r, body, next.cassette)
	default:
		for k, v := range next.header {
			w.Header().Set(k, v)
		}
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(next.status)
		_, _ = io.WriteString(w, next.body)
	}
}

// serveCassette checks the request against <name>.req.json (method, path and
// a JSON-equal body) and answers with <name>.res.http.
func (u *upstream) serveCassette(w http.ResponseWriter, r *http.Request, body []byte, name string) {
	dir := filepath.Join("testdata", "cassettes")
	reqFile, err := os.ReadFile(filepath.Join(dir, name+".req.json"))
	if err != nil {
		u.t.Errorf("cassette %s: %v", name, err)
		return
	}
	var want struct {
		Method string          `json:"method"`
		Path   string          `json:"path"`
		Body   json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(reqFile, &want); err != nil {
		u.t.Errorf("cassette %s: %v", name, err)
		return
	}
	if r.Method != want.Method || r.URL.Path != want.Path || !jsonEqual(body, want.Body) {
		u.t.Errorf("cassette %s: request differs\n got: %s %s %s\nwant: %s %s %s",
			name, r.Method, r.URL.Path, indent(body), want.Method, want.Path, indent(want.Body))
	}
	resFile, err := os.ReadFile(filepath.Join(dir, name+".res.http"))
	if err != nil {
		u.t.Errorf("cassette %s: %v", name, err)
		return
	}
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(resFile)), r)
	if err != nil {
		u.t.Errorf("cassette %s: %v", name, err)
		return
	}
	defer resp.Body.Close()
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func jsonEqual(a, b []byte) bool {
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && reflect.DeepEqual(x, y)
}

func indent(b []byte) string {
	var buf bytes.Buffer
	if json.Indent(&buf, b, "", "  ") != nil {
		return string(b)
	}
	return buf.String()
}

// logBuffer collects log output from concurrent goroutines.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logBuffer) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.Reset()
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// rig is the connector under test against a fake upstream.
type rig struct {
	t     *testing.T
	up    *upstream
	clk   *clocktest.Fake
	logs  *logBuffer
	tools map[string]tool.Tool
	caps  map[string]int // content cap per tool
}

type rigOption func(*rigConfig)

type rigConfig struct {
	client *http.Client
}

// withClient replaces the HTTP client (to serve hostile bodies directly).
func withClient(c *http.Client) rigOption { return func(rc *rigConfig) { rc.client = c } }

// newRig builds the connector with settings (JSON) against a new upstream.
func newRig(t *testing.T, settings string, opts ...rigOption) *rig {
	t.Helper()
	var rc rigConfig
	for _, o := range opts {
		o(&rc)
	}
	r := &rig{t: t, up: newUpstream(t), clk: clocktest.NewFake(time.Time{}), logs: &logBuffer{}}
	fopts := []parallel.Option{
		parallel.WithBaseURL(r.up.srv.URL),
		parallel.WithLookupEnv(lookup(map[string]string{"PARALLEL_API_KEY": canary})),
		parallel.WithClock(r.clk),
		parallel.WithLogger(slog.New(slog.NewJSONHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))),
	}
	if rc.client != nil {
		fopts = append(fopts, parallel.WithHTTPClient(rc.client))
	}
	tools, err := parallel.Factory(fopts...).Build(t.Context(), json.RawMessage(settings))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if n := len(r.up.requests()); n != 0 {
		t.Errorf("Build made %d requests, want none", n)
	}
	r.tools = map[string]tool.Tool{}
	for _, tl := range tools {
		r.tools[tl.Definition().Name] = tl
	}
	r.caps = contentCaps(t, settings)
	t.Cleanup(r.check)
	return r
}

// contentCaps reads max_content_chars from settings, with the defaults.
func contentCaps(t *testing.T, settings string) map[string]int {
	var s struct {
		MaxContentChars struct{ Search, Extract int } `json:"max_content_chars"`
	}
	if settings != "" {
		if err := json.Unmarshal([]byte(settings), &s); err != nil {
			t.Fatal(err)
		}
	}
	caps := map[string]int{"parallel_search": 4000, "parallel_extract": 20000}
	if s.MaxContentChars.Search > 0 {
		caps["parallel_search"] = s.MaxContentChars.Search
	}
	if s.MaxContentChars.Extract > 0 {
		caps["parallel_extract"] = s.MaxContentChars.Extract
	}
	return caps
}

func lookup(env map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	}
}

// callBound is how long a call may take in real time. Every wait in these
// tests is on the fake clock, so a call still running after it is waiting
// where it should not (a retry the rules forbid, or a wait that ignores ctx).
const callBound = 10 * time.Second

// call invokes a tool and checks the per-call invariants. It may run on a
// goroutine other than the test's, so it reports with Errorf only.
func (r *rig) call(ctx context.Context, name, input string) (json.RawMessage, error) {
	r.t.Helper()
	tl, ok := r.tools[name]
	if !ok {
		r.t.Errorf("no tool %q", name)
		return nil, errors.New("no such tool")
	}
	type result struct {
		out json.RawMessage
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := tl.Invoke(ctx, json.RawMessage(input))
		done <- result{out, err}
	}()
	var out json.RawMessage
	var err error
	select {
	case res := <-done:
		out, err = res.out, res.err
	case <-time.After(callBound):
		r.t.Errorf("%s did not return within %v of real time: it is waiting where it should not (%d requests so far)",
			name, callBound, len(r.up.requests()))
		return nil, errors.New("call did not return")
	}
	if err != nil {
		if strings.Contains(err.Error(), canary) {
			r.t.Errorf("%s: the key leaked into the error: %v", name, err)
		}
		return nil, err
	}
	checkOutput(r.t, name, out, r.caps[name])
	return out, nil
}

// checkOutput checks an output against E10's shape and the key.
func checkOutput(t *testing.T, name string, out []byte, maxChars int) {
	t.Helper()
	if bytes.Contains(out, []byte(canary)) {
		t.Errorf("%s: the key leaked into the output: %s", name, out)
	}
	var o struct {
		Results *[]struct {
			URL     *string `json:"url"`
			Title   *string `json:"title"`
			Content *string `json:"content"`
		} `json:"results"`
		Failed *[]struct{ URL, Error *string } `json:"failed"`
	}
	if err := json.Unmarshal(out, &o); err != nil || o.Results == nil {
		t.Errorf("%s: output %s is not the E10 shape (results array): %v", name, out, err)
		return
	}
	if name == "parallel_extract" && o.Failed == nil {
		t.Errorf("%s: output %s has no failed array", name, out)
	}
	for i, res := range *o.Results {
		if res.URL == nil || *res.URL == "" || res.Content == nil {
			t.Errorf("%s: result %d lacks a URL or content: %s", name, i, out)
			continue
		}
		if name == "parallel_search" && res.Title == nil {
			t.Errorf("%s: result %d lacks a title: %s", name, i, out)
		}
		c := *res.Content
		if loc := truncated.FindStringIndex(c); loc != nil {
			if n := utf8.RuneCountInString(c[:loc[0]]); n != maxChars {
				t.Errorf("%s: result %d was cut at %d characters, not at the cap %d", name, i, n, maxChars)
			}
		} else if n := utf8.RuneCountInString(c); n > maxChars {
			t.Errorf("%s: result %d content has %d characters, over the cap %d, without the marker", name, i, n, maxChars)
		}
	}
	if o.Failed != nil {
		for i, f := range *o.Failed {
			if f.URL == nil || *f.URL == "" || f.Error == nil || *f.Error == "" {
				t.Errorf("%s: failed %d lacks a URL or error: %s", name, i, out)
			}
		}
	}
}

// check runs the cross-call invariants after the test.
func (r *rig) check() {
	t := r.t
	if strings.Contains(r.logs.String(), canary) {
		t.Errorf("the key leaked into the log:\n%s", r.logs.String())
	}
	for i, req := range r.up.requests() {
		checkRequest(t, i, req)
	}
}

func checkRequest(t *testing.T, i int, req recorded) {
	t.Helper()
	if req.method != http.MethodPost || (req.path != "/v1/search" && req.path != "/v1/extract") {
		t.Errorf("request %d: %s %s, want POST /v1/search or /v1/extract", i, req.method, req.path)
		return
	}
	if got := req.header.Values("X-Api-Key"); len(got) != 1 || got[0] != canary {
		t.Errorf("request %d: x-api-key headers %q, want exactly the key once", i, got)
	}
	for k, vs := range req.header {
		if http.CanonicalHeaderKey(k) == "X-Api-Key" {
			continue
		}
		for _, v := range vs {
			if strings.Contains(v, canary) {
				t.Errorf("request %d: the key is in header %s", i, k)
			}
		}
	}
	if bytes.Contains(req.body, []byte(canary)) || strings.Contains(req.path, canary) {
		t.Errorf("request %d: the key is in the body or path", i)
	}
	if ct := req.header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("request %d: Content-Type %q", i, ct)
	}
	spec, err := openAPI()
	if err != nil {
		t.Errorf("load testdata/openapi.json: %v", err)
		return
	}
	if err := spec.CheckRequest(req.method, req.path, req.body); err != nil {
		t.Errorf("request %d: body does not fit the %s request schema: %v\n%s", i, req.path, err, req.body)
	}
}

// openAPI is Parallel's OpenAPI spec (the /v1/search and /v1/extract parts).
var openAPI = sync.OnceValues(func() (*toolsettest.OpenAPI, error) {
	return toolsettest.LoadOpenAPI(filepath.Join("testdata", "openapi.json"))
})

// golden compares out, indented, with testdata/golden/<name>.json.
func golden(t *testing.T, name string, out []byte) {
	t.Helper()
	testkit.Golden(t, filepath.Join("testdata", "golden", name+".json"), []byte(indent(out)+"\n"))
}

// isClass reports whether err is exactly one of the tool error classes.
func isClass(err, class error) bool {
	other := tool.ErrToolFailed
	if errors.Is(class, tool.ErrToolFailed) {
		other = tool.ErrInvalidInput
	}
	return errors.Is(err, class) && !errors.Is(err, other)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func jsonString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprint(err))
	}
	return string(b)
}

// countingReader is an endless body of spaces that counts the bytes read.
type countingReader struct{ n atomic.Int64 }

func (c *countingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = ' '
	}
	c.n.Add(int64(len(p)))
	return len(p), nil
}
