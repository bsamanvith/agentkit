package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/mcp/client"
	"github.com/bsamanvith/agentkit/pkg/mcp/server"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// stdioEnv makes the test binary an MCP server on stdio serving the demo
// toolset (TestStdio runs it as the command).
const stdioEnv = "AGENTKIT_MCP_CLIENT_TEST_STDIO"

func TestMain(m *testing.M) {
	if os.Getenv(stdioEnv) == "1" {
		// TestStdio passes exactly these; anything else leaked from the
		// parent's environment.
		for _, kv := range os.Environ() {
			if k, _, _ := strings.Cut(kv, "="); k != stdioEnv && k != "ECHO_PREFIX" && k != "PATH" {
				fmt.Fprintln(os.Stderr, "unexpected variable", k)
				os.Exit(3)
			}
		}
		regs, _, err := toolset.Build(context.Background(), demoCatalog(), []toolset.Spec{
			{Name: "demo", Type: toolset.DemoType, Settings: json.RawMessage(`{"echo_prefix": "` + os.Getenv("ECHO_PREFIX") + `"}`)},
		})
		if err == nil {
			err = server.ServeStdio(context.Background(), regs["demo"])
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func demoCatalog() *toolset.Catalog {
	c, err := toolset.NewCatalog(toolset.Demo())
	if err != nil {
		panic(err)
	}
	return c
}

// ourServer is agentkit's own MCP server with the demo toolset at /mcp/demo.
func ourServer(t *testing.T) *httptest.Server {
	t.Helper()
	regs, _, err := toolset.Build(t.Context(), demoCatalog(), []toolset.Spec{{Name: "demo", Type: toolset.DemoType}})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := server.New(regs)
	if err != nil {
		t.Fatal(err)
	}
	return serve(t, srv)
}

// serve starts h; at cleanup it ends open streams first, since clients keep
// one open for server notifications.
func serve(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(func() {
		ts.CloseClientConnections()
		ts.Close()
	})
	return ts
}

// fake is an upstream MCP server built with the SDK, with test tools.
type fake struct {
	srv       *mcp.Server
	cancelled atomic.Int32 // calls of "block" whose context ended
	started   chan struct{}
}

func newFake() *fake {
	f := &fake{srv: mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "1"}, nil), started: make(chan struct{}, 8)}
	text := func(s string) *mcp.CallToolResult {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
	}
	obj := &jsonObject
	f.srv.AddTool(&mcp.Tool{Name: "search", Description: "Search.", InputSchema: obj}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return text("found " + string(req.Params.Arguments)), nil
	})
	f.srv.AddTool(&mcp.Tool{Name: "fails", InputSchema: obj}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "quota exceeded"}}}, nil
	})
	f.srv.AddTool(&mcp.Tool{Name: "big", InputSchema: obj}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return text(strings.Repeat("x", 100_000)), nil
	})
	f.srv.AddTool(&mcp.Tool{Name: "block", InputSchema: obj}, func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		f.started <- struct{}{}
		<-ctx.Done()
		f.cancelled.Add(1)
		return nil, ctx.Err()
	})
	return f
}

var jsonObject = map[string]any{"type": "object"}

func (f *fake) streamable(t *testing.T, opts *mcp.StreamableHTTPOptions) *httptest.Server {
	t.Helper()
	return serve(t, mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return f.srv }, opts))
}

func (f *fake) sseOnly(t *testing.T) *httptest.Server {
	t.Helper()
	return serve(t, mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return f.srv }, nil))
}

// build builds the "mcp" toolset from settings, failing the test on error.
func build(t *testing.T, settings string, opts ...client.Option) map[string]tool.Tool {
	t.Helper()
	tools, err := client.Factory(opts...).Build(t.Context(), json.RawMessage(settings))
	if err != nil {
		t.Fatalf("Build(%s): %v", settings, err)
	}
	t.Cleanup(func() { _ = tools[0].(io.Closer).Close() })
	byName := map[string]tool.Tool{}
	for _, tl := range tools {
		byName[tl.Definition().Name] = tl
	}
	return byName
}

func names(tools map[string]tool.Tool) []string {
	var out []string
	for n := range tools {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

func settingsFor(url string, extra ...string) string {
	return `{"url": "` + url + `"` + strings.Join(append([]string{""}, extra...), ", ") + `}`
}

// The client against agentkit's own server: the demo tools with their
// schemas, and a call.
func TestRoundTripOwnServer(t *testing.T) {
	t.Parallel()
	ts := ourServer(t)
	tools := build(t, settingsFor(ts.URL+"/mcp/demo"))
	if got := names(tools); !slices.Equal(got, []string{"calculate", "current_time", "echo"}) {
		t.Fatalf("tools = %v", got)
	}
	def := tools["calculate"].Definition()
	var in struct{ Properties map[string]any }
	if err := json.Unmarshal(def.InputSchema, &in); err != nil || in.Properties["op"] == nil || def.OutputSchema == nil || def.Description == "" {
		t.Errorf("calculate definition = %+v", def)
	}
	out, err := tools["calculate"].Invoke(t.Context(), json.RawMessage(`{"op": "mul", "a": 6, "b": 7}`))
	if err != nil || string(out) != `{"result":42}` {
		t.Errorf("calculate = %s, %v", out, err)
	}
	// A call the server refuses (the schema says no) is a tool error.
	if _, err := tools["calculate"].Invoke(t.Context(), json.RawMessage(`{"op": "pow", "a": 1, "b": 2}`)); !errors.Is(err, tool.ErrToolFailed) {
		t.Errorf("calculate pow: %v, want a tool error", err)
	}
}

func TestTransports(t *testing.T) {
	t.Parallel()
	f := newFake()
	streamable := f.streamable(t, nil)
	sse := f.sseOnly(t)
	for _, tc := range []struct {
		name, url, transport string
		want                 client.Transport
	}{
		{"auto, streamable", streamable.URL, "auto", client.TransportHTTP},
		{"http", streamable.URL, "http", client.TransportHTTP},
		{"auto falls back to SSE", sse.URL, "auto", client.TransportSSE},
		{"sse", sse.URL, "sse", client.TransportSSE},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, err := client.Connect(t.Context(), client.Config{URL: tc.url, Transport: client.Transport(tc.transport)})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if got := c.Transport(); got != tc.want {
				t.Errorf("transport = %q, want %q", got, tc.want)
			}
			res, err := c.Call(t.Context(), "search", []byte(`{"q":"go"}`))
			if err != nil || contentText(res) != `found {"q":"go"}` {
				t.Errorf("search = %v, %v", contentText(res), err)
			}
		})
	}
	// Streamable HTTP alone does not fall back.
	if _, err := client.Connect(t.Context(), client.Config{URL: sse.URL, Transport: client.TransportHTTP}); err == nil {
		t.Error("transport http against an SSE-only server connected")
	}
}

func contentText(res *mcp.CallToolResult) string {
	if res == nil || len(res.Content) == 0 {
		return ""
	}
	tc, _ := res.Content[0].(*mcp.TextContent)
	if tc == nil {
		return ""
	}
	return tc.Text
}

// stdio: the test binary is the server; only PATH, HOME and the variables
// named in env reach it.
func TestStdio(t *testing.T) {
	t.Parallel()
	env := map[string]string{stdioEnv: "1", "ECHO_PREFIX": "[stdio] ", "PATH": os.Getenv("PATH"), "SECRET_ELSEWHERE": "x"}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	settings := fmt.Sprintf(`{"transport": "stdio", "command": %q, "env": [%q, "ECHO_PREFIX"]}`, os.Args[0], stdioEnv)
	tools := build(t, settings, client.WithLookupEnv(lookup))
	out, err := tools["echo"].Invoke(t.Context(), json.RawMessage(`{"text": "hi"}`))
	if err != nil || string(out) != `{"text":"[stdio] hi"}` {
		t.Errorf("echo over stdio = %s, %v", out, err)
	}
}

// Everything that goes wrong on the server's side is a tool error the model
// sees.
func TestErrors(t *testing.T) {
	t.Parallel()
	f := newFake()
	ts := f.streamable(t, nil)
	tools := build(t, settingsFor(ts.URL, `"timeout": "300ms"`, `"max_result_bytes": 10000`))
	c, err := client.Connect(t.Context(), client.Config{URL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// A result within what the wire allows but over the limit as JSON.
	mid := build(t, settingsFor(ts.URL, `"max_result_bytes": 50000`))
	missing, err := c.Wrap(&mcp.Tool{Name: "not_on_server", InputSchema: jsonObject})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		tool  tool.Tool
		input string
		want  string
	}{
		{"isError", tools["fails"], `{}`, "quota exceeded"},
		{"protocol error", missing, `{}`, "refused the call"},
		{"timeout", tools["block"], `{}`, "did not answer within 300ms"},
		{"oversized", tools["big"], `{}`, "limit of 10000"},
		{"oversized JSON", mid["big"], `{}`, "over this toolset's limit of 50000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := tc.tool.Invoke(t.Context(), json.RawMessage(tc.input))
			if !errors.Is(err, tool.ErrToolFailed) || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("= %s, %v; want a tool error saying %q", out, err, tc.want)
			}
		})
	}
	if _, err := tools["search"].Invoke(t.Context(), json.RawMessage(`[1]`)); !errors.Is(err, tool.ErrInvalidInput) {
		t.Errorf("array input: %v, want ErrInvalidInput", err)
	}
}

// A server that forgets the session: the call goes again on a new session.
func TestReconnect(t *testing.T) {
	t.Parallel()
	f := newFake()
	var forget atomic.Value // a session id the server answers 404 for
	forget.Store("")
	var seen atomic.Value // the last session id a request carried
	seen.Store("")
	var inits atomic.Int32
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return f.srv }, nil)
	ts := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("Mcp-Session-Id")
		if id == "" && r.Method == http.MethodPost {
			inits.Add(1)
		} else if id != "" {
			seen.Store(id)
		}
		if id != "" && id == forget.Load() {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		h.ServeHTTP(w, r)
	}))
	tools := build(t, settingsFor(ts.URL))
	search := tools["search"]
	if _, err := search.Invoke(t.Context(), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if seen.Load() == "" {
		t.Fatal("the server issued no session")
	}
	forget.Store(seen.Load())
	before := inits.Load()
	if out, err := search.Invoke(t.Context(), json.RawMessage(`{"n":1}`)); err != nil || !strings.Contains(string(out), "found") {
		t.Fatalf("call after the session was dropped = %s, %v", out, err)
	}
	if inits.Load() == before {
		t.Error("no new session after the server dropped the old one")
	}
}

func TestCancel(t *testing.T) {
	t.Parallel()
	f := newFake()
	ts := f.streamable(t, nil)
	tools := build(t, settingsFor(ts.URL))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := tools["block"].Invoke(ctx, json.RawMessage(`{}`))
		done <- err
	}()
	<-f.started
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("cancelled call = %v, want context.Canceled", err)
		}
		if took := time.Since(start); took > time.Second {
			t.Errorf("returned %s after cancel", took)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled call did not return")
	}
	// The server hears of it: notifications/cancelled ends the tool's context.
	testkit.Eventually(t, 5*time.Second, func() bool { return f.cancelled.Load() == 1 })
}

// notifications/tools/list_changed reaches ToolsChanged.
func TestToolsChanged(t *testing.T) {
	t.Parallel()
	f := newFake()
	ts := f.streamable(t, nil)
	c, err := client.Connect(t.Context(), client.Config{URL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	before, err := c.Tools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	ok := false
	deadline := time.After(10 * time.Second)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for i := 0; !ok; i++ {
		// Keep adding tools until the standalone stream is up and a
		// notification arrives.
		f.srv.AddTool(&mcp.Tool{Name: fmt.Sprintf("added_%d", i), InputSchema: &jsonObject}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		})
		select {
		case <-c.ToolsChanged():
			once.Do(func() { ok = true })
		case <-tick.C:
		case <-deadline:
			t.Fatal("no list_changed signal")
		}
	}
	after, err := c.Tools(t.Context())
	if err != nil || len(after) <= len(before) {
		t.Errorf("tools after the change = %d, before %d (%v)", len(after), len(before), err)
	}
}

// One client serves concurrent calls (go test -race).
func TestConcurrentCalls(t *testing.T) {
	t.Parallel()
	tools := build(t, settingsFor(newFake().streamable(t, nil).URL))
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in := fmt.Sprintf(`{"i":%d}`, i)
			out, err := tools["search"].Invoke(t.Context(), json.RawMessage(in))
			if err == nil && !strings.Contains(string(out), `\"i\":`+fmt.Sprint(i)) {
				err = fmt.Errorf("call %d got %s", i, out)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
}
