package tavily_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
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
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/bsamanvith/agentkit/connectors/tavily"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// canary is the key the tests put in the environment. It must never appear
// in anything a tool returns or logs, nor in a request body.
const canary = "tvly-CANARY-5e1f0c9a7b3d"

const truncated = "…[truncated]"

var testStart = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

func settingsOr(s string) string {
	if s == "" {
		return "{}"
	}
	return s
}

// A cassette is one exchange recorded from the real API (T049's format):
// testdata/cassettes/<name>.req.json holds the request as the tool sent it,
// <name>.res.http the response as `curl -i` prints it. Responses with no
// exchange behind them (curl captures of requests the tool cannot send, a
// documentation example) are in testdata/responses/<name>.http.

// cassetteRequest is a cassette's <name>.req.json.
type cassetteRequest struct {
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Query  string          `json:"query,omitempty"`
	Body   json.RawMessage `json:"body"`
}

// cassette returns a cassette's request and response.
func cassette(t testing.TB, name string) (cassetteRequest, reply) {
	t.Helper()
	var req cassetteRequest
	b, err := os.ReadFile(filepath.Join("testdata", "cassettes", name+".req.json"))
	if err == nil {
		err = json.Unmarshal(b, &req)
	}
	if err != nil {
		t.Fatalf("cassette %s: %v", name, err)
	}
	return req, readResponse(t, filepath.Join("testdata", "cassettes", name+".res.http"))
}

// fixture is the body of a recorded response: a cassette's, or one in
// testdata/responses.
func fixture(t testing.TB, name string) []byte {
	t.Helper()
	file := filepath.Join("testdata", "cassettes", name+".res.http")
	if _, err := os.Stat(file); err != nil {
		file = filepath.Join("testdata", "responses", name+".http")
	}
	return readResponse(t, file).body
}

func readResponse(t testing.TB, file string) reply {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(b)), nil)
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	return reply{status: resp.StatusCode, header: resp.Header, body: body}
}

// reply is one canned upstream response.
type reply struct {
	status int
	header http.Header
	body   []byte
}

// request is one request the fake upstream received.
type request struct {
	method, path, query string
	header              http.Header
	body                []byte
}

// upstream is a fake Tavily: it records every request and answers each with
// the next queued reply (a test that queues none gets a failure).
type upstream struct {
	t       testing.TB
	srv     *httptest.Server
	mu      sync.Mutex
	reqs    []request
	replies []reply
}

func newUpstream(t testing.TB) *upstream {
	u := &upstream{t: t}
	u.srv = httptest.NewServer(http.HandlerFunc(u.serve))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *upstream) queue(rs ...reply) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.replies = append(u.replies, rs...)
}

func (u *upstream) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.reqs = append(u.reqs, request{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), body})
	var rp reply
	ok := len(u.replies) > 0
	if ok {
		rp, u.replies = u.replies[0], u.replies[1:]
	}
	u.mu.Unlock()
	if !ok {
		u.t.Errorf("unexpected upstream request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
		return
	}
	for k, vs := range rp.header {
		w.Header()[k] = vs
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(rp.status)
	_, _ = w.Write(rp.body)
}

func (u *upstream) requests() []request {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]request(nil), u.reqs...)
}

// syncBuffer is a log sink safe for concurrent writes.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// rig is one toolset built against the fake upstream.
type rig struct {
	t     testing.TB
	up    *upstream
	clock *clocktest.Fake
	logs  *syncBuffer
	reg   tool.Registry
	key   string // the key the toolset must send; "" for keyless

	schemas map[string]*jsonschema.Resolved // output schema of each tool

	mu   sync.Mutex
	seen []string // every output and error text, for the canary check
}

type rigOpts struct {
	settings string
	env      map[string]string // default: the canary in TAVILY_API_KEY
	extra    []tavily.Option
}

// newRig builds the toolset with opts and registers the invariants checked
// when the test ends:
//
//  1. every upstream request is a POST to /search or /extract with a JSON
//     body, and carries exactly one auth mode: the bearer key when the
//     toolset is keyed, else the keyless header;
//  2. no request body holds the key;
//  3. the canary appears in no output, error or log line (debug included).
//
// Every call made through rig.call also checks its output against the
// shared E10 shape.
func newRig(t testing.TB, o rigOpts) *rig {
	t.Helper()
	env := o.env
	if env == nil {
		env = map[string]string{"TAVILY_API_KEY": canary}
	}
	r := &rig{t: t, up: newUpstream(t), clock: clocktest.NewFake(testStart), logs: &syncBuffer{}}
	log := slog.New(slog.NewJSONHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	opts := append([]tavily.Option{
		tavily.WithBaseURL(r.up.srv.URL),
		tavily.WithHTTPClient(r.up.srv.Client()),
		tavily.WithLookupEnv(func(k string) (string, bool) { v, ok := env[k]; return v, ok }),
		tavily.WithClock(r.clock),
		tavily.WithLogger(log),
	}, o.extra...)
	tools, err := tavily.Factory(opts...).Build(t.Context(), toolset.BuildInput{Settings: json.RawMessage(settingsOr(o.settings))})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if r.reg, err = tool.NewRegistry(tools); err != nil {
		t.Fatal(err)
	}
	r.schemas = outputSchemas(t, tools)
	r.key = expectedKey(o.settings, env)
	t.Cleanup(r.check)
	return r
}

func (r *rig) check() {
	t := r.t
	for i, q := range r.up.requests() {
		if q.method != http.MethodPost || (q.path != "/search" && q.path != "/extract") || !json.Valid(q.body) {
			t.Errorf("request %d: %s %s %q, want a POST of JSON to /search or /extract", i, q.method, q.path, q.body)
		}
		authz, keyless := q.header.Values("Authorization"), q.header.Values("X-Tavily-Access-Mode")
		switch {
		case len(authz)+len(keyless) != 1:
			t.Errorf("request %d: Authorization %q and X-Tavily-Access-Mode %q: want exactly one auth mode", i, authz, keyless)
		case r.key != "" && (len(authz) != 1 || authz[0] != "Bearer "+r.key):
			t.Errorf("request %d: keyed toolset sent Authorization %q (want the bearer key), keyless %q", i, authz, keyless)
		case r.key == "" && (len(keyless) != 1 || keyless[0] != "keyless"):
			t.Errorf("request %d: keyless toolset sent X-Tavily-Access-Mode %q, Authorization %q", i, keyless, authz)
		}
		if bytes.Contains(q.body, []byte(canary)) {
			t.Errorf("request %d: the key is in the request body", i)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range append(r.seen, r.logs.String()) {
		if strings.Contains(s, canary) {
			t.Errorf("the key leaked: %q", s)
		}
	}
}

// expectedKey restates the auth rule independently of the code: the key
// in the named variable unless auth is keyless; none if it is unset or blank.
func expectedKey(settings string, env map[string]string) string {
	var s struct {
		Auth      string `json:"auth"`
		APIKeyEnv string `json:"api_key_env"`
	}
	_ = json.Unmarshal([]byte(settingsOr(settings)), &s)
	if s.Auth == "keyless" {
		return ""
	}
	if s.APIKeyEnv == "" {
		s.APIKeyEnv = "TAVILY_API_KEY"
	}
	return strings.TrimSpace(env[s.APIKeyEnv])
}

// call invokes a tool and checks its output's shape. It has no deadline of
// its own: a deadline would change what the connector decides (whether a
// retry fits), and could fail a correct call on a loaded machine. Calls that
// may wait on the fake clock go through callAsync.
func (r *rig) call(name, input string) (json.RawMessage, error) {
	r.t.Helper()
	out, err := r.reg.Invoke(r.t.Context(), name, json.RawMessage(input))
	r.mu.Lock()
	r.seen = append(r.seen, string(out))
	if err != nil {
		r.seen = append(r.seen, err.Error())
	}
	r.mu.Unlock()
	if err == nil {
		if serr := checkShape(name, out); serr != nil {
			r.t.Errorf("%s output %s: %v", name, out, serr)
		}
		if serr := validOutput(r.schemas, name, out); serr != nil {
			r.t.Errorf("%s output does not validate against its output schema: %v", name, serr)
		}
	}
	return out, err
}

// The E10 output shapes, decoded strictly: any other field is a bug.
type (
	searchShape struct {
		Results *[]struct {
			Title         *string  `json:"title"`
			URL           *string  `json:"url"`
			PublishedDate string   `json:"published_date"`
			Author        string   `json:"author"`
			Score         *float64 `json:"score"`
			Content       *string  `json:"content"`
		} `json:"results"`
		Answer string `json:"answer"`
	}
	extractShape struct {
		Results *[]struct {
			URL     *string `json:"url"`
			Title   string  `json:"title"`
			Content *string `json:"content"`
		} `json:"results"`
		Failed *[]struct {
			URL   *string `json:"url"`
			Error *string `json:"error"`
		} `json:"failed"`
	}
)

// checkShape reports whether out is the E10 shape for the tool: the
// required fields present, nothing else, valid UTF-8.
func checkShape(name string, out []byte) error {
	if !utf8.Valid(out) {
		return errors.New("not valid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.DisallowUnknownFields()
	switch name {
	case "tavily_search":
		var s searchShape
		if err := dec.Decode(&s); err != nil {
			return err
		}
		if s.Results == nil {
			return errors.New("results missing or null")
		}
		for i, r := range *s.Results {
			if r.Title == nil || r.URL == nil || r.Content == nil {
				return fmt.Errorf("result %d lacks title, url or content", i)
			}
		}
	case "tavily_extract":
		var s extractShape
		if err := dec.Decode(&s); err != nil {
			return err
		}
		if s.Results == nil || s.Failed == nil {
			return errors.New("results or failed missing or null")
		}
		for i, r := range *s.Results {
			if r.URL == nil || r.Content == nil {
				return fmt.Errorf("result %d lacks url or content", i)
			}
		}
		for i, f := range *s.Failed {
			if f.URL == nil || f.Error == nil {
				return fmt.Errorf("failure %d lacks url or error", i)
			}
		}
	default:
		return fmt.Errorf("unknown tool %q", name)
	}
	return nil
}

// isInvalidInput reports an error the model can fix: ErrInvalidInput and
// not ErrToolFailed.
func isInvalidInput(err error) bool {
	return errors.Is(err, tool.ErrInvalidInput) && !errors.Is(err, tool.ErrToolFailed)
}

// isToolFailed reports a failure: ErrToolFailed and not ErrInvalidInput.
func isToolFailed(err error) bool {
	return errors.Is(err, tool.ErrToolFailed) && !errors.Is(err, tool.ErrInvalidInput)
}

// outputSchemas resolves each tool's declared output schema.
func outputSchemas(t testing.TB, tools []tool.Tool) map[string]*jsonschema.Resolved {
	t.Helper()
	m := map[string]*jsonschema.Resolved{}
	for _, tl := range tools {
		d := tl.Definition()
		var s jsonschema.Schema
		if err := json.Unmarshal(d.OutputSchema, &s); err != nil {
			t.Fatalf("%s output schema: %v", d.Name, err)
		}
		resolved, err := s.Resolve(nil)
		if err != nil {
			t.Fatalf("%s output schema: %v", d.Name, err)
		}
		m[d.Name] = resolved
	}
	return m
}

// validOutput checks a tool's output against its output schema.
func validOutput(schemas map[string]*jsonschema.Resolved, name string, out []byte) error {
	var v any
	if err := json.Unmarshal(out, &v); err != nil {
		return err
	}
	return schemas[name].Validate(v)
}
