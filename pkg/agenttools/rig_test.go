package agenttools_test

import (
	"context"
	"encoding/json"
	"iter"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agenttools"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/mcp/server"
	"github.com/bsamanvith/agentkit/pkg/store/scoped"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// The rig's API keys. Every one has every scope unless it says otherwise.
const (
	keyFull   = "full-key-0123456789abcdef"   // tenant t1, every toolset
	keyOther  = "other-key-0123456789abcdef"  // tenant t2, every toolset
	keyNarrow = "narrow-key-0123456789abcdef" // tenant t1, toolsets agentkit and demo
	keyReader = "reader-key-0123456789abcdef" // tenant t1, scope agent:read only
	keyNoMCP  = "nomcp-key-0123456789abcdef"  // tenant t1, toolset demo only: not agentkit
)

// rigOptions configure newRig.
type rigOptions struct {
	// remote serves the agent API on its own httptest server and gives the
	// toolset its URL, as when the MCP server runs elsewhere; otherwise the
	// toolset calls the API in process (WithHandler).
	remote bool
	// settings are the toolset's settings besides "url".
	settings map[string]any
	// interruptAtOnce sets the API's reconnect grace to 0 (default 1
	// minute): a run nobody follows is interrupted at once.
	interruptAtOnce bool
	// fakeClock gives the toolset a clocktest.Fake (rig.clock) for its wait.
	fakeClock bool
	// open serves both with no authentication.
	open bool
	// callTimeout is the MCP server's call timeout (0: none).
	callTimeout time.Duration
}

// rig is the agent API with two agents on fake models, the agentkit toolset
// over it served by pkg/mcp/server, and MCP SDK clients of that.
type rig struct {
	t       *testing.T
	helper  llm.Model // the default agent's model: toolset demo
	wide    llm.Model // agent "wide": toolsets demo and secret
	factory *agenttools.Factory
	mcpURL  string
	clock   *clocktest.Fake // with rigOptions.fakeClock
	// apiCalls counts the requests the agent API got, by path.
	mu       sync.Mutex
	apiCalls []string
	apiAuth  []string // the Authorization header of each
}

func newRig(t *testing.T, helper, wide llm.Model, o rigOptions) *rig {
	t.Helper()
	r := &rig{t: t, helper: helper, wide: wide}
	ctx := t.Context()
	catalog, err := toolset.NewCatalog(toolset.Demo())
	if err != nil {
		t.Fatal(err)
	}
	sets, _, err := toolset.Build(ctx, catalog, []toolset.Spec{{Name: "demo", Type: "demo"}, {Name: "secret", Type: "demo"}})
	if err != nil {
		t.Fatal(err)
	}
	harnessOf := func(m llm.Model, names ...string) *harness.Harness {
		pick := map[string]tool.Registry{}
		for _, n := range names {
			pick[n] = sets[n]
		}
		reg, err := agentapi.MergeTools(ctx, pick, len(names) > 1)
		if err != nil {
			t.Fatal(err)
		}
		h, err := harness.New(m, reg)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	var authn auth.Authenticator
	if !o.open {
		key := func(id, k, tenant string, scopes, toolsets []string) auth.APIKey {
			e, err := auth.KeyEntry(auth.APIKey{ID: id, Tenant: tenant, Scopes: scopes, Toolsets: toolsets}, k)
			if err != nil {
				t.Fatal(err)
			}
			return e
		}
		all := []string{"*"}
		keys, err := auth.NewAPIKeys([]auth.APIKey{
			key("full", keyFull, "t1", all, all),
			key("other", keyOther, "t2", all, all),
			key("narrow", keyNarrow, "t1", all, []string{"agentkit", "demo"}),
			key("reader", keyReader, "t1", []string{"agent:read"}, all),
			key("nomcp", keyNoMCP, "t1", all, []string{"demo"}),
		})
		if err != nil {
			t.Fatal(err)
		}
		authn = keys
	}
	grace := time.Minute
	if o.interruptAtOnce {
		grace = 0
	}
	apiOpts := []agentapi.Option{
		agentapi.WithStore(scoped.Memory(nil)),
		agentapi.WithReconnectGrace(grace),
		agentapi.WithToolsets(agentapi.Toolset{Name: "demo", Type: "demo", Tools: sets["demo"]}, agentapi.Toolset{Name: "secret", Type: "demo", Tools: sets["secret"]}),
	}
	if authn != nil {
		apiOpts = append(apiOpts, agentapi.WithAuthenticator(authn))
	}
	api, err := agentapi.NewAgents([]agentapi.Agent{
		{Name: agentapi.DefaultAgent, Description: "helps", Harness: harnessOf(helper, "demo"), Toolsets: []string{"demo"}},
		{Name: "wide", Description: "sees more", Harness: harnessOf(wide, "demo", "secret"), Toolsets: []string{"demo", "secret"}, PrefixToolNames: true},
	}, apiOpts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = api.Shutdown(ctx)
	})
	apiHandler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.apiCalls = append(r.apiCalls, req.Method+" "+req.URL.Path)
		r.apiAuth = append(r.apiAuth, strings.Join(req.Header.Values("Authorization"), ","))
		r.mu.Unlock()
		api.Handler().ServeHTTP(w, req)
	})

	opts := []agenttools.Option{agenttools.WithCallTimeout(o.callTimeout)}
	if o.fakeClock {
		r.clock = clocktest.NewFake(time.Time{})
		opts = append(opts, agenttools.WithClock(r.clock))
	}
	settings := map[string]any{}
	for k, v := range o.settings {
		settings[k] = v
	}
	if o.remote {
		ts := httptest.NewServer(apiHandler)
		t.Cleanup(ts.Close)
		settings["url"] = ts.URL
		opts = append(opts, agenttools.WithHTTPClient(ts.Client()))
	} else {
		opts = append(opts, agenttools.WithHandler(apiHandler))
	}
	r.factory, err = agenttools.New(opts...)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(settings)
	regs, _, err := toolset.Build(ctx, mustCatalog(t, r.factory), []toolset.Spec{{Name: "agentkit", Type: agenttools.Type, Settings: raw}})
	if err != nil {
		t.Fatal(err)
	}
	srvOpts := []server.Option{server.WithBasePath("/mcp"), server.WithCallTimeout(o.callTimeout)}
	if authn != nil {
		srvOpts = append(srvOpts,
			server.WithMiddleware(func(h http.Handler) http.Handler {
				return agenttools.ForwardCredentials(auth.Middleware(authn)(h))
			}),
			server.WithAccess(func(ctx context.Context, name string) bool {
				p, ok := auth.From(ctx)
				return ok && p.AllowsToolset(name)
			}))
	}
	srv, err := server.New(regs, srvOpts...)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	r.mcpURL = ts.URL + "/mcp/agentkit"
	// Last: the factory closes before the servers do.
	t.Cleanup(func() { r.check() })
	return r
}

func mustCatalog(t *testing.T, f toolset.Factory) *toolset.Catalog {
	t.Helper()
	c, err := toolset.NewCatalog(f)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// check is the invariant after every test: Close stops every run follower
// it left (a stuck one hangs the test here), and no request reached the API
// with a credential no MCP caller sent.
func (r *rig) check() {
	r.t.Helper()
	if err := r.factory.Close(); err != nil {
		r.t.Errorf("Factory.Close: %v", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, a := range r.apiAuth {
		if a != "" && !slices.Contains(sentAuth, a) {
			r.t.Errorf("%s reached the API with Authorization %q, which no MCP caller sent", r.apiCalls[i], a)
		}
	}
}

// sentAuth are the Authorization headers the tests' MCP clients send.
var sentAuth = []string{"Bearer " + keyFull, "Bearer " + keyOther, "Bearer " + keyNarrow, "Bearer " + keyReader, "Bearer " + keyNoMCP}

// runPastTimeout calls agent_run, which the gated model holds, and lets the
// toolset's wait (on the fake clock, wait long) run out: the result is a
// run still going.
func (r *rig) runPastTimeout(cs *mcp.ClientSession, model *gated, wait time.Duration, args map[string]any) agenttools.Run {
	r.t.Helper()
	type result struct {
		run agenttools.Run
		err string
	}
	done := make(chan result, 1)
	go func() {
		var res result
		res.err = call(r.t, cs, "agent_run", args, &res.run)
		done <- res
	}()
	<-model.waiting
	if err := r.clock.WaitForTimers(r.t.Context(), 1); err != nil {
		r.t.Fatal(err)
	}
	r.clock.Advance(wait)
	res := <-done
	if res.err != "" {
		r.t.Fatalf("agent_run: %s", res.err)
	}
	return res.run
}

// waitEnd waits until run_get shows the run ended, with its answer or error,
// and returns it. (Right as a run ends, GET /v1/runs/{id} can show the
// stored status before the run's last event, so without the answer.)
func waitEnd(t *testing.T, cs *mcp.ClientSession, runID string) agenttools.Run {
	t.Helper()
	var got agenttools.Run
	testkit.Eventually(t, 10*time.Second, func() bool {
		mustCall(t, cs, "run_get", map[string]any{"run_id": runID}, &got)
		return got.Status != wire.StatusRunning && (got.Answer != "" || got.Error != nil)
	})
	return got
}

// http404 is a handler that answers 404 to everything.
type http404 struct{}

func (http404) ServeHTTP(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }

// client is an MCP SDK client session of the toolset, sending key ("" for
// none).
func (r *rig) client(key string) *mcp.ClientSession {
	r.t.Helper()
	cs, err := r.connect(key)
	if err != nil {
		r.t.Fatalf("connect: %v", err)
	}
	return cs
}

func (r *rig) connect(key string) (*mcp.ClientSession, error) {
	hc := &http.Client{Transport: bearer{key, http.DefaultTransport}}
	c := mcp.NewClient(&mcp.Implementation{Name: "agenttools-test", Version: "0"}, nil)
	cs, err := c.Connect(r.t.Context(), &mcp.StreamableClientTransport{Endpoint: r.mcpURL, HTTPClient: hc, DisableStandaloneSSE: true}, nil)
	if err != nil {
		return nil, err
	}
	r.t.Cleanup(func() { _ = cs.Close() })
	return cs, nil
}

type bearer struct {
	key  string
	base http.RoundTripper
}

func (b bearer) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	if b.key != "" {
		req.Header.Set("Authorization", "Bearer "+b.key)
	}
	return b.base.RoundTrip(req)
}

// call calls a tool and decodes its structured result into out; it returns
// the result's text when it is an error result.
func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, out any) (errText string) {
	t.Helper()
	if args == nil {
		args = map[string]any{} // the SDK sends nil as null, which no tool takes
	}
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		var b strings.Builder
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				b.WriteString(tc.Text)
			}
		}
		if b.Len() == 0 {
			t.Fatalf("%s: an error result with no text", name)
		}
		return b.String()
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s: %v in %s", name, err, raw)
		}
	}
	return ""
}

// mustCall is call for a call that must succeed.
func mustCall(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, out any) {
	t.Helper()
	if e := call(t, cs, name, args, out); e != "" {
		t.Fatalf("%s(%v): error result %q", name, args, e)
	}
}

// gated is a model that waits for release (or its context) before each
// reply of the model it wraps, and says when a call is waiting.
type gated struct {
	llm.Model
	waiting chan struct{} // gets a value when a call starts waiting
	release chan struct{} // closed to let the calls go
}

func newGated(m llm.Model) *gated {
	return &gated{Model: m, waiting: make(chan struct{}, 16), release: make(chan struct{})}
}

func (g *gated) Generate(ctx context.Context, req llm.Request) iter.Seq2[llm.Event, error] {
	return func(yield func(llm.Event, error) bool) {
		g.waiting <- struct{}{}
		select {
		case <-g.release:
		case <-ctx.Done():
			yield(llm.Event{}, ctx.Err())
			return
		}
		for e, err := range g.Model.Generate(ctx, req) {
			if !yield(e, err) {
				return
			}
		}
	}
}
