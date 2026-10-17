package toolrpc_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/rpc"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/tool/toolrpc"
	"github.com/bsamanvith/agentkit/pkg/tool/tooltest"
)

// serve serves tools as toolset "fx" (and a skipped toolset "off"), and
// returns a client over HTTP or in process.
func serve(t *testing.T, tools []tool.Tool, http bool) *toolrpc.Client {
	t.Helper()
	reg, err := tool.NewRegistry(tools)
	if err != nil {
		t.Fatal(err)
	}
	s := rpc.NewServer()
	sets := []toolrpc.Toolset{
		{Name: "fx", Type: "fixtures", Credential: toolrpc.Credential{Status: "not_needed"}},
		{Name: "off", Type: "tavily", Skipped: "no key", Credential: toolrpc.Credential{Status: "missing", Env: "TAVILY_API_KEY"}},
	}
	if err := toolrpc.Register(s, sets, map[string]tool.Registry{"fx": reg}); err != nil {
		t.Fatal(err)
	}
	if !http {
		return toolrpc.NewClient(rpc.Pipe(s))
	}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	conn, err := rpc.Dial(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return toolrpc.NewClient(conn)
}

// A remote registry passes the suite every registry passes.
func TestConformance(t *testing.T) {
	for name, http := range map[string]bool{"HTTP": true, "Pipe": false} {
		t.Run(name, func(t *testing.T) {
			tooltest.RunRegistry(t, func(t *testing.T, tools []tool.Tool) tool.Registry {
				return serve(t, tools, http).Registry("fx")
			})
		})
	}
}

func TestToolsetsListsBuiltAndSkipped(t *testing.T) {
	t.Parallel()
	got, err := serve(t, tooltest.Fixtures(), true).Toolsets(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []toolrpc.Toolset{
		{Name: "fx", Type: "fixtures", Credential: toolrpc.Credential{Status: "not_needed"}},
		{Name: "off", Type: "tavily", Skipped: "no key", Credential: toolrpc.Credential{Status: "missing", Env: "TAVILY_API_KEY"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Toolsets = %+v, want %+v", got, want)
	}
}

func TestToolsetNotServed(t *testing.T) {
	t.Parallel()
	c := serve(t, tooltest.Fixtures(), true)
	for _, name := range []string{"off", "nope"} {
		reg := c.Registry(name)
		if _, err := reg.List(t.Context()); !errors.Is(err, toolrpc.ErrNoToolset) {
			t.Errorf("%s: List err = %v, want ErrNoToolset", name, err)
		}
		if _, err := reg.Invoke(t.Context(), "echo", json.RawMessage(`{}`)); !errors.Is(err, toolrpc.ErrNoToolset) {
			t.Errorf("%s: Invoke err = %v, want ErrNoToolset", name, err)
		}
	}
}

// A tool that panics on the server reports a panic, not a crash.
func TestPanicKeepsItsIdentity(t *testing.T) {
	t.Parallel()
	boom, err := tool.Func("boom", "Panic.", func(context.Context, struct{}) (struct{}, error) { panic("boom") })
	if err != nil {
		t.Fatal(err)
	}
	_, err = serve(t, []tool.Tool{boom}, true).Registry("fx").Invoke(t.Context(), "boom", nil)
	if !errors.Is(err, tool.ErrToolFailed) || !errors.Is(err, tool.ErrPanic) {
		t.Errorf("err = %v, want ErrToolFailed and ErrPanic", err)
	}
}

func TestRegisterRejectsInconsistentToolsets(t *testing.T) {
	t.Parallel()
	reg, err := tool.NewRegistry(tooltest.Fixtures())
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		sets []toolrpc.Toolset
		regs map[string]tool.Registry
	}{
		"listed twice":        {[]toolrpc.Toolset{{Name: "a"}, {Name: "a"}}, map[string]tool.Registry{"a": reg}},
		"built, no registry":  {[]toolrpc.Toolset{{Name: "a"}}, nil},
		"skipped, registry":   {[]toolrpc.Toolset{{Name: "a", Skipped: "x"}}, map[string]tool.Registry{"a": reg}},
		"registry not listed": {nil, map[string]tool.Registry{"a": reg}},
	} {
		if err := toolrpc.Register(rpc.NewServer(), tc.sets, tc.regs); err == nil {
			t.Errorf("%s: Register succeeded", name)
		}
	}
}

// A toolsets component that is down fails the call as a tool failure, which
// the model sees, as a local connector's dropped connection is.
func TestUnreachableIsAToolFailure(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(rpc.NewServer())
	url := srv.URL
	srv.Close()
	conn, err := rpc.Dial(url)
	if err != nil {
		t.Fatal(err)
	}
	_, err = toolrpc.NewClient(conn).Registry("fx").Invoke(t.Context(), "echo", json.RawMessage(`{}`))
	if !errors.Is(err, tool.ErrToolFailed) || !errors.Is(err, rpc.ErrUnavailable) {
		t.Errorf("err = %v, want ErrToolFailed and ErrUnavailable", err)
	}
}

// A nil output comes back nil, not as the JSON null.
func TestNilOutput(t *testing.T) {
	t.Parallel()
	s := rpc.NewServer()
	if err := toolrpc.Register(s, []toolrpc.Toolset{{Name: "nil", Type: "test"}}, map[string]tool.Registry{"nil": nilRegistry{}}); err != nil {
		t.Fatal(err)
	}
	out, err := toolrpc.NewClient(rpc.Pipe(s)).Registry("nil").Invoke(t.Context(), "x", nil)
	if err != nil || out != nil {
		t.Errorf("Invoke = %q, %v; want nil, nil", out, err)
	}
}

type nilRegistry struct{}

func (nilRegistry) List(context.Context) ([]tool.Definition, error) { return nil, nil }
func (nilRegistry) Invoke(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, nil
}
