package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
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
	path := filepath.Join(t.TempDir(), "agentkit.json")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadServeConfig(path)
	if err != nil {
		return nil, err
	}
	s := &memoryServer{flags: map[string]modelFlags{}}
	c := cli{
		getenv: func(string) string { return "" },
		catalog: func() (*toolset.Catalog, error) {
			return toolset.NewCatalog(toolset.Demo(), memoryNamedFactory{})
		},
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
	authn, err := c.authenticator(cfg)
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

func (memoryNamedFactory) Build(context.Context, json.RawMessage) ([]tool.Tool, error) {
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

// TestMemoryIsRefusedOnARunner: the tenant does not cross to a runner
// component yet, so an agent with memory that runs on one fails startup,
// in the runner and in an agent API that sends it runs.
func TestMemoryIsRefusedOnARunner(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "agentkit.json")
	if err := os.WriteFile(path, []byte(`{"agent": {"model": {"id": "m"}, "memory": {"store": {"backend": "memory"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadServeConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	c := cli{getenv: func(string) string { return "" }, newModel: func(modelFlags) (llm.Model, error) { return llmtest.NewFake(t), nil }}
	log := slog.New(slog.DiscardHandler)
	env := serveEnv{components: []string{runnerName}, addr: "127.0.0.1:0", shutdownTimeout: time.Second}
	if _, err := c.newRunner(t.Context(), cfg, nil, env, clock.Real(), log); err == nil || !strings.Contains(err.Error(), "agent.memory") {
		t.Errorf("runner with a memory agent: %v, want an error naming agent.memory", err)
	}
	env = serveEnv{components: []string{agentapi.Name}, addr: "127.0.0.1:0", shutdownTimeout: time.Second, runnerURL: "http://127.0.0.1:1"}
	if _, _, err := c.newAgents(t.Context(), cfg, nil, nil, env, nil, scoped.Memory(nil), nil, clock.Real(), log); err == nil || !strings.Contains(err.Error(), "agent.memory") {
		t.Errorf("agent API on a runner with a memory agent: %v, want an error naming agent.memory", err)
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
