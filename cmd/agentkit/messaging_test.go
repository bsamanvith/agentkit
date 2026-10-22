//go:build !noagent

package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/agentmsg"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/store/memory"
)

const messagingServeConfig = `{
  "agents": [
    {"name": "boss", "description": "Delegates.", "model": {"id": "model-boss"}, "can_message": ["worker"]},
    {"name": "worker", "description": "Does the work.", "model": {"id": "model-worker"}}
  ],
  "agent_api": {"messaging": {"max_hops": 4}}
}`

// buildMessaging builds the agent component of config as serve does, on a
// memory store, with fake models by model ID.
func buildMessaging(t *testing.T, config string, models map[string]*llmtest.Fake, env serveEnv) (*agentComponent, *memory.Store, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agentkit.json")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadServeConfig(path)
	if err != nil {
		return nil, nil, err
	}
	c := cli{
		getenv: func(string) string { return "" },
		newModel: func(f modelFlags) (llm.Model, error) {
			return models[f.model], nil
		},
	}
	log := slog.New(slog.DiscardHandler)
	built, problems, err := c.buildToolsets(t.Context(), env, cfg, path, log)
	if err != nil {
		t.Fatal(err)
	}
	st := memory.New()
	m, _, err := c.newAgents(t.Context(), cfg, built, problems, env, nil, st, nil, clock.Real(), log)
	if err != nil {
		return nil, nil, err
	}
	ac := m.Component.(*agentComponent)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = ac.Shutdown(ctx)
	})
	return ac, st, nil
}

// TestServeAgentMessaging: an agent with can_message gets the messaging
// tools (one without gets none), its limits come from agent_api.messaging,
// and with serve's dispatcher running, a chat run of it that delegates with
// wait gets the other agent's answer in the same run.
func TestServeAgentMessaging(t *testing.T) {
	t.Parallel()
	send, _ := json.Marshal(map[string]any{"to": "agent:worker", "text": "what is 6*7?", "wait": true, "timeout": 30})
	boss := llmtest.NewFake(t, llmtest.ToolCalls(llm.ToolUse{ID: "s1", Name: agentmsg.MessageToolName, Input: send}), llmtest.Text("the worker says 42"))
	worker := llmtest.NewFake(t, llmtest.Text("42"))
	env := serveEnv{components: []string{agentapi.Name}, addr: "127.0.0.1:0", shutdownTimeout: time.Second}
	ac, st, err := buildMessaging(t, messagingServeConfig, map[string]*llmtest.Fake{"model-boss": boss, "model-worker": worker}, env)
	if err != nil {
		t.Fatal(err)
	}
	if ac.messenger == nil || ac.messenger.Limits().MaxHops != 4 {
		t.Fatalf("messenger = %v, want one with max_hops 4", ac.messenger)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- runMessaging(ctx, ac.messenger, memory.Admin(st), ac.Server, slog.New(slog.DiscardHandler))
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("runMessaging: %v", err)
		}
	})
	ts := httptest.NewServer(ac.Handler())
	t.Cleanup(ts.Close)
	c, err := agentclient.New(ts.URL, agentclient.WithHTTPClient(ts.Client()))
	if err != nil {
		t.Fatal(err)
	}
	run, err := c.Run(t.Context(), agentclient.RunRequest{Message: "ask the worker", Agent: "boss"})
	if err != nil || run.Answer != "the worker says 42" {
		t.Fatalf("boss's run = %+v, %v", run, err)
	}
	reqs := boss.Requests()
	if got := toolNames(reqs[0]); !slices.Equal(got, []string{"agent_message", "agents_list", "thread_read"}) {
		t.Errorf("boss's tools = %v, want the messaging tools", got)
	}
	if got := toolNames(worker.Requests()[0]); len(got) != 0 {
		t.Errorf("worker's tools = %v, want none", got)
	}
	last := reqs[len(reqs)-1].Messages
	result, _ := json.Marshal(last[len(last)-1])
	if !strings.Contains(string(result), `\"status\":\"replied\"`) || !strings.Contains(string(result), "42") {
		t.Errorf("boss's tool result = %s, want the reply", result)
	}
}

// TestServeAgentMessagingRefused: can_message naming no agent fails
// startup, as does a negative limit or an agent on a remote runner, which
// cannot learn its sender.
func TestServeAgentMessagingRefused(t *testing.T) {
	t.Parallel()
	env := serveEnv{components: []string{agentapi.Name}, addr: "127.0.0.1:0", shutdownTimeout: time.Second}
	for name, tc := range map[string]struct {
		config, want string
		env          serveEnv
	}{
		"unknown agent": {`{"agents": [{"name": "a", "model": {"id": "m"}, "can_message": ["nobody"]}]}`, "can_message", env},
		"toolsets": {`{"agents": [{"name": "a", "model": {"id": "m"}, "can_message": ["b"]},
			{"name": "b", "model": {"id": "m"}, "runtime_toolsets": ["github"]}]}`, `uses toolset "github"`, env},
		"negative": {`{"agents": [{"name": "a", "model": {"id": "m"}, "can_message": ["a"]}], "agent_api": {"messaging": {"max_hops": -1}}}`, "negative", env},
		"runner": {`{"agents": [{"name": "a", "model": {"id": "m"}, "can_message": ["a"]}]}`, "cannot message", func() serveEnv {
			e := env
			e.runnerURL = "http://127.0.0.1:1"
			return e
		}()},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := buildMessaging(t, tc.config, map[string]*llmtest.Fake{"m": llmtest.NewFake(t)}, tc.env)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one saying %q", err, tc.want)
			}
		})
	}
}
