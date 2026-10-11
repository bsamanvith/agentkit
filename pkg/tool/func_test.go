package tool_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

type searchInput struct {
	Query   string            `json:"query" jsonschema:"what to search for"`
	Limit   int               `json:"limit,omitempty" jsonschema:"maximum number of results"`
	Filters filters           `json:"filters,omitzero"`
	Tags    []string          `json:"tags,omitempty"`
	Labels  map[string]string `json:"labels,omitempty"`
}

type filters struct {
	Lang  string `json:"lang"`
	Since string `json:"since,omitempty" jsonschema:"RFC 3339 date"`
}

type searchOutput struct {
	Results []result `json:"results"`
	Total   int      `json:"total"`
}

type result struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

func search(_ context.Context, in searchInput) (searchOutput, error) {
	return searchOutput{
		Results: []result{{Title: in.Query + " in " + in.Filters.Lang, URL: "https://example.com"}},
		Total:   max(in.Limit, 1),
	}, nil
}

func newSearch(t *testing.T) *tool.FuncTool[searchInput, searchOutput] {
	t.Helper()
	st, err := tool.Func("search", "Search the docs.", search)
	if err != nil {
		t.Fatalf("Func: %v", err)
	}
	return st
}

func TestFuncDefinitionGolden(t *testing.T) {
	t.Parallel()
	got, err := json.MarshalIndent(newSearch(t).Definition(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	testkit.Golden(t, "testdata/search_definition.json", append(got, '\n'))
}

func TestFuncRejectsInvalidDefinition(t *testing.T) {
	t.Parallel()
	noop := func(context.Context, struct{}) (struct{}, error) { return struct{}{}, nil }
	tests := map[string]func() error{
		"empty name": func() error { _, err := tool.Func("", "", noop); return err },
		"name with space": func() error {
			_, err := tool.Func("my tool", "", noop)
			return err
		},
		"name too long": func() error {
			_, err := tool.Func(fmt.Sprintf("%065d", 0), "", noop)
			return err
		},
		"nil function": func() error {
			_, err := tool.Func[struct{}, struct{}]("ok", "", nil)
			return err
		},
		"non-object input": func() error {
			_, err := tool.Func("ok", "", func(context.Context, string) (struct{}, error) { return struct{}{}, nil })
			return err
		},
		"input without a schema": func() error {
			_, err := tool.Func("ok", "", func(context.Context, struct{ C chan int }) (struct{}, error) { return struct{}{}, nil })
			return err
		},
		"output without a schema": func() error {
			_, err := tool.Func("ok", "", func(context.Context, struct{}) (func(), error) { return nil, nil })
			return err
		},
	}
	for name, build := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := build(); !errors.Is(err, tool.ErrInvalidDefinition) {
				t.Errorf("err = %v, want ErrInvalidDefinition", err)
			}
		})
	}
}

func TestDefinitionIsACopy(t *testing.T) {
	t.Parallel()
	st := newSearch(t)
	before, err := json.Marshal(st.Definition()) // snapshot, shares no memory
	if err != nil {
		t.Fatal(err)
	}
	d := st.Definition()
	d.Name = "changed"
	d.InputSchema[0] = '!'
	d.OutputSchema[0] = '!'
	after, err := json.Marshal(st.Definition())
	if err != nil {
		t.Fatalf("definition after modifying a copy: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("modifying a returned Definition changed the tool:\nbefore %s\nafter  %s", before, after)
	}
}

func TestFuncInvoke(t *testing.T) {
	t.Parallel()
	out, err := newSearch(t).Invoke(t.Context(), json.RawMessage(`{"query":"agents","limit":3,"filters":{"lang":"go"}}`))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	var got searchOutput
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output %s: %v", out, err)
	}
	if got.Total != 3 || len(got.Results) != 1 || got.Results[0].Title != "agents in go" {
		t.Errorf("output = %+v", got)
	}
}

func TestFuncEmptyInput(t *testing.T) {
	t.Parallel()
	called := false
	nt, err := tool.Func("now", "", func(context.Context, struct{}) (string, error) {
		called = true
		return "ok", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []json.RawMessage{nil, json.RawMessage(""), json.RawMessage("  \n")} {
		called = false
		out, err := nt.Invoke(t.Context(), in)
		if err != nil || !called || string(out) != `"ok"` {
			t.Errorf("input %q: out=%s err=%v called=%v", in, out, err, called)
		}
	}
}

func TestFuncInvalidInput(t *testing.T) {
	t.Parallel()
	inputs := map[string]string{
		"malformed JSON":         `{"query":`,
		"not an object":          `["agents"]`,
		"null":                   `null`,
		"missing required field": `{"limit":3}`,
		"wrong type":             `{"query":7}`,
		"wrong nested type":      `{"query":"x","filters":{"lang":1}}`,
		"missing nested field":   `{"query":"x","filters":{}}`,
		"fraction for integer":   `{"query":"x","limit":1.5}`,
		"unknown field":          `{"query":"x","color":"red"}`,
	}
	for name, input := range inputs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			called := false
			st, err := tool.Func("search", "", func(ctx context.Context, in searchInput) (searchOutput, error) {
				called = true
				return search(ctx, in)
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = st.Invoke(t.Context(), json.RawMessage(input))
			if !errors.Is(err, tool.ErrInvalidInput) || errors.Is(err, tool.ErrToolFailed) {
				t.Errorf("err = %v, want ErrInvalidInput only", err)
			}
			if called {
				t.Error("function was called with invalid input")
			}
		})
	}
}

func TestFuncToolError(t *testing.T) {
	t.Parallel()
	errQuota := errors.New("quota exceeded")
	qt, err := tool.Func("quota", "", func(context.Context, struct{}) (struct{}, error) {
		return struct{}{}, fmt.Errorf("calling upstream: %w", errQuota)
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = qt.Invoke(t.Context(), nil)
	if !errors.Is(err, tool.ErrToolFailed) || !errors.Is(err, errQuota) {
		t.Errorf("err = %v, want ErrToolFailed wrapping the tool's error", err)
	}
	var te *tool.Error
	if !errors.As(err, &te) || te.Tool != "quota" {
		t.Errorf("errors.As(*tool.Error) = %+v", te)
	}
}

func TestFuncPanic(t *testing.T) {
	t.Parallel()
	pt, err := tool.Func("boom", "", func(context.Context, struct{}) (struct{}, error) {
		panic("boom")
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = pt.Invoke(t.Context(), nil)
	if !errors.Is(err, tool.ErrToolFailed) || !errors.Is(err, tool.ErrPanic) {
		t.Errorf("err = %v, want ErrToolFailed and ErrPanic", err)
	}
}

func TestFuncContextCanceled(t *testing.T) {
	t.Parallel()

	t.Run("cancelled while running", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(t.Context())
		started := make(chan struct{})
		wt, err := tool.Func("wait", "", func(ctx context.Context, _ struct{}) (struct{}, error) {
			close(started)
			<-ctx.Done()
			return struct{}{}, fmt.Errorf("waiting: %w", ctx.Err()) // tools often wrap it
		})
		if err != nil {
			t.Fatal(err)
		}
		go func() { <-started; cancel() }()
		_, err = wt.Invoke(ctx, nil)
		if !errors.Is(err, context.Canceled) || errors.Is(err, tool.ErrToolFailed) {
			t.Errorf("err = %v, want context.Canceled and not ErrToolFailed", err)
		}
	})

	t.Run("cancelled before the call", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		called := false
		nt, err := tool.Func("never", "", func(context.Context, struct{}) (struct{}, error) {
			called = true
			return struct{}{}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = nt.Invoke(ctx, nil)
		if !errors.Is(err, context.Canceled) || called {
			t.Errorf("err = %v called = %v, want context.Canceled without calling the tool", err, called)
		}
	})
}

func FuzzFuncInvoke(f *testing.F) {
	for _, seed := range []string{
		``, `{}`, `null`, `[]`, `"x"`, `{"query":"a"}`,
		`{"query":"a","limit":3,"filters":{"lang":"go","since":"2026-01-01"},"tags":["x"],"labels":{"k":"v"}}`,
		`{"query":"a","limit":1e309}`, `{"query":"\ud800"}`, `{"query":"a","query":"b"}`,
	} {
		f.Add([]byte(seed))
	}
	st, err := tool.Func("search", "", search)
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		out, err := st.Invoke(context.Background(), input)
		switch {
		case err == nil:
			if !json.Valid(out) {
				t.Errorf("success with invalid JSON output %q", out)
			}
		case errors.Is(err, tool.ErrInvalidInput), errors.Is(err, tool.ErrToolFailed):
		default:
			t.Errorf("unexpected error kind: %v", err)
		}
	})
}
