package tool_test

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
)

func TestRegistryConformance(t *testing.T) {
	t.Parallel()
	tooltest.RunRegistry(t, func(t *testing.T, tools []tool.Tool) tool.Registry {
		reg, err := tool.NewRegistry(tools)
		if err != nil {
			t.Fatalf("NewRegistry: %v", err)
		}
		return reg
	})
}

func TestNewRegistryRejects(t *testing.T) {
	t.Parallel()
	echo := tooltest.Fixtures()[0]

	t.Run("duplicate name", func(t *testing.T) {
		t.Parallel()
		_, err := tool.NewRegistry([]tool.Tool{echo, tooltest.Fixtures()[0]})
		if !errors.Is(err, tool.ErrDuplicateName) {
			t.Errorf("err = %v, want ErrDuplicateName", err)
		}
	})
	t.Run("invalid name on a custom tool", func(t *testing.T) {
		t.Parallel()
		_, err := tool.NewRegistry([]tool.Tool{customTool{name: "has space"}})
		if !errors.Is(err, tool.ErrInvalidDefinition) {
			t.Errorf("err = %v, want ErrInvalidDefinition", err)
		}
	})
	t.Run("input schema that is not a JSON object", func(t *testing.T) {
		t.Parallel()
		for _, schema := range []string{"", "null", "[]", `"object"`, "{"} {
			_, err := tool.NewRegistry([]tool.Tool{customTool{name: "custom", schema: &schema}})
			if !errors.Is(err, tool.ErrInvalidDefinition) {
				t.Errorf("schema %q: err = %v, want ErrInvalidDefinition", schema, err)
			}
		}
	})
	t.Run("nil tool", func(t *testing.T) {
		t.Parallel()
		_, err := tool.NewRegistry([]tool.Tool{echo, nil})
		if !errors.Is(err, tool.ErrInvalidDefinition) {
			t.Errorf("err = %v, want ErrInvalidDefinition", err)
		}
	})
}

func TestRegistryRecoversPanics(t *testing.T) {
	t.Parallel()
	reg, err := tool.NewRegistry([]tool.Tool{customTool{name: "custom", panics: true}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = reg.Invoke(t.Context(), "custom", nil)
	if !errors.Is(err, tool.ErrToolFailed) || !errors.Is(err, tool.ErrPanic) {
		t.Errorf("err = %v, want ErrToolFailed and ErrPanic", err)
	}
}

func TestMiddlewareOrder(t *testing.T) {
	t.Parallel()
	var (
		mu    sync.Mutex
		trace []string
	)
	record := func(s string) {
		mu.Lock()
		defer mu.Unlock()
		trace = append(trace, s)
	}
	tag := func(label string) tool.Middleware {
		return func(next tool.InvokeFunc) tool.InvokeFunc {
			return func(ctx context.Context, def tool.Definition, in json.RawMessage) (json.RawMessage, error) {
				record(label + ">" + def.Name)
				out, err := next(ctx, def, in)
				record(label + "<")
				return out, err
			}
		}
	}
	reg, err := tool.NewRegistry(tooltest.Fixtures(), tool.WithMiddleware(tag("a"), tag("b")), tool.WithMiddleware(tag("c")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Invoke(t.Context(), "echo", json.RawMessage(`{"text":"x"}`)); err != nil {
		t.Fatal(err)
	}
	want := []string{"a>echo", "b>echo", "c>echo", "c<", "b<", "a<"}
	if !slices.Equal(trace, want) {
		t.Errorf("trace = %v, want %v", trace, want)
	}
}

func TestTimeout(t *testing.T) {
	t.Parallel()
	reg, err := tool.NewRegistry(tooltest.Fixtures(), tool.WithMiddleware(tool.Timeout(250*time.Millisecond)))
	if err != nil {
		t.Fatal(err)
	}

	// Safety net: if Timeout did nothing, the call ends with context.Canceled
	// after 5s instead of hanging, and the check below fails.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	time.AfterFunc(5*time.Second, cancel)

	_, err = reg.Invoke(ctx, "block", nil)
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, tool.ErrToolFailed) {
		t.Errorf("slow tool: err = %v, want context.DeadlineExceeded", err)
	}
	if _, err := reg.Invoke(t.Context(), "echo", json.RawMessage(`{"text":"x"}`)); err != nil {
		t.Errorf("fast tool: %v", err)
	}
}

func TestRegistryConcurrentUse(t *testing.T) {
	t.Parallel()
	reg, err := tool.NewRegistry(tooltest.Fixtures(), tool.WithMiddleware(tool.Timeout(time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			if _, err := reg.List(t.Context()); err != nil {
				t.Error(err)
			}
			out, err := reg.Invoke(t.Context(), "echo", json.RawMessage(`{"text":"x","times":3}`))
			if err != nil || string(out) != `{"text":"xxx"}` {
				t.Errorf("out=%s err=%v", out, err)
			}
			if _, err := reg.Invoke(t.Context(), "fail", nil); !errors.Is(err, tool.ErrToolFailed) {
				t.Errorf("fail: %v", err)
			}
		})
	}
	wg.Wait()
}

// customTool is a Tool not built with Func, to check what the registry
// guarantees on its own.
type customTool struct {
	name   string
	panics bool
	schema *string // input schema; nil means a valid object schema
}

func (c customTool) Definition() tool.Definition {
	schema := json.RawMessage(`{"type":"object"}`)
	if c.schema != nil {
		schema = json.RawMessage(*c.schema)
	}
	return tool.Definition{Name: c.name, InputSchema: schema}
}

func (c customTool) Invoke(context.Context, json.RawMessage) (json.RawMessage, error) {
	if c.panics {
		panic("custom tool bug")
	}
	return json.RawMessage(`{}`), nil
}
