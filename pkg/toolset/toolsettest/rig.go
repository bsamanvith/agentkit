package toolsettest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// watchdog bounds every call the suite makes, so a connector that hangs fails
// instead of stopping the test binary.
const watchdog = 10 * time.Second

// keyMode is what LookupEnv returns in a rig, and so what requests must carry.
type keyMode int

const (
	modeKeyed   keyMode = iota // the canary key: exactly one header carries it
	modeKeyless                // no key: KeylessMarker and no key
	modeNoKey                  // no key, and no request is expected to check
)

// rig is one subtest's world: an upstream serving the cassettes, a fake clock,
// a log sink and the Env wired to them. Its invariants are checked when the
// subtest ends, whatever the subtest was about.
type rig struct {
	t    testing.TB
	s    *suite
	up   *Upstream
	clk  *clocktest.Fake
	logs *logSink
	mode keyMode
	env  Env
}

func (s *suite) newRig(t testing.TB, mode keyMode) *rig {
	t.Helper()
	dir := s.c.Cassettes
	if s.fake {
		dir = ""
	}
	r := &rig{t: t, s: s, up: NewUpstream(t, dir), clk: clocktest.NewFake(time.Time{}), logs: &logSink{}, mode: mode}
	r.env = Env{
		BaseURL:    r.up.URL(),
		HTTPClient: r.up.Client(),
		LookupEnv:  r.lookupEnv,
		Clock:      r.clk,
		Logger:     slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	t.Cleanup(r.checkInvariants)
	return r
}

func (r *rig) lookupEnv(name string) (string, bool) {
	if r.mode == modeKeyed && r.s.c.KeyEnv != "" && name == r.s.c.KeyEnv {
		return r.s.canary, true
	}
	return "", false
}

// checkInvariants is what holds after every subtest.
func (r *rig) checkInvariants() {
	r.t.Helper()
	if log := r.logs.String(); strings.Contains(log, r.s.canary) {
		r.t.Errorf("[canary] the API key appears in a log line:\n%s", lineWith(log, r.s.canary))
	}
	for i, req := range r.up.Requests() {
		if err := r.checkRequest(req); err != nil {
			r.t.Errorf("[key] request %d (%s %s): %v", i+1, req.Method, req.Path, err)
		}
	}
}

// checkRequest checks where a request carries the key.
func (r *rig) checkRequest(req Request) error {
	if r.s.c.KeyEnv == "" || r.s.fake {
		return nil
	}
	canary := r.s.canary
	if strings.Contains(req.Path, canary) || strings.Contains(req.Query.Encode(), canary) {
		return errors.New("the key is in the URL; send it in one header only")
	}
	if bytes.Contains(req.Body, []byte(canary)) {
		return errors.New("the key is in the request body; send it in one header only")
	}
	var carriers []string
	for k, vs := range req.Header {
		for _, v := range vs {
			if strings.Contains(v, canary) {
				carriers = append(carriers, k)
			}
		}
	}
	switch r.mode {
	case modeKeyed:
		if len(carriers) != 1 {
			return fmt.Errorf("the key is in %d header values %v, want exactly one", len(carriers), carriers)
		}
	case modeKeyless:
		if len(carriers) != 0 {
			return fmt.Errorf("a keyless request carries the key in %v", carriers)
		}
		for k, vs := range r.s.c.KeylessMarker {
			if got := req.Header.Values(k); !slices.Equal(got, vs) {
				return fmt.Errorf("a keyless request has %s: %q, want %q", k, got, vs)
			}
		}
	}
	return nil
}

// build builds the connector's tools with settings.
func (r *rig) build(settings json.RawMessage) ([]tool.Tool, error) {
	r.t.Helper()
	f := r.s.c.New(r.env)
	ctx, cancel := context.WithTimeout(context.Background(), watchdog)
	defer cancel()
	tools, err := f.Build(ctx, settings)
	if err != nil && strings.Contains(err.Error(), r.s.canary) {
		r.t.Errorf("[canary] the API key appears in a Build error: %v", err)
	}
	return tools, err
}

// mustBuild builds with the connector's settings, failing the test on error.
func (r *rig) mustBuild() []tool.Tool {
	r.t.Helper()
	tools, err := r.build(r.s.c.Settings)
	if err != nil {
		r.t.Fatalf("Build(%s): %v", r.s.c.Settings, err)
	}
	return tools
}

// invoke calls a tool, bounded by the watchdog, and checks that neither the
// output nor the error carries the key.
func (r *rig) invoke(ctx context.Context, tools []tool.Tool, name string, input json.RawMessage) (json.RawMessage, error) {
	r.t.Helper()
	tl := find(tools, name)
	if tl == nil {
		r.t.Fatalf("no tool %q among %v", name, toolNames(tools))
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, watchdog)
		defer cancel()
	}
	out, err := tl.Invoke(ctx, input)
	if bytes.Contains(out, []byte(r.s.canary)) {
		r.t.Errorf("[canary] the API key appears in the output of %s", name)
	}
	if err != nil && strings.Contains(err.Error(), r.s.canary) {
		r.t.Errorf("[canary] the API key appears in the error of %s: %v", name, err)
	}
	return out, err
}

// checkOutput checks a happy call's output: valid against the tool's output
// schema, and equal to the golden file.
func (r *rig) checkOutput(tools []tool.Tool, call Call, out json.RawMessage) {
	r.t.Helper()
	if err := validate(find(tools, call.Tool).Definition().OutputSchema, out); err != nil {
		r.t.Errorf("[schema] output of %s does not match its output schema: %v\n%s", call.Name, err, out)
	}
	testkit.Golden(r.t, goldenPath(call), indent(r.t, out))
}

// sameAsGolden reports whether out equals the golden file as JSON, without
// rewriting it.
func sameAsGolden(call Call, out json.RawMessage) error {
	want, err := os.ReadFile(goldenPath(call))
	if err != nil {
		return fmt.Errorf("golden file: %w (run make golden)", err)
	}
	if !jsonEqual(want, out) {
		return fmt.Errorf("output differs from %s:\n%s", goldenPath(call), out)
	}
	return nil
}

func goldenPath(call Call) string { return filepath.Join("testdata", "golden", call.Name+".json") }

// cassettesRequested returns the names of the cassettes that answered, sorted.
func (r *rig) cassettesRequested() []string {
	var names []string
	for _, req := range r.up.Requests() {
		names = append(names, req.Cassette)
	}
	slices.Sort(names)
	return names
}

func sorted(s []string) []string {
	s = slices.Clone(s)
	slices.Sort(s)
	return s
}

func find(tools []tool.Tool, name string) tool.Tool {
	for _, tl := range tools {
		if tl.Definition().Name == name {
			return tl
		}
	}
	return nil
}

func toolNames(tools []tool.Tool) []string {
	var names []string
	for _, tl := range tools {
		names = append(names, tl.Definition().Name)
	}
	return names
}

// withSetting returns settings with key set to value.
func withSetting(settings json.RawMessage, key string, value any) json.RawMessage {
	m := map[string]json.RawMessage{}
	_ = json.Unmarshal(settings, &m)
	m = maps.Clone(m)
	m[key], _ = json.Marshal(value)
	b, _ := json.Marshal(m)
	return b
}

// schemaOf decodes and resolves a JSON Schema that must describe an object.
func schemaOf(raw json.RawMessage) (*jsonschema.Resolved, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("missing")
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	if s.Type != "object" {
		return nil, fmt.Errorf("type %q, want object", s.Type)
	}
	return s.Resolve(nil)
}

// validate checks a JSON value against a schema.
func validate(schema, value json.RawMessage) error {
	rs, err := schemaOf(schema)
	if err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	var v any
	if err := json.Unmarshal(value, &v); err != nil {
		return fmt.Errorf("not JSON: %w", err)
	}
	return rs.Validate(v)
}

func jsonEqual(a, b []byte) bool {
	var va, vb any
	if json.Unmarshal(a, &va) != nil || json.Unmarshal(b, &vb) != nil {
		return false
	}
	ja, _ := json.Marshal(va)
	jb, _ := json.Marshal(vb)
	return bytes.Equal(ja, jb)
}

func indent(tb testing.TB, b []byte) []byte {
	tb.Helper()
	var out bytes.Buffer
	if err := json.Indent(&out, b, "", "  "); err != nil {
		tb.Fatalf("output is not JSON: %v\n%s", err, b)
	}
	out.WriteByte('\n')
	return out.Bytes()
}

// logSink collects log output, safe for concurrent use.
type logSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logSink) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logSink) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func lineWith(text, needle string) string {
	for line := range strings.SplitSeq(text, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}
