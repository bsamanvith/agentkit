//go:build !noagent

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/store/scoped"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// memoryServer is the agent component built from a config as serve builds
// it, with fake models by model ID, and the model flags each agent was
// built with.
type memoryServer struct {
	url   string
	ts    *httptest.Server
	stop  func()
	mu    sync.Mutex
	flags map[string]modelFlags
}

func startAgents(t *testing.T, config string, models map[string]*llmtest.Fake) (*memoryServer, error) {
	t.Helper()
	return startAgentsIn(t, t.TempDir(), config, models, func() (*toolset.Catalog, error) {
		return toolset.NewCatalog(toolset.Demo(), memoryNamedFactory{})
	})
}

// startAgentsIn is startAgents with the config file in dir and the toolsets
// of catalog.
func startAgentsIn(t *testing.T, dir, config string, models map[string]*llmtest.Fake, catalog func() (*toolset.Catalog, error)) (*memoryServer, error) {
	t.Helper()
	path := filepath.Join(dir, "agentkit.json")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadServeConfig(path)
	if err != nil {
		return nil, err
	}
	s := &memoryServer{flags: map[string]modelFlags{}}
	c := cli{
		getenv:  func(string) string { return "" },
		catalog: catalog,
		newModel: func(f modelFlags) (llm.Model, error) {
			s.mu.Lock()
			s.flags[f.model] = f
			s.mu.Unlock()
			if m, ok := models[f.model]; ok {
				return m, nil
			}
			return nil, fmt.Errorf("no fake model %q", f.model)
		},
	}
	env := serveEnv{components: []string{agentapi.Name}, addr: "127.0.0.1:0", shutdownTimeout: time.Second}
	log := slog.New(slog.DiscardHandler)
	built, problems, err := c.buildToolsets(t.Context(), env, cfg, path, log)
	if err != nil {
		return nil, err
	}
	authn, err := c.authenticator(t.Context(), cfg, log)
	if err != nil {
		return nil, err
	}
	m, _, err := c.newAgents(t.Context(), cfg, built, problems, env, authn, scoped.Memory(nil), nil, clock.Real(), log)
	if err != nil {
		return nil, err
	}
	s.ts = httptest.NewServer(m.Component.Handler())
	s.url = s.ts.URL
	var once sync.Once
	s.stop = func() {
		once.Do(func() {
			s.ts.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := m.Component.Shutdown(ctx); err != nil {
				t.Errorf("shutdown: %v", err)
			}
		})
	}
	t.Cleanup(s.stop)
	return s, nil
}

func (s *memoryServer) client(t *testing.T, token string) *agentclient.Client {
	t.Helper()
	opts := []agentclient.Option{agentclient.WithHTTPClient(s.ts.Client())}
	if token != "" {
		opts = append(opts, agentclient.WithToken(token))
	}
	c, err := agentclient.New(s.url, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func memoryCall(id, input string) llmtest.Reply {
	return llmtest.ToolCalls(llm.ToolUse{ID: id, Name: "memory", Input: json.RawMessage(input)})
}

// toolResult is the text of the result of call id in req, or "" and false.
func toolResult(req llm.Request, id string) (string, bool) {
	for _, m := range req.Messages {
		for _, b := range m.Content {
			if r, ok := b.(llm.ToolResult); ok && r.ToolUseID == id {
				var text string
				if json.Unmarshal([]byte(r.Content), &text) != nil {
					text = r.Content
				}
				return text, !r.IsError
			}
		}
	}
	return "", false
}

// lastText is the text of the last message of req, its blocks joined.
func lastText(req llm.Request) string {
	if len(req.Messages) == 0 {
		return ""
	}
	var parts []string
	for _, b := range req.Messages[len(req.Messages)-1].Content {
		if t, ok := b.(llm.Text); ok {
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// TestMemoryLoads: each new message starts with the agent's memory: nothing
// while it is empty, every file's text while it is small, and past
// load_bytes the file list, after which the agent greps it.
func TestMemoryLoads(t *testing.T) {
	t.Parallel()
	config := `{"agents": [{"name": "default", "model": {"id": "m"},
	  "memory": {"store": {"backend": "memory", "load_bytes": 40}}}]}`
	fake := llmtest.NewFake(t,
		memoryCall("save", `{"command":"create","path":"/memories/prefs.md","file_text":"Units: metric\n"}`), llmtest.Text("ok"),
		memoryCall("more", `{"command":"create","path":"/memories/notes.md","file_text":"`+strings.Repeat("x", 30)+`"}`), llmtest.Text("ok"),
		memoryCall("grep", `{"command":"grep","pattern":"(?i)units"}`), llmtest.Text("Metric."),
	)
	s, err := startAgents(t, config, map[string]*llmtest.Fake{"m": fake})
	if err != nil {
		t.Fatal(err)
	}
	c := s.client(t, "")
	for range 3 {
		if _, err := c.Run(t.Context(), agentclient.RunRequest{Message: "Which units do I like?"}); err != nil {
			t.Fatal(err)
		}
	}
	reqs := fake.Requests()
	if got := lastText(reqs[0]); got != "Which units do I like?" {
		t.Errorf("with no memory, the message is %q", got)
	}
	if got := lastText(reqs[2]); !strings.HasPrefix(got, "<memory>\n") || !strings.HasSuffix(got, "</memory>\nWhich units do I like?") ||
		!strings.Contains(got, "<file path=\"/memories/prefs.md\">\nUnits: metric\n</file>") {
		t.Errorf("with a small memory, the message is\n%s", got)
	}
	got := lastText(reqs[4])
	if !strings.Contains(got, "holds 2 files, 44 in all") || !strings.Contains(got, "grep command") || strings.Contains(got, "Units: metric") {
		t.Errorf("past load_bytes, the message is\n%s", got)
	}
	if out, ok := toolResult(reqs[5], "grep"); !ok || out != "/memories/prefs.md:1: Units: metric" {
		t.Errorf("grep: %q, ok %v", out, ok)
	}
}

// TestAgentWithoutMemoryHasNoMemoryTool: only an agent with a memory
// section is offered the memory tool (the fakes record the tools of each
// request), and only native_tool asks the model for the native type.
func TestAgentWithoutMemoryHasNoMemoryTool(t *testing.T) {
	t.Parallel()
	const config = `{
	  "mcp": {"toolsets": [{"name": "demo", "type": "demo"}]},
	  "agents": [
	    {"name": "plain", "model": {"id": "plain"}, "toolsets": ["demo"]},
	    {"name": "remembers", "model": {"id": "remembers"}, "toolsets": ["demo"], "memory": {"store": {"backend": "memory"}}},
	    {"name": "native", "model": {"id": "native"}, "memory": {"store": {"backend": "memory"}, "native_tool": true}}
	  ]
	}`
	fakes := map[string]*llmtest.Fake{
		"plain": llmtest.NewFake(t, llmtest.Text("hi")), "remembers": llmtest.NewFake(t, llmtest.Text("hi")),
		"native": llmtest.NewFake(t, llmtest.Text("hi")),
	}
	s, err := startAgents(t, config, fakes)
	if err != nil {
		t.Fatal(err)
	}
	c := s.client(t, "")
	for name, f := range fakes {
		if _, err := c.Run(t.Context(), agentclient.RunRequest{Message: "hi", Agent: name}); err != nil {
			t.Fatalf("run %s: %v", name, err)
		}
		tools := toolNames(f.Requests()[0])
		if has := slices.Contains(tools, "memory"); has != (name != "plain") {
			t.Errorf("agent %s was offered %v", name, tools)
		}
		if native := s.flags[name].nativeMemory; native != (name == "native") {
			t.Errorf("agent %s: model built with nativeMemory %v", name, native)
		}
	}
}

// TestMemoryAcrossConversations: run 1 saves a preference; run 2, in a new
// conversation, views it and gets it; after a restart on the same SQLite
// file, run 3 still does.
func TestMemoryAcrossConversations(t *testing.T) {
	t.Parallel()
	db := filepath.Join(t.TempDir(), "memories.db")
	config := `{"agents": [{"name": "default", "model": {"id": "m"},
	  "memory": {"store": {"backend": "sqlite", "path": ` + strconv.Quote(db) + `}}}]}`
	const view = `{"command":"view","path":"/memories/preferences.md"}`
	fake := llmtest.NewFake(t,
		memoryCall("save", `{"command":"create","path":"/memories/preferences.md","file_text":"Units: metric\n"}`),
		llmtest.Text("I'll remember that."),
		memoryCall("recall", view),
		llmtest.Text("Mount Everest is 8,849 m tall."),
	)
	s, err := startAgents(t, config, map[string]*llmtest.Fake{"m": fake})
	if err != nil {
		t.Fatal(err)
	}
	c := s.client(t, "")
	first, err := c.Run(t.Context(), agentclient.RunRequest{Message: "I prefer metric units. Remember that."})
	if err != nil {
		t.Fatal(err)
	}
	if out, ok := toolResult(fake.Requests()[1], "save"); !ok || out != "File created successfully at: /memories/preferences.md" {
		t.Errorf("save: %q, ok %v", out, ok)
	}
	second, err := c.Run(t.Context(), agentclient.RunRequest{Message: "How tall is Everest?"})
	if err != nil {
		t.Fatal(err)
	}
	reqs := fake.Requests()
	if second.ConversationID == first.ConversationID || len(reqs[2].Messages) != 1 {
		t.Fatalf("run 2 is not a new conversation: %s after %s, %d messages", second.ConversationID, first.ConversationID, len(reqs[2].Messages))
	}
	if out, ok := toolResult(reqs[3], "recall"); !ok || !strings.Contains(out, "Units: metric") {
		t.Errorf("recall in a new conversation: %q, ok %v; want the saved preference", out, ok)
	}
	if !strings.Contains(second.Answer, "8,849 m") {
		t.Errorf("answer = %q", second.Answer)
	}

	// A restart on the same file.
	s.stop()
	again := llmtest.NewFake(t, memoryCall("after", view), llmtest.Text("Metric."))
	s2, err := startAgents(t, config, map[string]*llmtest.Fake{"m": again})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.client(t, "").Run(t.Context(), agentclient.RunRequest{Message: "Which units?"}); err != nil {
		t.Fatal(err)
	}
	if out, ok := toolResult(again.Requests()[1], "after"); !ok || !strings.Contains(out, "Units: metric") {
		t.Errorf("recall after a restart: %q, ok %v", out, ok)
	}
}

// TestMemoryUserScope: with scope user, two users of one tenant (two API
// keys) each have their own memories, through the API.
func TestMemoryUserScope(t *testing.T) {
	t.Parallel()
	keys := ""
	for i, user := range []string{"u1", "u2"} {
		e, err := authKeyEntry(fmt.Sprintf("k%d", i+1), "acme", user, "secret-key-of-user-"+user)
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 {
			keys += ","
		}
		keys += e
	}
	config := `{"auth": {"api_keys": [` + keys + `]},
	  "agents": [{"name": "default", "model": {"id": "m"}, "memory": {"store": {"backend": "memory", "scope": "user"}}}]}`
	fake := llmtest.NewFake(t,
		memoryCall("u1-save", `{"command":"create","path":"/memories/me.md","file_text":"I am u1"}`), llmtest.Text("ok"),
		memoryCall("u2-view", `{"command":"view","path":"/memories"}`), llmtest.Text("ok"),
		memoryCall("u1-view", `{"command":"view","path":"/memories"}`), llmtest.Text("ok"),
	)
	s, err := startAgents(t, config, map[string]*llmtest.Fake{"m": fake})
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"u1", "u2", "u1"} {
		if _, err := s.client(t, "secret-key-of-user-"+user).Run(t.Context(), agentclient.RunRequest{Message: "hi"}); err != nil {
			t.Fatalf("run as %s: %v", user, err)
		}
	}
	reqs := fake.Requests()
	if out, ok := toolResult(reqs[3], "u2-view"); !ok || strings.Contains(out, "me.md") {
		t.Errorf("u2's view: %q, ok %v; want no u1 file", out, ok)
	}
	if out, ok := toolResult(reqs[5], "u1-view"); !ok || !strings.Contains(out, "/memories/me.md") {
		t.Errorf("u1's view: %q, ok %v; want its file", out, ok)
	}
}

// memoryNamedFactory is a toolset type "memory-named" with one tool named
// "memory", which an agent with memory must not have too.
type memoryNamedFactory struct{}

func (memoryNamedFactory) Type() string { return "memory-named" }

func (memoryNamedFactory) Build(context.Context, toolset.BuildInput) ([]tool.Tool, error) {
	t, err := tool.Func("memory", "Not the memory tool.", func(context.Context, struct{}) (string, error) { return "", nil })
	return []tool.Tool{t}, err
}

// authKeyEntry is the JSON of an API key of tenant and user with every scope.
func authKeyEntry(id, tenant, user, key string) (string, error) {
	e, err := auth.KeyEntry(auth.APIKey{ID: id, Tenant: tenant, User: user, Scopes: []string{"*"}}, key)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(e)
	return string(b), err
}

// TestRunnerMemoryNeedsURL: a runner keeps no memory, so an agent with
// memory fails its startup without $AGENTKIT_MEMORY_URL, naming it; auto
// recall, which would run on the runner, is refused there and in the agent
// API that sends it runs.
func TestRunnerMemoryNeedsURL(t *testing.T) {
	t.Parallel()
	load := func(memory string) serveConfig {
		path := filepath.Join(t.TempDir(), "agentkit.json")
		if err := os.WriteFile(path, []byte(`{"agent": {"model": {"id": "m"}, "memory": `+memory+`}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadServeConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	c := cli{getenv: func(string) string { return "" }, newModel: func(modelFlags) (llm.Model, error) { return llmtest.NewFake(t), nil }}
	log := slog.New(slog.DiscardHandler)
	env := serveEnv{components: []string{runnerName}, addr: "127.0.0.1:0", shutdownTimeout: time.Second}
	store := load(`{"store": {"backend": "memory"}}`)
	if _, err := c.newRunner(t.Context(), store, nil, env, clock.Real(), log); err == nil || !strings.Contains(err.Error(), "agent.memory: the runner keeps no memory: set $AGENTKIT_MEMORY_URL") {
		t.Errorf("runner with a memory agent and no memory URL: %v", err)
	}
	recall := load(`{"index": {"backend": "memory", "auto_recall": true}}`)
	env.memoryURL = "http://127.0.0.1:1"
	if _, err := c.newRunner(t.Context(), recall, nil, env, clock.Real(), log); err == nil || !strings.Contains(err.Error(), "agent.memory.index.auto_recall") {
		t.Errorf("runner with auto recall: %v, want it refused", err)
	}
	env = serveEnv{components: []string{agentapi.Name}, addr: "127.0.0.1:0", shutdownTimeout: time.Second, runnerURL: "http://127.0.0.1:1"}
	if _, _, err := c.newAgents(t.Context(), recall, nil, nil, env, nil, scoped.Memory(nil), nil, clock.Real(), log); err == nil || !strings.Contains(err.Error(), "agent.memory.index.auto_recall") {
		t.Errorf("agent API on a runner with auto recall: %v, want it refused", err)
	}
}

// TestSplitMemory: an agent on a runner keeps its memory in the agent API
// (the runner holds none) and reads it back in a new conversation, each of
// two users apart; the memory tool's texts are those of one process; the
// agent's other tools never call the agent API.
func TestSplitMemory(t *testing.T) {
	t.Parallel()
	keys := ""
	for i, user := range []string{"u1", "u2"} {
		e, err := auth.KeyEntry(auth.APIKey{ID: fmt.Sprintf("k%d", i+1), Tenant: "acme", User: user, Scopes: []string{"*"}, Toolsets: []string{"*"}}, "secret-key-of-user-"+user)
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 {
			keys += ","
		}
		keys += string(b)
	}
	path := filepath.Join(t.TempDir(), "agentkit.json")
	config := `{"auth": {"api_keys": [` + keys + `]},
	  "mcp": {"toolsets": [{"name": "demo", "type": "demo"}]},
	  "agents": [{"name": "default", "model": {"id": "m"}, "toolsets": ["demo"],
	    "memory": {"store": {"backend": "memory", "scope": "user", "max_file_bytes": 64}}}]}`
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadServeConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	fake := llmtest.NewFake(t,
		memoryCall("u1-save", `{"command":"create","path":"/memories/me.md","file_text":"Units: metric\n"}`), llmtest.Text("ok"),
		memoryCall("u1-big", `{"command":"create","path":"/memories/big.md","file_text":"`+strings.Repeat("x", 65)+`"}`), llmtest.Text("ok"),
		llmtest.ToolCalls(llm.ToolUse{ID: "echo", Name: "echo", Input: json.RawMessage(`{"text":"hi"}`)}), llmtest.Text("ok"),
		memoryCall("u2-view", `{"command":"view","path":"/memories"}`), llmtest.Text("ok"),
		memoryCall("u1-view", `{"command":"view","path":"/memories/me.md"}`), llmtest.Text("ok"),
	)
	// The two servers name each other: start both on handlers set later.
	var apiH, runH http.Handler
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { apiH.ServeHTTP(w, r) }))
	t.Cleanup(apiSrv.Close)
	runSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { runH.ServeHTTP(w, r) }))
	t.Cleanup(runSrv.Close)
	log := slog.New(slog.DiscardHandler)
	c := cli{
		getenv:   func(string) string { return "" },
		catalog:  func() (*toolset.Catalog, error) { return toolset.NewCatalog(toolset.Demo()) },
		newModel: func(modelFlags) (llm.Model, error) { return fake, nil },
	}
	const token = "rpc-token-0123456789abcdef"
	runEnv := serveEnv{components: []string{runnerName}, addr: "127.0.0.1:0", shutdownTimeout: time.Second, rpcToken: token, memoryURL: apiSrv.URL}
	built, _, err := c.buildToolsets(t.Context(), runEnv, cfg, path, log)
	if err != nil {
		t.Fatal(err)
	}
	run, err := c.newRunner(t.Context(), cfg, built, runEnv, clock.Real(), log)
	if err != nil {
		t.Fatal(err)
	}
	runH = run.Component.Handler()
	apiEnv := serveEnv{components: []string{agentapi.Name}, addr: "127.0.0.1:0", shutdownTimeout: time.Second, rpcToken: token, runnerURL: runSrv.URL}
	authn, err := c.authenticator(t.Context(), cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := c.newAgents(t.Context(), cfg, built, nil, apiEnv, authn, scoped.Memory(nil), nil, clock.Real(), log)
	if err != nil {
		t.Fatal(err)
	}
	ac := m.Component.(*agentComponent)
	if ac.memory == nil {
		t.Fatal("the agent API serves no memory")
	}
	mux := http.NewServeMux()
	mux.Handle("/v1/", ac.Handler())
	var invokes atomic.Int32 // memory tool calls the agent API answered
	mux.Handle("/rpc/memory/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Invoke") {
			invokes.Add(1)
		}
		ac.memory.Handler().ServeHTTP(w, r)
	}))
	apiH = mux
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = ac.Shutdown(ctx)
		_ = run.Component.Shutdown(ctx)
	})
	client := func(user string) *agentclient.Client {
		cl, err := agentclient.New(apiSrv.URL, agentclient.WithToken("secret-key-of-user-"+user))
		if err != nil {
			t.Fatal(err)
		}
		return cl
	}
	for _, user := range []string{"u1", "u1", "u1", "u2", "u1"} {
		if _, err := client(user).Run(t.Context(), agentclient.RunRequest{Message: "hi", Agent: "default"}); err != nil {
			t.Fatalf("run as %s: %v", user, err)
		}
	}
	reqs := fake.Requests()
	if out, ok := toolResult(reqs[1], "u1-save"); !ok || out != "File created successfully at: /memories/me.md" {
		t.Errorf("u1's save: %q, ok %v", out, ok)
	}
	if out, ok := toolResult(reqs[3], "u1-big"); ok || !strings.Contains(out, "The memory is full") {
		t.Errorf("u1's write past the file limit: %q, ok %v; want the memory tool's limit text", out, ok)
	}
	if out, ok := toolResult(reqs[5], "echo"); !ok || !strings.Contains(out, "hi") {
		t.Errorf("echo: %q, ok %v", out, ok)
	}
	if out, ok := toolResult(reqs[7], "u2-view"); !ok || strings.Contains(out, "me.md") {
		t.Errorf("u2's view: %q, ok %v; want no u1 file", out, ok)
	}
	if out, ok := toolResult(reqs[9], "u1-view"); !ok || !strings.Contains(out, "Units: metric") {
		t.Errorf("u1's view in a new conversation: %q, ok %v", out, ok)
	}
	// Each run starts with the memory, loaded from the agent API too.
	if got := lastText(reqs[8]); !strings.Contains(got, "<file path=\"/memories/me.md\">\nUnits: metric\n</file>") {
		t.Errorf("u1's new message does not start with u1's memory:\n%s", got)
	}
	if got := lastText(reqs[6]); got != "hi" {
		t.Errorf("u2's new message is %q, want u2's empty memory loaded as nothing", got)
	}
	if n := invokes.Load(); n != 4+5 {
		t.Errorf("the agent API answered %d tool calls, want the 4 memory calls and 5 loads", n)
	}
	resp, err := http.Post(apiSrv.URL+"/rpc/memory/Toolsets", "application/json", strings.NewReader("{}")) //nolint:noctx // local server
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("memory without the token: %v %v, want 401", resp, err)
	} else {
		resp.Body.Close()
	}
}

// TestMemoryConfigErrors: a bad memory section fails startup, naming it.
func TestMemoryConfigErrors(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ memory, toolsets, want string }{
		{`{"store": {"backend": "redis"}}`, "", `agent.memory.store.backend "redis"`},
		{`{"store": {"backend": "sqlite"}}`, "", "needs a path"},
		{`{"store": {"backend": "memory", "path": "x.db"}}`, "", "leave out path"},
		{`{"store": {"backend": "memory", "scope": "team"}}`, "", `scope "team"`},
		{`{"store": {"backend": "memory", "max_files": -1}}`, "", "negative limit"},
		{`{"store": {"backend": "memory"}, "extra": 1}`, "", "extra"},
		{`{"store": {"backend": "memory", "load_bytes": -2}}`, "", "load_bytes -2"},
		// A toolset with a tool of the same name.
		{`{"store": {"backend": "memory"}}`, `"mine"`, `has a tool named "memory"`},
	} {
		config := `{"mcp": {"toolsets": [{"name": "mine", "type": "memory-named"}]},
		  "agent": {"model": {"id": "m"}, "toolsets": [` + c.toolsets + `], "memory": ` + c.memory + `}}`
		_, err := startAgents(t, config, map[string]*llmtest.Fake{"m": llmtest.NewFake(t)})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("memory %s: %v, want an error with %q", c.memory, err, c.want)
		}
	}
}

// TestMemoryToolNames: the runner's copy of the memory tools' names is
// pkg/memory's.
func TestMemoryToolNames(t *testing.T) {
	t.Parallel()
	recall := []string{memory.RecallGetTool, memory.RecallSaveTool, memory.RecallSearchTool}
	if memoryToolName != memory.ToolName || !slices.Equal(recallToolNames, recall) {
		t.Errorf("names %q %q, want %q %q", memoryToolName, recallToolNames, memory.ToolName, recall)
	}
}

// TestMemoryAPI: what an agent saved through its tool, its user sees
// through GET /v1/agents/{name}/memory (files deeper than a listing goes
// counted in the usage, the limits the config's) and deletes with DELETE;
// another user sees none of it.
func TestMemoryAPI(t *testing.T) {
	t.Parallel()
	keys := ""
	for i, user := range []string{"u1", "u2"} {
		e, err := authKeyEntry(fmt.Sprintf("k%d", i+1), "acme", user, "secret-key-of-user-"+user)
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 {
			keys += ","
		}
		keys += e
	}
	config := `{"auth": {"api_keys": [` + keys + `]},
	  "agents": [{"name": "default", "model": {"id": "m"},
	    "memory": {"store": {"backend": "memory", "scope": "user", "max_files": 7}}}]}`
	fake := llmtest.NewFake(t,
		memoryCall("a", `{"command":"create","path":"/memories/prefs.md","file_text":"Units: metric\n"}`),
		memoryCall("b", `{"command":"create","path":"/memories/trips/2026/lisbon/notes.md","file_text":"May"}`),
		llmtest.Text("ok"),
	)
	s, err := startAgents(t, config, map[string]*llmtest.Fake{"m": fake})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.client(t, "secret-key-of-user-u1").Run(t.Context(), agentclient.RunRequest{Message: "remember"}); err != nil {
		t.Fatal(err)
	}
	call := func(method, path, user string) (int, wire.MemoryView) {
		req, err := http.NewRequestWithContext(t.Context(), method, s.url+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer secret-key-of-user-"+user)
		resp, err := s.ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var v wire.MemoryView
		if resp.StatusCode == http.StatusOK {
			if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
				t.Fatal(err)
			}
		}
		return resp.StatusCode, v
	}
	code, v := call("GET", "/v1/agents/default/memory", "u1")
	if code != 200 || v.Scope != "user" || v.Usage != (wire.MemoryUsage{Bytes: 17, Files: 2}) ||
		v.Limits != (wire.MemoryLimits{FileBytes: memory.DefaultMaxFileBytes, ScopeBytes: memory.DefaultMaxScopeBytes, Files: 7}) {
		t.Errorf("u1's memory = %d %+v", code, v)
	}
	if code, v := call("GET", "/v1/agents/default/memory?path=/memories/prefs.md", "u1"); code != 200 || v.Text != "Units: metric\n" {
		t.Errorf("u1's file = %d %+v", code, v)
	}
	if code, v := call("GET", "/v1/agents/default/memory", "u2"); code != 200 || v.Usage.Files != 0 || len(v.Entries) != 0 {
		t.Errorf("u2's memory = %d %+v, want empty", code, v)
	}
	if code, _ := call("DELETE", "/v1/agents/default/memory?path=/memories/trips", "u2"); code != 404 {
		t.Errorf("u2 deleting u1's directory = %d, want 404", code)
	}
	if code, _ := call("DELETE", "/v1/agents/default/memory?path=/memories", "u1"); code != 400 {
		t.Errorf("deleting the root = %d, want 400", code)
	}
	if code, _ := call("DELETE", "/v1/agents/default/memory?path=/memories/trips", "u1"); code != 204 {
		t.Errorf("u1 deleting a directory = %d, want 204", code)
	}
	if code, v := call("GET", "/v1/agents/default/memory", "u1"); code != 200 || v.Usage != (wire.MemoryUsage{Bytes: 14, Files: 1}) {
		t.Errorf("u1's memory after the delete = %d %+v", code, v)
	}
}
