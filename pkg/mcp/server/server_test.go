package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/mcp/server"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/tool/tooltest"
)

// fixture builds a tool from a Go function, failing the test on error.
func fixture[In, Out any](t testing.TB, name string, fn func(context.Context, In) (Out, error)) tool.Tool {
	t.Helper()
	tl, err := tool.Func(name, "Test tool "+name+".", fn)
	if err != nil {
		t.Fatalf("tool.Func(%s): %v", name, err)
	}
	return tl
}

func registry(t testing.TB, tools ...tool.Tool) *tool.LocalRegistry {
	t.Helper()
	reg, err := tool.NewRegistry(tools)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return reg
}

// fixtureTools returns the tooltest fixtures keyed by name.
func fixtureTools() map[string]tool.Tool {
	m := map[string]tool.Tool{}
	for _, tl := range tooltest.Fixtures() {
		m[tl.Definition().Name] = tl
	}
	return m
}

// twoToolsets: "a" has echo and fail; "b" has block and shout (no output schema).
func twoToolsets(t testing.TB) map[string]tool.Registry {
	fx := fixtureTools()
	shout := fixture(t, "shout", func(_ context.Context, in struct {
		Text string `json:"text"`
	},
	) (string, error) {
		return strings.ToUpper(in.Text), nil
	})
	return map[string]tool.Registry{
		"a": registry(t, fx["echo"], fx["fail"]),
		"b": registry(t, fx["block"], shout),
	}
}

func start(t testing.TB, toolsets map[string]tool.Registry, opts ...server.Option) *httptest.Server {
	t.Helper()
	srv, err := server.New(toolsets, opts...)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts
}

func connect(t testing.TB, url string, hc *http.Client) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: url, HTTPClient: hc, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect %s: %v", url, err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func listNames(t testing.TB, cs *mcp.ClientSession) []string {
	t.Helper()
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var names []string
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
	}
	slices.Sort(names)
	return names
}

func text(t testing.TB, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) != 1 {
		t.Fatalf("content = %v, want one item", res.Content)
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content[0] = %T, want text", res.Content[0])
	}
	return tc.Text
}

func TestToolsetsAreIsolated(t *testing.T) {
	t.Parallel()
	toolsets := twoToolsets(t)
	ts := start(t, toolsets)

	for name, want := range map[string][]string{"a": {"echo", "fail"}, "b": {"block", "shout"}} {
		cs := connect(t, ts.URL+"/mcp/"+name, nil)
		if got := listNames(t, cs); !slices.Equal(got, want) {
			t.Errorf("/mcp/%s tools = %v, want %v", name, got, want)
		}
	}

	// Schemas pass through exactly as the registry declared them.
	res, err := connect(t, ts.URL+"/mcp/a", nil).ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defs, _ := toolsets["a"].List(t.Context())
	for i, tl := range res.Tools {
		in, _ := json.Marshal(tl.InputSchema)
		if !jsonEqual(t, in, defs[i].InputSchema) {
			t.Errorf("%s: input schema = %s, want %s", tl.Name, in, defs[i].InputSchema)
		}
	}

	// Golden file for one tools/list result.
	got, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	testkit.Golden(t, filepath.Join("testdata", "tools_list_a.golden.json"), append(got, '\n'))
}

func jsonEqual(t testing.TB, a, b []byte) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatalf("json %s: %v", a, err)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatalf("json %s: %v", b, err)
	}
	xb, _ := json.Marshal(x)
	yb, _ := json.Marshal(y)
	return string(xb) == string(yb)
}

func TestCallTool(t *testing.T) {
	t.Parallel()
	ts := start(t, twoToolsets(t))

	t.Run("output schema gives text and structuredContent", func(t *testing.T) {
		t.Parallel()
		res, err := connect(t, ts.URL+"/mcp/a", nil).CallTool(t.Context(), &mcp.CallToolParams{
			Name: "echo", Arguments: map[string]any{"text": "hi", "times": 2},
		})
		if err != nil || res.IsError {
			t.Fatalf("CallTool: res=%+v err=%v", res, err)
		}
		if got := text(t, res); !jsonEqual(t, []byte(got), []byte(`{"text":"hihi"}`)) {
			t.Errorf("text = %s", got)
		}
		sc, ok := res.StructuredContent.(map[string]any)
		if !ok || sc["text"] != "hihi" {
			t.Errorf("structuredContent = %#v, want {text: hihi}", res.StructuredContent)
		}
	})

	t.Run("no output schema gives text only", func(t *testing.T) {
		t.Parallel()
		res, err := connect(t, ts.URL+"/mcp/b", nil).CallTool(t.Context(), &mcp.CallToolParams{
			Name: "shout", Arguments: map[string]any{"text": "hi"},
		})
		if err != nil || res.IsError {
			t.Fatalf("CallTool: res=%+v err=%v", res, err)
		}
		if got := text(t, res); got != `"HI"` {
			t.Errorf("text = %s, want \"HI\"", got)
		}
		if res.StructuredContent != nil {
			t.Errorf("structuredContent = %#v, want none", res.StructuredContent)
		}
	})
}

// vanishing lists a tool it then cannot find, like a remote registry whose
// tool was removed after the server started.
type vanishing struct{ tool.Registry }

func (v vanishing) Invoke(ctx context.Context, name string, in json.RawMessage) (json.RawMessage, error) {
	return nil, tool.ErrNotFound
}

func TestCallErrors(t *testing.T) {
	t.Parallel()
	fx := fixtureTools()
	boom := fixture(t, "boom", func(context.Context, struct{}) (struct{}, error) { panic("kaboom") })
	reg := registry(t, fx["echo"], fx["fail"], boom)
	ts := start(t, map[string]tool.Registry{"t": reg, "gone": vanishing{reg}})
	cs := connect(t, ts.URL+"/mcp/t", nil)

	for _, tc := range []struct {
		name string
		tool string
		args any
	}{
		{"invalid input", "echo", map[string]any{"text": 5}},
		{"missing input", "echo", map[string]any{}},
		{"tool failure", "fail", map[string]any{}},
		{"tool panic", "boom", map[string]any{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tc.tool, Arguments: tc.args})
			if err != nil {
				t.Fatalf("got protocol error %v, want an isError result", err)
			}
			if !res.IsError {
				t.Fatalf("result = %+v, want isError", res)
			}
			// The message is the behavior here: the model sees it, and it
			// must never carry a stack trace.
			msg := text(t, res)
			if msg == "" || strings.Contains(msg, "goroutine") || strings.Contains(msg, ".go:") {
				t.Errorf("error text = %q, want a message without a stack trace", msg)
			}
		})
	}

	t.Run("unknown tool is a protocol error", func(t *testing.T) {
		t.Parallel()
		_, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "nope", Arguments: map[string]any{}})
		var je *jsonrpc.Error
		if !errors.As(err, &je) || je.Code != jsonrpc.CodeInvalidParams {
			t.Errorf("err = %v, want a JSON-RPC invalid-params error", err)
		}
	})

	t.Run("tool that vanished from the registry is a protocol error", func(t *testing.T) {
		t.Parallel()
		res, err := connect(t, ts.URL+"/mcp/gone", nil).CallTool(t.Context(), &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"text": "x"}})
		var je *jsonrpc.Error
		if !errors.As(err, &je) || je.Code != jsonrpc.CodeInvalidParams {
			t.Errorf("res=%+v err=%v, want a JSON-RPC invalid-params error", res, err)
		}
	})
}

func TestRouting(t *testing.T) {
	t.Parallel()
	ts := start(t, twoToolsets(t))

	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"POST", "/mcp/nope", 404},
		{"GET", "/mcp/nope", 404},
		{"POST", "/mcp/a/extra", 404},
		{"POST", "/other/a", 404},
		{"POST", "/", 404},
		{"DELETE", "/mcp/", 405},
	} {
		req, _ := http.NewRequestWithContext(t.Context(), tc.method, ts.URL+tc.path, strings.NewReader(`{}`))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, resp.StatusCode, tc.want)
		}
	}

	t.Run("listing", func(t *testing.T) {
		t.Parallel()
		resp, err := http.Get(ts.URL + "/mcp/")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var got struct {
			Toolsets []server.ToolsetInfo `json:"toolsets"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		want := []server.ToolsetInfo{{Name: "a", Tools: 2}, {Name: "b", Tools: 2}}
		if !slices.Equal(got.Toolsets, want) {
			t.Errorf("toolsets = %v, want %v", got.Toolsets, want)
		}
	})

	t.Run("custom base path", func(t *testing.T) {
		t.Parallel()
		ts := start(t, twoToolsets(t), server.WithBasePath("/v1/tools/"))
		if got := listNames(t, connect(t, ts.URL+"/v1/tools/a", nil)); !slices.Equal(got, []string{"echo", "fail"}) {
			t.Errorf("tools = %v", got)
		}
		resp, err := http.Post(ts.URL+"/mcp/a", "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Errorf("default base path still served: %d", resp.StatusCode)
		}
	})
}

func TestNewRejects(t *testing.T) {
	t.Parallel()
	reg := registry(t, fixtureTools()["echo"])
	for _, name := range []string{"", "a b", "a/b", "..", "a.b", "ü", strings.Repeat("x", 65)} {
		_, err := server.New(map[string]tool.Registry{name: reg})
		if !errors.Is(err, server.ErrInvalidName) {
			t.Errorf("name %q: err = %v, want ErrInvalidName", name, err)
		}
	}
	if _, err := server.New(map[string]tool.Registry{"ok": nil}); !errors.Is(err, server.ErrInvalidConfig) {
		t.Errorf("nil registry: err = %v, want ErrInvalidConfig", err)
	}
	for _, opt := range []server.Option{server.WithMaxInFlight(-1), server.WithCallTimeout(-1), server.WithSessionTimeout(-1)} {
		if _, err := server.New(nil, opt); !errors.Is(err, server.ErrInvalidConfig) {
			t.Errorf("negative limit: err = %v, want ErrInvalidConfig", err)
		}
	}
	for _, base := range []string{"", "mcp", "/a//b"} {
		if _, err := server.New(nil, server.WithBasePath(base)); !errors.Is(err, server.ErrInvalidConfig) {
			t.Errorf("base %q: err = %v, want ErrInvalidConfig", base, err)
		}
	}
}

// watcher is a tool that reports when its context is cancelled.
type watcher struct {
	started   chan struct{}
	cancelled chan struct{}
	release   chan struct{} // closed at cleanup so a broken server cannot hang the test
}

func (w *watcher) registry(t testing.TB) tool.Registry {
	return registry(t, fixture(t, "watch", func(ctx context.Context, _ struct{}) (struct{}, error) {
		close(w.started)
		select {
		case <-ctx.Done():
			close(w.cancelled)
		case <-w.release:
		}
		return struct{}{}, ctx.Err()
	}))
}

func TestCancellation(t *testing.T) {
	t.Parallel()
	w := &watcher{started: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
	ts := start(t, map[string]tool.Registry{"w": w.registry(t)})
	t.Cleanup(func() { close(w.release) }) // runs before ts.Close

	cs := connect(t, ts.URL+"/mcp/w", nil)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "watch", Arguments: map[string]any{}})
		done <- err
	}()

	<-w.started
	cancel()
	select {
	case <-w.cancelled:
	case <-time.After(10 * time.Second):
		t.Fatal("the tool's context was not cancelled when the client cancelled")
	}
	if err := <-done; err == nil {
		t.Error("CallTool returned no error after cancel")
	}
}

func TestStatelessConcurrentClients(t *testing.T) {
	t.Parallel()
	ts := start(t, twoToolsets(t))

	var wg sync.WaitGroup
	for client := range 2 {
		cs := connect(t, ts.URL+"/mcp/a", nil)
		if id := cs.ID(); id != "" {
			t.Errorf("client %d got session id %q in stateless mode", client, id)
		}
		for call := range 10 {
			wg.Go(func() {
				res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"text": "x", "times": call + 1}})
				if err != nil || res.IsError {
					t.Errorf("client %d call %d: res=%+v err=%v", client, call, res, err)
					return
				}
				if sc, _ := res.StructuredContent.(map[string]any); sc["text"] != strings.Repeat("x", call+1) {
					t.Errorf("client %d call %d: %v", client, call, sc)
				}
			})
		}
	}
	wg.Wait()
}

func TestStateful(t *testing.T) {
	t.Parallel()
	ts := start(t, twoToolsets(t), server.WithStateful())
	cs := connect(t, ts.URL+"/mcp/a", nil)
	if cs.ID() == "" {
		t.Error("no session id in stateful mode")
	}
	if got := listNames(t, cs); !slices.Equal(got, []string{"echo", "fail"}) {
		t.Errorf("tools = %v", got)
	}
}

// headerTransport adds a header to every request.
type headerTransport struct{ key, value string }

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set(h.key, h.value)
	return http.DefaultTransport.RoundTrip(r)
}

type tenantKey struct{}

func TestMiddleware(t *testing.T) {
	t.Parallel()
	var seen sync.Map // tenants the tool saw
	whoami := fixture(t, "whoami", func(ctx context.Context, _ struct{}) (struct{ Tenant string }, error) {
		tenant, _ := ctx.Value(tenantKey{}).(string)
		seen.Store(tenant, true)
		return struct{ Tenant string }{tenant}, nil
	})
	toolsets := twoToolsets(t)
	toolsets["c"] = registry(t, whoami)

	auth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tenant := r.Header.Get("X-Tenant")
			if tenant == "" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), tenantKey{}, tenant)))
		})
	}
	ts := start(t, toolsets, server.WithMiddleware(auth))

	t.Run("blocks every path without the header", func(t *testing.T) {
		t.Parallel()
		for _, path := range []string{"/mcp/a", "/mcp/b", "/mcp/c", "/mcp/", "/mcp/unknown"} {
			resp, err := http.Post(ts.URL+path, "application/json", strings.NewReader(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("POST %s = %d, want 401", path, resp.StatusCode)
			}
		}
	})

	t.Run("lets requests with the header through, and the tool sees the context", func(t *testing.T) {
		t.Parallel()
		hc := &http.Client{Transport: headerTransport{"X-Tenant", "acme"}}
		for _, name := range []string{"a", "b"} {
			if got := listNames(t, connect(t, ts.URL+"/mcp/"+name, hc)); len(got) != 2 {
				t.Errorf("/mcp/%s tools = %v", name, got)
			}
		}
		res, err := connect(t, ts.URL+"/mcp/c", hc).CallTool(t.Context(), &mcp.CallToolParams{Name: "whoami", Arguments: map[string]any{}})
		if err != nil || res.IsError {
			t.Fatalf("CallTool: res=%+v err=%v", res, err)
		}
		if _, ok := seen.Load("acme"); !ok {
			t.Error("the tool did not see the tenant the middleware put in the request context")
		}
	})
}

func TestMaxBodyBytes(t *testing.T) {
	t.Parallel()
	ts := start(t, twoToolsets(t), server.WithMaxBodyBytes(64))
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"pad":"` + strings.Repeat("x", 200) + `"}}`
	req, _ := http.NewRequestWithContext(t.Context(), "POST", ts.URL+"/mcp/a", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", resp.StatusCode)
	}
}

// TestServeStdio uses ServeTransport, the transport-generic function that
// ServeStdio calls with the process's stdin/stdout.
func TestServeStdio(t *testing.T) {
	t.Parallel()
	reg := twoToolsets(t)["a"]
	serverSide, clientSide := mcp.NewInMemoryTransports()

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- server.ServeTransport(ctx, reg, serverSide) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if got := listNames(t, cs); !slices.Equal(got, []string{"echo", "fail"}) {
		t.Errorf("tools = %v", got)
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"text": "hi"}})
	if err != nil || res.IsError {
		t.Fatalf("CallTool: res=%+v err=%v", res, err)
	}
	if sc, _ := res.StructuredContent.(map[string]any); sc["text"] != "hi" {
		t.Errorf("structuredContent = %v", res.StructuredContent)
	}
	_ = cs.Close()
	cancel()
	<-done
}

// holdRegistry has one tool, "hold", that reports it started and then waits
// for its context or for release.
func holdRegistry(t testing.TB, started chan<- struct{}, release <-chan struct{}) tool.Registry {
	return registry(t, fixture(t, "hold", func(ctx context.Context, _ struct{}) (struct{}, error) {
		started <- struct{}{}
		select {
		case <-ctx.Done():
		case <-release:
		}
		return struct{}{}, ctx.Err()
	}))
}

func TestCallTimeout(t *testing.T) {
	t.Parallel()
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	ts := start(t, map[string]tool.Registry{"h": holdRegistry(t, started, release)}, server.WithCallTimeout(50*time.Millisecond))
	t.Cleanup(func() { close(release) }) // a server that ignores the timeout must not hang the test

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	res, err := connect(t, ts.URL+"/mcp/h", nil).CallTool(ctx, &mcp.CallToolParams{Name: "hold", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("CallTool: %v, want an isError result saying the tool timed out", err)
	}
	if !res.IsError {
		t.Errorf("result = %+v, want isError", res)
	}
}

func TestMaxInFlight(t *testing.T) {
	t.Parallel()
	started := make(chan struct{}, 8)
	release := make(chan struct{})
	ts := start(t, map[string]tool.Registry{"h": holdRegistry(t, started, release)}, server.WithMaxInFlight(2))
	released := false
	releaseOnce := func() {
		if !released {
			released = true
			close(release)
		}
	}
	t.Cleanup(releaseOnce)

	// Fill both slots with calls that stay open.
	var wg sync.WaitGroup
	for range 2 {
		cs := connect(t, ts.URL+"/mcp/h", nil)
		wg.Go(func() {
			_, _ = cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "hold", Arguments: map[string]any{}})
		})
	}
	<-started
	<-started

	post := func() int {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, "POST", ts.URL+"/mcp/h", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusServiceUnavailable && resp.Header.Get("Retry-After") == "" {
			t.Error("503 without Retry-After")
		}
		return resp.StatusCode
	}
	if got := post(); got != http.StatusServiceUnavailable {
		t.Errorf("request over the limit = %d, want 503", got)
	}
	// The listing and unknown paths are not limited.
	if resp, err := http.Get(ts.URL + "/mcp/"); err != nil || resp.StatusCode != 200 {
		t.Errorf("listing while busy: %v %v", resp, err)
	}

	releaseOnce()
	wg.Wait()
	// The slot is freed just after the response is sent, so poll briefly.
	testkit.Eventually(t, 10*time.Second, func() bool { return post() == http.StatusOK })
}

// TestToolsListPublishesAnnotations: over MCP, each tool's annotations are
// its metadata mapped by tool.Metadata.Annotations, and a tool whose
// metadata MCP cannot express has none.
func TestToolsListPublishesAnnotations(t *testing.T) {
	t.Parallel()
	del, err := tool.Func("drop", "Delete a thing.",
		func(context.Context, struct{}) (struct{}, error) { return struct{}{}, nil },
		tool.WithMetadata(tool.Metadata{
			Kind: tool.KindDelete, Risk: tool.RiskHigh, Tags: []string{tool.TagExternal},
			Display: tool.Display{Title: "Drop a thing", Present: "Dropping", Past: "Dropped"},
		}))
	if err != nil {
		t.Fatal(err)
	}
	fx := fixtureTools()
	plain := fixture(t, "plain", func(context.Context, struct{}) (struct{}, error) { return struct{}{}, nil })
	tools := []tool.Tool{fx["echo"], del, plain}
	cs := connect(t, start(t, map[string]tool.Registry{"a": registry(t, tools...)}).URL+"/mcp/a", nil)
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]*mcp.ToolAnnotations{}
	for _, tl := range res.Tools {
		listed[tl.Name] = tl.Annotations
	}
	for _, tl := range tools {
		d := tl.Definition()
		want := d.Annotations()
		got, ok := listed[d.Name]
		if !ok {
			t.Errorf("%s is not listed", d.Name)
			continue
		}
		var gotA *tool.Annotations
		if got != nil {
			gotA = &tool.Annotations{
				Title: got.Title, ReadOnlyHint: got.ReadOnlyHint, DestructiveHint: got.DestructiveHint,
				IdempotentHint: got.IdempotentHint, OpenWorldHint: got.OpenWorldHint,
			}
		}
		if !reflect.DeepEqual(gotA, want) {
			t.Errorf("%s: listed annotations %+v, want %+v", d.Name, gotA, want)
		}
	}
	if listed["drop"] == nil || listed["drop"].DestructiveHint == nil || !*listed["drop"].DestructiveHint {
		t.Errorf("drop, a delete, is listed with annotations %+v; want destructiveHint true", listed["drop"])
	}
	if listed["plain"] != nil {
		t.Errorf("plain, unclassified, is listed with annotations %+v; want none", listed["plain"])
	}
}
