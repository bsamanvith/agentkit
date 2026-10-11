// Package tooltest provides a conformance suite for [tool.Registry]
// implementations and the fixture tools it runs against.
//
// Every Registry implementation (the in-process registry, remote clients)
// runs the same suite, so they behave the same way:
//
//	func TestConformance(t *testing.T) {
//		tooltest.RunRegistry(t, func(t *testing.T, tools []tool.Tool) tool.Registry {
//			return newMyRegistry(t, tools)
//		})
//	}
package tooltest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/tool"
)

// ErrFixture is returned by the "fail" fixture tool.
var ErrFixture = errors.New("fixture failure")

// EchoInput is the input of the "echo" fixture tool.
type EchoInput struct {
	Text  string `json:"text" jsonschema:"text to echo"`
	Times int    `json:"times,omitempty" jsonschema:"how many times to repeat the text (default 1)"`
}

// EchoOutput is the output of the "echo" fixture tool.
type EchoOutput struct {
	Text string `json:"text"`
}

// Fixtures returns the tools the conformance suite expects the registry under
// test to contain:
//
//   - "echo" repeats its input text;
//   - "fail" always returns ErrFixture;
//   - "block" waits until its context is done.
func Fixtures() []tool.Tool {
	echo, err := tool.Func("echo", "Repeat the input text.",
		func(_ context.Context, in EchoInput) (EchoOutput, error) {
			n := max(in.Times, 1)
			return EchoOutput{Text: strings.Repeat(in.Text, n)}, nil
		})
	must(err)
	fail, err := tool.Func("fail", "Always fail.",
		func(context.Context, struct{}) (struct{}, error) { return struct{}{}, ErrFixture })
	must(err)
	block, err := tool.Func("block", "Wait until cancelled.",
		func(ctx context.Context, _ struct{}) (struct{}, error) {
			<-ctx.Done()
			return struct{}{}, ctx.Err()
		})
	must(err)
	return []tool.Tool{echo, fail, block}
}

// RunRegistry runs the conformance suite. newRegistry must return a registry
// containing exactly the given tools.
func RunRegistry(t *testing.T, newRegistry func(t *testing.T, tools []tool.Tool) tool.Registry) {
	t.Helper()
	setup := func(t *testing.T) tool.Registry {
		t.Helper()
		return newRegistry(t, Fixtures())
	}

	t.Run("ListSortedByName", func(t *testing.T) {
		t.Parallel()
		defs, err := setup(t).List(t.Context())
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		var names []string
		for _, d := range defs {
			names = append(names, d.Name)
			if !json.Valid(d.InputSchema) {
				t.Errorf("%s: input schema is not valid JSON: %s", d.Name, d.InputSchema)
			}
		}
		if want := []string{"block", "echo", "fail"}; !reflect.DeepEqual(names, want) {
			t.Errorf("names = %v, want %v", names, want)
		}
	})

	t.Run("ListReturnsCopies", func(t *testing.T) {
		t.Parallel()
		reg := setup(t)
		defs, err := reg.List(t.Context())
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		before := string(defs[0].InputSchema)
		defs[0].Name = "changed"
		for i := range defs[0].InputSchema {
			defs[0].InputSchema[i] = ' '
		}
		again, err := reg.List(t.Context())
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if again[0].Name != "block" || string(again[0].InputSchema) != before {
			t.Errorf("modifying a listed definition changed the registry: %+v", again[0])
		}
	})

	t.Run("InvokeRoutesByName", func(t *testing.T) {
		t.Parallel()
		out, err := setup(t).Invoke(t.Context(), "echo", json.RawMessage(`{"text":"hi","times":2}`))
		if err != nil {
			t.Fatalf("Invoke: %v", err)
		}
		var got EchoOutput
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("output %s: %v", out, err)
		}
		if got.Text != "hihi" {
			t.Errorf("text = %q, want %q", got.Text, "hihi")
		}
	})

	t.Run("UnknownToolIsErrNotFound", func(t *testing.T) {
		t.Parallel()
		_, err := setup(t).Invoke(t.Context(), "nope", json.RawMessage(`{}`))
		if !errors.Is(err, tool.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("InvalidInputIsErrInvalidInput", func(t *testing.T) {
		t.Parallel()
		_, err := setup(t).Invoke(t.Context(), "echo", json.RawMessage(`{"text":5}`))
		if !errors.Is(err, tool.ErrInvalidInput) || errors.Is(err, tool.ErrToolFailed) {
			t.Errorf("err = %v, want ErrInvalidInput only", err)
		}
	})

	t.Run("ToolFailureIsErrToolFailed", func(t *testing.T) {
		t.Parallel()
		_, err := setup(t).Invoke(t.Context(), "fail", nil)
		if !errors.Is(err, tool.ErrToolFailed) || errors.Is(err, tool.ErrInvalidInput) {
			t.Errorf("err = %v, want ErrToolFailed only", err)
		}
	})

	t.Run("ExpiredContextIsContextError", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		defer cancel()
		_, err := setup(t).Invoke(ctx, "block", nil)
		if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, tool.ErrToolFailed) {
			t.Errorf("err = %v, want context.DeadlineExceeded and not ErrToolFailed", err)
		}
	})

	t.Run("OutputIsJSON", func(t *testing.T) {
		t.Parallel()
		out, err := setup(t).Invoke(t.Context(), "echo", json.RawMessage(`{"text":"x"}`))
		if err != nil {
			t.Fatalf("Invoke: %v", err)
		}
		if !json.Valid(out) || !bytes.HasPrefix(bytes.TrimSpace(out), []byte("{")) {
			t.Errorf("output is not a JSON object: %s", out)
		}
	})
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
