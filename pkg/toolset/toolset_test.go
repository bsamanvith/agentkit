package toolset_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/tool/tooltest"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// fixtureFactory serves the tooltest fixtures (echo, fail, block) under a type
// of its own, so tests can tell it from the demo factory.
type fixtureFactory struct{ typ string }

func (f fixtureFactory) Type() string { return f.typ }
func (f fixtureFactory) Build(_ context.Context, settings json.RawMessage) ([]tool.Tool, error) {
	var s struct {
		Fail bool `json:"fail"`
	}
	if err := toolset.DecodeSettings(settings, &s); err != nil {
		return nil, err
	}
	if s.Fail {
		return nil, errBoom
	}
	return tooltest.Fixtures(), nil
}

var errBoom = errors.New("boom")

func newCatalog(t *testing.T, fs ...toolset.Factory) *toolset.Catalog {
	t.Helper()
	c, err := toolset.NewCatalog(fs...)
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	return c
}

func fixedClock() func() time.Time {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return func() time.Time { return at }
}

func demoCatalog(t *testing.T) *toolset.Catalog {
	return newCatalog(t, toolset.Demo(toolset.DemoClock(fixedClock())), fixtureFactory{"fixtures"})
}

func names(t *testing.T, reg tool.Registry) []string {
	t.Helper()
	defs, err := reg.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var out []string
	for _, d := range defs {
		out = append(out, d.Name)
	}
	return out
}

func call(t *testing.T, reg tool.Registry, name, input string) string {
	t.Helper()
	out, err := reg.Invoke(t.Context(), name, json.RawMessage(input))
	if err != nil {
		t.Fatalf("Invoke %s: %v", name, err)
	}
	return string(out)
}

func TestBuild(t *testing.T) {
	t.Parallel()
	regs, problems, err := toolset.Build(t.Context(), demoCatalog(t), []toolset.Spec{
		{Name: "demo", Type: "demo"},
		{Name: "fx", Type: "fixtures"},
	})
	if err != nil || len(problems) != 0 {
		t.Fatalf("Build: problems=%v err=%v", problems, err)
	}
	if got, want := len(regs), 2; got != want {
		t.Fatalf("got %d registries, want %d", got, want)
	}
	if got, want := names(t, regs["demo"]), []string{"calculate", "current_time", "echo"}; !slices.Equal(got, want) {
		t.Errorf("demo tools = %v, want %v", got, want)
	}
	if got, want := names(t, regs["fx"]), []string{"block", "echo", "fail"}; !slices.Equal(got, want) {
		t.Errorf("fx tools = %v, want %v", got, want)
	}
}

func TestBuildTwoInstances(t *testing.T) {
	t.Parallel()
	regs, _, err := toolset.Build(t.Context(), demoCatalog(t), []toolset.Spec{
		{Name: "a", Type: "demo", Settings: json.RawMessage(`{"echo_prefix":"A:"}`)},
		{Name: "b", Type: "demo", Settings: json.RawMessage(`{"echo_prefix":"B:","utc_offset_minutes":330}`)},
		{Name: "c", Type: "demo"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for name, want := range map[string]string{"a": `{"text":"A:hi"}`, "b": `{"text":"B:hi"}`, "c": `{"text":"hi"}`} {
		if got := call(t, regs[name], "echo", `{"text":"hi"}`); got != want {
			t.Errorf("%s echo = %s, want %s", name, got, want)
		}
	}
	if got, want := call(t, regs["a"], "current_time", `{}`), `{"time":"2026-01-02T03:04:05Z"}`; got != want {
		t.Errorf("a current_time = %s, want %s", got, want)
	}
	if got, want := call(t, regs["b"], "current_time", `{}`), `{"time":"2026-01-02T08:34:05+05:30"}`; got != want {
		t.Errorf("b current_time = %s, want %s", got, want)
	}
}

func TestBuildRejects(t *testing.T) {
	t.Parallel()
	cat := demoCatalog(t)
	tests := []struct {
		name  string
		specs []toolset.Spec
		want  error
	}{
		{"duplicate name", []toolset.Spec{{Name: "x", Type: "demo"}, {Name: "x", Type: "fixtures"}}, toolset.ErrDuplicateName},
		{"unknown type", []toolset.Spec{{Name: "x", Type: "salesforce"}}, toolset.ErrUnknownType},
		{"empty name", []toolset.Spec{{Name: "", Type: "demo"}}, toolset.ErrInvalidName},
		{"space in name", []toolset.Spec{{Name: "a b", Type: "demo"}}, toolset.ErrInvalidName},
		{"slash in name", []toolset.Spec{{Name: "a/b", Type: "demo"}}, toolset.ErrInvalidName},
		{"dot-dot name", []toolset.Spec{{Name: "..", Type: "demo"}}, toolset.ErrInvalidName},
		{"name too long", []toolset.Spec{{Name: string(make([]byte, 65)), Type: "demo"}}, toolset.ErrInvalidName},
		{"unknown settings field", []toolset.Spec{{Name: "x", Type: "demo", Settings: json.RawMessage(`{"echo_prefx":"a"}`)}}, toolset.ErrInvalidSettings},
		{"settings of the wrong type", []toolset.Spec{{Name: "x", Type: "demo", Settings: json.RawMessage(`{"echo_prefix":5}`)}}, toolset.ErrInvalidSettings},
		{"settings not an object", []toolset.Spec{{Name: "x", Type: "demo", Settings: json.RawMessage(`[1]`)}}, toolset.ErrInvalidSettings},
		{"settings out of range", []toolset.Spec{{Name: "x", Type: "demo", Settings: json.RawMessage(`{"utc_offset_minutes":9999}`)}}, toolset.ErrInvalidSettings},
		{"malformed glob", []toolset.Spec{{Name: "x", Type: "demo", Include: []string{"[a"}}}, toolset.ErrInvalidFilter},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			regs, _, err := toolset.Build(t.Context(), cat, tc.specs)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if regs != nil {
				t.Errorf("registries returned alongside an error: %v", regs)
			}
		})
	}

	t.Run("unknown type lists the compiled-in types", func(t *testing.T) {
		t.Parallel()
		_, _, err := toolset.Build(t.Context(), cat, []toolset.Spec{{Name: "x", Type: "salesforce"}})
		var ute *toolset.UnknownTypeError
		if !errors.As(err, &ute) {
			t.Fatalf("err = %v, want *UnknownTypeError", err)
		}
		if want := []string{"demo", "fixtures"}; !slices.Equal(ute.Available, want) || ute.Type != "salesforce" {
			t.Errorf("type=%q available=%v, want salesforce and %v", ute.Type, ute.Available, want)
		}
	})

	t.Run("spec mistakes fail even when optional", func(t *testing.T) {
		t.Parallel()
		_, _, err := toolset.Build(t.Context(), cat, []toolset.Spec{{Name: "x", Type: "salesforce", Optional: true}})
		if !errors.Is(err, toolset.ErrUnknownType) {
			t.Errorf("err = %v, want ErrUnknownType", err)
		}
	})

	t.Run("a bad later spec runs no factory", func(t *testing.T) {
		t.Parallel()
		var calls countingFactory
		c := newCatalog(t, &calls)
		_, _, err := toolset.Build(t.Context(), c, []toolset.Spec{{Name: "ok", Type: "counting"}, {Name: "bad", Type: "nope"}})
		if !errors.Is(err, toolset.ErrUnknownType) {
			t.Fatalf("err = %v, want ErrUnknownType", err)
		}
		if n := calls.count(); n != 0 {
			t.Errorf("factory ran %d times before validation finished", n)
		}
	})
}

type countingFactory struct {
	mu sync.Mutex
	n  int
}

func (f *countingFactory) Type() string { return "counting" }
func (f *countingFactory) Build(context.Context, json.RawMessage) ([]tool.Tool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	return tooltest.Fixtures(), nil
}

func (f *countingFactory) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

func TestNewCatalogRejects(t *testing.T) {
	t.Parallel()
	if _, err := toolset.NewCatalog(toolset.Demo(), toolset.Demo()); !errors.Is(err, toolset.ErrDuplicateType) {
		t.Errorf("duplicate type: err = %v, want ErrDuplicateType", err)
	}
	if _, err := toolset.NewCatalog(nil); !errors.Is(err, toolset.ErrInvalidFactory) {
		t.Errorf("nil factory: err = %v, want ErrInvalidFactory", err)
	}
	if _, err := toolset.NewCatalog(fixtureFactory{"bad type"}); !errors.Is(err, toolset.ErrInvalidFactory) {
		t.Errorf("bad type: err = %v, want ErrInvalidFactory", err)
	}
}

func TestFilters(t *testing.T) {
	t.Parallel()
	cat := demoCatalog(t)
	ok := []struct {
		name            string
		include, exclue []string
		want            []string
	}{
		{"include exact", []string{"echo"}, nil, []string{"echo"}},
		{"include glob", []string{"c*"}, nil, []string{"calculate", "current_time"}},
		{"include several", []string{"echo", "calculate"}, nil, []string{"calculate", "echo"}},
		{"exclude exact", nil, []string{"echo"}, []string{"calculate", "current_time"}},
		{"exclude glob", nil, []string{"c*"}, []string{"echo"}},
		{"include then exclude", []string{"c*"}, []string{"calculate"}, []string{"current_time"}},
	}
	for _, tc := range ok {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			regs, _, err := toolset.Build(t.Context(), cat, []toolset.Spec{{Name: "x", Type: "demo", Include: tc.include, Exclude: tc.exclue}})
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if got := names(t, regs["x"]); !slices.Equal(got, tc.want) {
				t.Errorf("tools = %v, want %v", got, tc.want)
			}
			// A filtered-out tool is not callable either.
			for _, n := range []string{"echo", "calculate", "current_time"} {
				if slices.Contains(tc.want, n) {
					continue
				}
				if _, err := regs["x"].Invoke(t.Context(), n, json.RawMessage(`{}`)); !errors.Is(err, tool.ErrNotFound) {
					t.Errorf("Invoke(%s) err = %v, want ErrNotFound", n, err)
				}
			}
		})
	}

	bad := []struct {
		name             string
		include, exclude []string
	}{
		{"include matches nothing", []string{"nope*"}, nil},
		{"one of several includes matches nothing", []string{"echo", "nope"}, nil},
		{"exclude matches nothing", nil, []string{"nope"}},
		{"everything excluded", nil, []string{"*"}},
		{"include and exclude cancel out", []string{"echo"}, []string{"echo"}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := toolset.Build(t.Context(), cat, []toolset.Spec{{Name: "x", Type: "demo", Include: tc.include, Exclude: tc.exclude}})
			if !errors.Is(err, toolset.ErrInvalidFilter) {
				t.Errorf("err = %v, want ErrInvalidFilter", err)
			}
		})
	}
}

func TestOptional(t *testing.T) {
	t.Parallel()
	cat := demoCatalog(t)
	failing := json.RawMessage(`{"fail":true}`)

	t.Run("optional failure is skipped and reported", func(t *testing.T) {
		t.Parallel()
		regs, problems, err := toolset.Build(t.Context(), cat, []toolset.Spec{
			{Name: "good", Type: "demo"},
			{Name: "flaky", Type: "fixtures", Settings: failing, Optional: true},
		})
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if _, ok := regs["flaky"]; ok || regs["good"] == nil {
			t.Errorf("registries = %v, want only good", regs)
		}
		if len(problems) != 1 || problems[0].Toolset != "flaky" || problems[0].Type != "fixtures" || !errors.Is(problems[0].Err, errBoom) {
			t.Errorf("problems = %v, want one for flaky wrapping the factory error", problems)
		}
	})

	t.Run("optional with invalid settings and with an unmatched filter", func(t *testing.T) {
		t.Parallel()
		regs, problems, err := toolset.Build(t.Context(), cat, []toolset.Spec{
			{Name: "badsettings", Type: "demo", Settings: json.RawMessage(`{"x":1}`), Optional: true},
			{Name: "badfilter", Type: "demo", Include: []string{"nope"}, Optional: true},
		})
		if err != nil || len(regs) != 0 || len(problems) != 2 {
			t.Fatalf("regs=%v problems=%v err=%v, want no regs and two problems", regs, problems, err)
		}
		if !errors.Is(problems[0].Err, toolset.ErrInvalidSettings) || !errors.Is(problems[1].Err, toolset.ErrInvalidFilter) {
			t.Errorf("problems = %v", problems)
		}
	})

	t.Run("required failure fails the build", func(t *testing.T) {
		t.Parallel()
		regs, _, err := toolset.Build(t.Context(), cat, []toolset.Spec{
			{Name: "good", Type: "demo"},
			{Name: "needed", Type: "fixtures", Settings: failing},
		})
		var te *toolset.Error
		if !errors.Is(err, errBoom) || !errors.As(err, &te) || te.Toolset != "needed" {
			t.Fatalf("err = %v, want *Error for needed wrapping the factory error", err)
		}
		if regs != nil {
			t.Errorf("registries returned alongside an error: %v", regs)
		}
	})

	t.Run("cancelled context fails even for optional", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, _, err := toolset.Build(ctx, cat, []toolset.Spec{{Name: "x", Type: "demo", Optional: true}})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})
}

func TestBuildConformance(t *testing.T) {
	t.Parallel()
	// Registries made by Build pass the same suite as any other Registry.
	tooltest.RunRegistry(t, func(t *testing.T, _ []tool.Tool) tool.Registry {
		regs, _, err := toolset.Build(t.Context(), newCatalog(t, fixtureFactory{"fixtures"}),
			[]toolset.Spec{{Name: "fx", Type: "fixtures"}})
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		return regs["fx"]
	})
}

// TestDemoConformance checks the demo factory through Build. RunRegistry
// expects the echo/fail/block fixtures, which demo does not have, so this is
// demo-specific; TestBuildConformance covers Build's registries with the suite.
func TestDemoConformance(t *testing.T) {
	t.Parallel()
	regs, _, err := toolset.Build(t.Context(), demoCatalog(t), []toolset.Spec{{Name: "d", Type: "demo"}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	reg := regs["d"]

	defs, err := reg.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range defs {
		if !json.Valid(d.InputSchema) || !json.Valid(d.OutputSchema) || d.Description == "" {
			t.Errorf("%s: incomplete definition %+v", d.Name, d)
		}
	}

	for _, tc := range []struct{ input, want string }{
		{`{"op":"add","a":2,"b":3}`, `{"result":5}`},
		{`{"op":"sub","a":2,"b":3}`, `{"result":-1}`},
		{`{"op":"mul","a":-4,"b":3}`, `{"result":-12}`},
		{`{"op":"div","a":7,"b":2}`, `{"result":3}`},
	} {
		if got := call(t, reg, "calculate", tc.input); got != tc.want {
			t.Errorf("calculate %s = %s, want %s", tc.input, got, tc.want)
		}
	}
	for _, input := range []string{
		`{"op":"div","a":1,"b":0}`,
		`{"op":"pow","a":1,"b":2}`,
		`{"op":"add","a":9223372036854775807,"b":1}`,
		`{"op":"div","a":-9223372036854775808,"b":-1}`,
		`{"op":"mul","a":9223372036854775807,"b":2}`,
	} {
		if _, err := reg.Invoke(t.Context(), "calculate", json.RawMessage(input)); !errors.Is(err, tool.ErrToolFailed) {
			t.Errorf("calculate %s: err = %v, want ErrToolFailed", input, err)
		}
	}
	if _, err := reg.Invoke(t.Context(), "calculate", json.RawMessage(`{"op":"add","a":"x","b":1}`)); !errors.Is(err, tool.ErrInvalidInput) {
		t.Errorf("bad input: err = %v, want ErrInvalidInput", err)
	}
	if _, err := reg.Invoke(t.Context(), "nope", nil); !errors.Is(err, tool.ErrNotFound) {
		t.Errorf("unknown tool: err = %v, want ErrNotFound", err)
	}
	if got, want := call(t, reg, "current_time", `{}`), `{"time":"2026-01-02T03:04:05Z"}`; got != want {
		t.Errorf("current_time = %s, want %s", got, want)
	}
}

// TestDemoToolsAreClassified: every demo tool declares its class, read/low
// (they change nothing and reach nothing outside), and the display a console
// shows its calls with.
func TestDemoToolsAreClassified(t *testing.T) {
	t.Parallel()
	tools, err := toolset.Demo().Build(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools {
		d := tl.Definition()
		if d.Kind != tool.KindRead || d.Risk != tool.RiskLow {
			t.Errorf("%s declares %q/%q, want read/low", d.Name, d.Kind, d.Risk)
		}
		if d.Display.Title == "" || d.Display.Icon == "" || d.Display.Present == "" || d.Display.Past == "" {
			t.Errorf("%s has display %+v, want every field set", d.Name, d.Display)
		}
		if d.HasTag(tool.TagExternal) || d.Interrupts || d.RequiresApproval {
			t.Errorf("%s: %+v, want no external tag, no interrupt and no approval", d.Name, d.Metadata)
		}
	}
}
