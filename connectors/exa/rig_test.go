package exa_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/connectors/exa"
	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// canaryKey is the API key every test uses. The invariants check that it
// reaches Exa in the x-api-key header and nowhere else.
const canaryKey = "exa-canary-key-5f0c1d9e2b7a4c3e"

// start is the fake clock's time.
var start = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

// keyEnv is the variable the rig's LookupEnv answers for.
const keyEnv = "EXA_API_KEY"

// response is one canned upstream response.
type response struct {
	status int
	header map[string]string
	body   []byte
}

func ok(body []byte) response { return response{status: http.StatusOK, body: body} }

// recorded is one request the fake upstream received.
type recorded struct {
	method, path, query string
	header              http.Header
	body                []byte
}

// upstream is a fake Exa. It answers with respond, or from a queue of
// responses (a request past the end of the queue fails the test).
type upstream struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	reqs    []recorded
	queue   []response
	respond func(recorded) response
}

func newUpstream(t *testing.T) *upstream {
	u := &upstream{t: t}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := readAll(r)
		if err != nil {
			t.Errorf("upstream: read request: %v", err)
		}
		rec := recorded{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, header: r.Header.Clone(), body: body}
		u.mu.Lock()
		u.reqs = append(u.reqs, rec)
		var resp response
		switch {
		case u.respond != nil:
			resp = u.respond(rec)
		case len(u.queue) > 0:
			resp, u.queue = u.queue[0], u.queue[1:]
		default:
			t.Errorf("upstream: unexpected request %d to %s", len(u.reqs), r.URL.Path)
			resp = response{status: http.StatusTeapot}
		}
		u.mu.Unlock()
		for k, v := range resp.header {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.status)
		_, _ = w.Write(resp.body)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func readAll(r *http.Request) ([]byte, error) {
	var buf bytes.Buffer
	_, err := buf.ReadFrom(r.Body)
	return buf.Bytes(), err
}

func (u *upstream) push(rs ...response) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.queue = append(u.queue, rs...)
}

func (u *upstream) requests() []recorded {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.reqs)
}

// rig builds the exa toolset against a fake upstream, with a fake clock and
// a debug log, and checks the invariants after every call and every test.
type rig struct {
	t     *testing.T
	up    *upstream
	clock *clocktest.Fake
	logs  *lockedBuffer
	tools map[string]tool.Tool
}

type rigOption struct {
	env      map[string]string // replaces the default {EXA_API_KEY: canaryKey}
	settings string
	client   *http.Client
}

// newRig builds the toolset with settings (JSON, may be empty).
func newRig(t *testing.T, settings string) *rig {
	return newRigWith(t, rigOption{settings: settings})
}

func newRigWith(t *testing.T, o rigOption) *rig {
	t.Helper()
	r := &rig{t: t, up: newUpstream(t), clock: clocktest.NewFake(start), logs: &lockedBuffer{}}
	tools, err := r.factory(o).Build(t.Context(), toolset.BuildInput{Settings: json.RawMessage(o.settings)})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if n := len(r.up.requests()); n != 0 {
		t.Fatalf("Build made %d requests, want none", n)
	}
	r.tools = map[string]tool.Tool{}
	for _, tl := range tools {
		r.tools[tl.Definition().Name] = tl
	}
	t.Cleanup(r.checkTest)
	return r
}

func (r *rig) factory(o rigOption) toolset.Factory {
	env := o.env
	if env == nil {
		env = map[string]string{keyEnv: canaryKey}
	}
	opts := []exa.Option{
		exa.WithBaseURL(r.up.srv.URL),
		exa.WithLookupEnv(func(k string) (string, bool) { v, ok := env[k]; return v, ok }),
		exa.WithClock(r.clock),
		exa.WithLogger(slog.New(slog.NewJSONHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))),
	}
	if o.client != nil {
		opts = append(opts, exa.WithHTTPClient(o.client))
	}
	return exa.Factory(opts...)
}

// call invokes a tool and checks the per-call invariants:
//   - the key appears in neither the output nor the error;
//   - an error is a tool.Error of exactly one kind, ErrInvalidInput or
//     ErrToolFailed, or the context's error;
//   - an output has the shared E10 shape for that tool, every string is
//     valid UTF-8 and every content is within its cap.
func (r *rig) call(ctx context.Context, name, input string) (json.RawMessage, error) {
	r.t.Helper()
	tl, found := r.tools[name]
	if !found {
		r.t.Fatalf("no tool %q", name)
	}
	out, err := tl.Invoke(ctx, json.RawMessage(input))
	checkNoKey(r.t, "output", string(out))
	if err != nil {
		checkNoKey(r.t, "error", err.Error())
		checkErrorKind(r.t, err)
		return out, err
	}
	checkShape(r.t, name, out, 0)
	return out, nil
}

// checkTest runs after each test: every request to Exa was a JSON POST to
// /search or /contents carrying the key exactly once, in x-api-key, never in
// Authorization or the body; and the log never held the key.
func (r *rig) checkTest() {
	for i, req := range r.up.requests() {
		if req.method != http.MethodPost || (req.path != "/search" && req.path != "/contents") {
			r.t.Errorf("request %d: %s %s, want POST /search or /contents", i, req.method, req.path)
		}
		if got := req.header.Values("X-Api-Key"); len(got) != 1 || got[0] != canaryKey {
			r.t.Errorf("request %d: x-api-key = %q, want the key exactly once", i, got)
		}
		if got := req.header.Values("Authorization"); len(got) != 0 {
			r.t.Errorf("request %d: Authorization header sent", i)
		}
		if bytes.Contains(req.body, []byte(canaryKey)) {
			r.t.Errorf("request %d: body contains the key", i)
		}
		if !json.Valid(req.body) {
			r.t.Errorf("request %d: body is not JSON: %q", i, req.body)
		}
	}
	checkNoKey(r.t, "log", r.logs.String())
}

func checkNoKey(t testing.TB, where, s string) {
	t.Helper()
	if strings.Contains(s, canaryKey) {
		t.Errorf("%s contains the API key: %s", where, s)
	}
}

func checkErrorKind(t testing.TB, err error) {
	t.Helper()
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	var te *tool.Error
	if !errors.As(err, &te) {
		t.Errorf("error %v is not a *tool.Error", err)
		return
	}
	if errors.Is(err, tool.ErrInvalidInput) == errors.Is(err, tool.ErrToolFailed) {
		t.Errorf("error %v: want exactly one of ErrInvalidInput and ErrToolFailed", err)
	}
}

// Shapes of E10, decoded strictly so an extra or misnamed field fails.
type (
	searchShape struct {
		Results []struct {
			Title         string `json:"title"`
			URL           string `json:"url"`
			PublishedDate string `json:"published_date,omitempty"`
			Author        string `json:"author,omitempty"`
			Content       string `json:"content"`
		} `json:"results"`
		Answer string `json:"answer,omitempty"`
	}
	extractShape struct {
		Results []struct {
			URL     string `json:"url"`
			Title   string `json:"title,omitempty"`
			Content string `json:"content"`
		} `json:"results"`
		Failed []struct {
			URL   string `json:"url"`
			Error string `json:"error"`
		} `json:"failed"`
	}
)

// checkShape checks out against E10's shape for the tool. contentCap, if not
// zero, is the cap each content must respect (the defaults otherwise).
func checkShape(t testing.TB, name string, out json.RawMessage, contentCap int) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.DisallowUnknownFields()
	var strs []string
	var contents []string
	switch name {
	case "exa_search":
		if contentCap == 0 {
			contentCap = 4000
		}
		var s searchShape
		if err := dec.Decode(&s); err != nil {
			t.Errorf("exa_search output %s: %v", out, err)
			return
		}
		if s.Results == nil {
			t.Errorf("exa_search output %s: results is null", out)
		}
		for _, r := range s.Results {
			if r.URL == "" {
				t.Errorf("exa_search output: result without a url: %s", out)
			}
			strs = append(strs, r.Title, r.URL, r.PublishedDate, r.Author)
			contents = append(contents, r.Content)
		}
		strs = append(strs, s.Answer)
	case "exa_extract":
		if contentCap == 0 {
			contentCap = 20000
		}
		var s extractShape
		if err := dec.Decode(&s); err != nil {
			t.Errorf("exa_extract output %s: %v", out, err)
			return
		}
		if s.Results == nil || s.Failed == nil {
			t.Errorf("exa_extract output %s: results or failed is null", out)
		}
		for _, r := range s.Results {
			if r.URL == "" {
				t.Errorf("exa_extract output: result without a url: %s", out)
			}
			strs = append(strs, r.URL, r.Title)
			contents = append(contents, r.Content)
		}
		for _, f := range s.Failed {
			if f.URL == "" || f.Error == "" {
				t.Errorf("exa_extract output: failed entry %+v without url or error", f)
			}
			strs = append(strs, f.URL, f.Error)
		}
	default:
		t.Fatalf("unknown tool %q", name)
	}
	for _, c := range contents {
		if n := utf8.RuneCountInString(contentMarkerRE.ReplaceAllString(c, "")); n > contentCap {
			t.Errorf("%s: content of %d characters is over the cap %d", name, n, contentCap)
		}
	}
	for _, s := range append(strs, contents...) {
		if !utf8.ValidString(s) {
			t.Errorf("%s: invalid UTF-8 in output: %q", name, s)
		}
	}
}

// contentMarkerRE is the marker a cut content ends with (E10: it says how
// much was cut and how to get more).
var contentMarkerRE = regexp.MustCompile(`…\[truncated [0-9,]+ characters; call exa_extract [^\]]+\]$`)

// golden compares JSON with a golden file, indented for review.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Indent(&buf, got, "", "  "); err != nil {
		t.Fatalf("golden %s: %v (%s)", name, err, got)
	}
	buf.WriteByte('\n')
	testkit.Golden(t, filepath.Join("testdata", "golden", name+".json"), buf.Bytes())
}

// lockedBuffer is a bytes.Buffer safe for the logger's concurrent writes.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
