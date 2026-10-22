package toolsettest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// DummyKey is the key [Definitions] hands a factory for any variable.
const DummyKey = "toolsettest-dummy-key"

// Definitions returns a connector's real tool definitions, sorted by name.
// newFactory is wired to a dummy key for every variable, a client that refuses
// every host and a fake clock, so building costs nothing; a request fails the
// test. Pass the same constructor the connector's own test gives
// [Connector.New].
func Definitions(tb testing.TB, newFactory func(Env) toolset.Factory, settings json.RawMessage) []tool.Definition {
	tb.Helper()
	h := newHermetic(tb, newToken()) // allows no host
	defer h.close()
	f := newFactory(Env{
		HTTPClient: &http.Client{Transport: h},
		LookupEnv:  func(string) (string, bool) { return DummyKey, true },
		Clock:      clocktest.NewFake(time.Time{}),
		Logger:     slog.New(slog.DiscardHandler),
	})
	ctx, cancel := context.WithTimeout(context.Background(), watchdog)
	defer cancel()
	tools, err := f.Build(ctx, toolset.BuildInput{Settings: settings})
	if err != nil {
		tb.Fatalf("toolsettest: Definitions: Build: %v", err)
	}
	defs := make([]tool.Definition, len(tools))
	for i, tl := range tools {
		defs[i] = tl.Definition()
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs
}

// Script says how a [Fake] answers calls of one tool. Build one with
// [Output], [OutputFunc], [Error], [Golden] or [Sequence].
type Script struct {
	tool  string
	steps []func(ctx context.Context, input json.RawMessage) (json.RawMessage, error)
	once  bool // a Sequence: each step answers one call
	err   error
}

// Output answers every call of toolName with output: a json.RawMessage as is,
// anything else marshalled to JSON.
func Output(toolName string, output any) Script {
	b, err := marshalOutput(output)
	return Script{tool: toolName, err: err, steps: []func(context.Context, json.RawMessage) (json.RawMessage, error){
		func(context.Context, json.RawMessage) (json.RawMessage, error) { return slices.Clone(b), nil },
	}}
}

// OutputFunc answers every call of toolName with fn's result. fn gets input
// already validated against the tool's real input schema; its output is
// validated against the real output schema. An error is returned to the
// caller as is (classify it with tool.ErrInvalidInput or tool.ErrToolFailed).
func OutputFunc(toolName string, fn func(ctx context.Context, input json.RawMessage) (any, error)) Script {
	return Script{tool: toolName, steps: []func(context.Context, json.RawMessage) (json.RawMessage, error){
		func(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
			v, err := fn(ctx, input)
			if err != nil {
				return nil, err
			}
			return marshalOutput(v)
		},
	}}
}

// ErrScripted is the cause of an error a [Fake] returns from an [Error] script.
var ErrScripted = errors.New("toolsettest: scripted failure")

// Error answers every call of toolName with an error of class kind:
// tool.ErrInvalidInput or tool.ErrToolFailed (as a *tool.Error whose cause is
// [ErrScripted]), or a context error, returned as is.
func Error(toolName string, kind error) Script {
	return Script{tool: toolName, steps: []func(context.Context, json.RawMessage) (json.RawMessage, error){
		func(context.Context, json.RawMessage) (json.RawMessage, error) {
			if errors.Is(kind, context.Canceled) || errors.Is(kind, context.DeadlineExceeded) {
				return nil, kind
			}
			if !errors.Is(kind, tool.ErrInvalidInput) {
				return nil, &tool.Error{Tool: toolName, Kind: tool.ErrToolFailed, Err: errors.Join(kind, ErrScripted)}
			}
			return nil, &tool.Error{Tool: toolName, Kind: kind, Err: ErrScripted}
		},
	}}
}

// Golden answers every call of toolName with the JSON in the file at path,
// typically a connector's golden output (testdata/golden/<call>.json), so the
// fake replays what the real connector returned.
func Golden(toolName, path string) Script {
	b, err := os.ReadFile(path)
	var compact bytes.Buffer
	if err == nil {
		if cerr := json.Compact(&compact, b); cerr != nil {
			err = fmt.Errorf("%s is not JSON: %w", path, cerr)
		}
	}
	s := Output(toolName, json.RawMessage(compact.Bytes()))
	s.err = err
	return s
}

// Sequence answers successive calls of one tool with each step in turn, each
// once. A call after the last fails the test. Every step must be for the
// same tool, and none may itself be a Sequence.
func Sequence(steps ...Script) Script {
	s := Script{once: true}
	for i, st := range steps {
		switch {
		case st.err != nil:
			s.err = st.err
		case st.once:
			s.err = errors.New("a Sequence cannot contain a Sequence")
		case i > 0 && st.tool != s.tool:
			s.err = fmt.Errorf("a Sequence mixes tools %q and %q", s.tool, st.tool)
		}
		s.tool = st.tool
		s.steps = append(s.steps, st.steps...)
	}
	if len(steps) == 0 {
		s.err = errors.New("an empty Sequence")
	}
	return s
}

func marshalOutput(v any) (json.RawMessage, error) {
	if raw, ok := v.(json.RawMessage); ok {
		if !json.Valid(raw) {
			return nil, fmt.Errorf("output is not JSON: %s", raw)
		}
		return raw, nil
	}
	return json.Marshal(v)
}

// FakeCall is one call a [Fake] received.
type FakeCall struct {
	Tool   string
	Input  json.RawMessage
	Output json.RawMessage
	Err    error
}

// Fake is a tool.Registry with exactly a connector's real definitions, for
// tests of code that uses a connector (the agent, the API, flows, the
// binary). It stays honest the way the fake model does:
//
//   - every call's input is validated against the real input schema; invalid
//     input gives tool.ErrInvalidInput, as the real connector would;
//   - every scripted output is validated against the real output schema; one
//     that does not match fails the test, so a fake cannot return a shape the
//     real connector never would;
//   - a call with no script fails the test: there are no silent defaults.
//
// It is safe for concurrent use.
type Fake struct {
	tb      testing.TB
	defs    []tool.Definition
	inputs  map[string]*jsonschema.Resolved
	outputs map[string]*jsonschema.Resolved
	scripts map[string]*scriptState

	mu    sync.Mutex
	calls []FakeCall
}

type scriptState struct {
	Script
	next int
}

// NewFake returns a fake with defs (from [Definitions]) answering by scripts.
// A script for a tool not in defs, two scripts for one tool, or a malformed
// script fails the test at once.
func NewFake(tb testing.TB, defs []tool.Definition, scripts ...Script) *Fake {
	tb.Helper()
	f := &Fake{
		tb: tb, inputs: map[string]*jsonschema.Resolved{}, outputs: map[string]*jsonschema.Resolved{},
		scripts: map[string]*scriptState{},
	}
	for _, d := range defs {
		d = d.Clone()
		in, err := schemaOf(d.InputSchema)
		if err != nil {
			tb.Fatalf("toolsettest: NewFake: %s: input schema: %v", d.Name, err)
		}
		f.inputs[d.Name] = in
		if len(d.OutputSchema) > 0 {
			out, err := schemaOf(d.OutputSchema)
			if err != nil {
				tb.Fatalf("toolsettest: NewFake: %s: output schema: %v", d.Name, err)
			}
			f.outputs[d.Name] = out
		}
		f.defs = append(f.defs, d)
	}
	sort.Slice(f.defs, func(i, j int) bool { return f.defs[i].Name < f.defs[j].Name })
	for _, s := range scripts {
		switch {
		case s.err != nil:
			tb.Fatalf("toolsettest: NewFake: script for %s: %v", s.tool, s.err)
		case f.inputs[s.tool] == nil:
			tb.Fatalf("toolsettest: NewFake: script for %q, which is not among the definitions", s.tool)
		case f.scripts[s.tool] != nil:
			tb.Fatalf("toolsettest: NewFake: two scripts for %s: use Sequence", s.tool)
		}
		f.scripts[s.tool] = &scriptState{Script: s}
	}
	return f
}

// List implements tool.Registry.
func (f *Fake) List(context.Context) ([]tool.Definition, error) {
	out := make([]tool.Definition, len(f.defs))
	for i, d := range f.defs {
		out[i] = d.Clone()
	}
	return out, nil
}

// Invoke implements tool.Registry.
func (f *Fake) Invoke(ctx context.Context, name string, input json.RawMessage) (json.RawMessage, error) {
	input = slices.Clone(input)
	out, err := f.invoke(ctx, name, input)
	f.mu.Lock()
	f.calls = append(f.calls, FakeCall{Tool: name, Input: input, Output: slices.Clone(out), Err: err})
	f.mu.Unlock()
	return out, err
}

func (f *Fake) invoke(ctx context.Context, name string, input json.RawMessage) (json.RawMessage, error) {
	in := f.inputs[name]
	if in == nil {
		return nil, fmt.Errorf("%w: %q", tool.ErrNotFound, name)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(input)) == 0 {
		input = json.RawMessage(`{}`)
	}
	var v any
	if err := json.Unmarshal(input, &v); err != nil {
		return nil, &tool.Error{Tool: name, Kind: tool.ErrInvalidInput, Err: fmt.Errorf("parse input: %w", err)}
	}
	if err := in.Validate(v); err != nil {
		return nil, &tool.Error{Tool: name, Kind: tool.ErrInvalidInput, Err: err}
	}
	step := f.step(name)
	if step == nil {
		return nil, &tool.Error{Tool: name, Kind: tool.ErrToolFailed, Err: errors.New("toolsettest: no script")}
	}
	out, err := step(ctx, input)
	if err != nil {
		return nil, err
	}
	if rs := f.outputs[name]; rs != nil {
		var ov any
		if jerr := json.Unmarshal(out, &ov); jerr != nil {
			err = jerr
		} else {
			err = rs.Validate(ov)
		}
		if err != nil {
			f.tb.Errorf("toolsettest: the script for %s returned output the real output schema rejects: %v\n%s", name, err, out)
			return nil, &tool.Error{Tool: name, Kind: tool.ErrToolFailed, Err: fmt.Errorf("toolsettest: scripted output does not match the output schema: %w", err)}
		}
	}
	return out, nil
}

// step returns the script step for the next call of name, or nil (failing
// the test) if there is none.
func (f *Fake) step(name string) func(context.Context, json.RawMessage) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.scripts[name]
	switch {
	case s == nil:
		f.tb.Errorf("toolsettest: unscripted call of %s: give the fake a script for it", name)
		return nil
	case !s.once:
		return s.steps[0]
	case s.next >= len(s.steps):
		f.tb.Errorf("toolsettest: call %d of %s, but its Sequence has %d step(s)", s.next+1, name, len(s.steps))
		return nil
	default:
		s.next++
		return s.steps[s.next-1]
	}
}

// Calls returns the calls received so far, oldest first, including invalid
// and failed ones.
func (f *Fake) Calls() []FakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]FakeCall, len(f.calls))
	for i, c := range f.calls {
		c.Input, c.Output = slices.Clone(c.Input), slices.Clone(c.Output)
		out[i] = c
	}
	return out
}

// Tools returns the fake's tools, each calling through the fake.
func (f *Fake) Tools() []tool.Tool {
	out := make([]tool.Tool, len(f.defs))
	for i, d := range f.defs {
		out[i] = fakeTool{f: f, def: d}
	}
	return out
}

type fakeTool struct {
	f   *Fake
	def tool.Definition
}

func (t fakeTool) Definition() tool.Definition { return t.def.Clone() }

func (t fakeTool) Invoke(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	return t.f.Invoke(ctx, t.def.Name, input)
}

// FakeToolset is a toolset.Factory serving a [Fake] under a real connector's
// type, to substitute for it in a toolset.Catalog. Build it with
// [FakeFactory].
type FakeToolset struct {
	*Fake
	typ string
}

// FakeFactory returns a factory of type typ whose tools are a [Fake] with defs
// and scripts. Config-driven tests (cmd/agentkit, the agent API) put it in
// their catalog in place of the real connector, so they run with no network.
// Build accepts any settings object (the fake has no account to configure)
// and every instance shares the one Fake, whose Calls records them all.
func FakeFactory(tb testing.TB, typ string, defs []tool.Definition, scripts ...Script) *FakeToolset {
	tb.Helper()
	return &FakeToolset{Fake: NewFake(tb, defs, scripts...), typ: typ}
}

// Type implements toolset.Factory.
func (f *FakeToolset) Type() string { return f.typ }

// Build implements toolset.Factory.
func (f *FakeToolset) Build(_ context.Context, in toolset.BuildInput) ([]tool.Tool, error) {
	settings := in.Settings
	var ignored map[string]json.RawMessage
	if err := toolset.DecodeSettings(settings, &ignored); err != nil {
		return nil, err
	}
	return f.Tools(), nil
}
