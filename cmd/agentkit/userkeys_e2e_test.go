//go:build !noagent

package main

// End-to-end test of per-user search keys (T047, T048): Exa as the split
// deployment runs it, its toolset on a toolsets component and the agent API
// calling it over /rpc/, one credential vault behind both (in a deployment,
// the shared Postgres). Each user connects their own Exa key through the
// credentials API, and the agent's exa_search calls Exa with that user's
// key; a user who has connected none is told so, and the run goes on.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/credentials/memory"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/store/scoped"
)

func TestUsersConnectTheirOwnExaKey(t *testing.T) {
	t.Parallel()
	// A fake Exa that echoes the key it got in the result's text.
	var mu sync.Mutex
	var sent []string
	exaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k := r.Header.Get("x-api-key")
		mu.Lock()
		sent = append(sent, k)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results": [{"url": "https://a.example/", "title": "t", "highlights": ["ok, key `+k+`"]}]}`)
	}))
	t.Cleanup(exaSrv.Close)

	var keys []string
	for _, u := range []string{"ann", "bob"} {
		e, _ := json.Marshal(auth.APIKey{ID: u, Hash: auth.HashKey(keyOf(u)), Tenant: "acme", User: u, Scopes: []string{"*"}, Toolsets: []string{"*"}})
		keys = append(keys, string(e))
	}
	config := fmt.Sprintf(`{
	  "mcp": {"toolsets": [
	    {"name": "exa", "type": "exa", "settings": {"base_url": %q}, "credential": {"slot": "exa", "scope": "user"}}
	  ]},
	  "agents": [{"name": "default", "model": {"id": "fake"}, "toolsets": ["exa"]}],
	  "auth": {"api_keys": [%s]}
	}`, exaSrv.URL, strings.Join(keys, ","))
	path := filepath.Join(t.TempDir(), "agentkit.json")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadServeConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	store := memory.New()
	sealer := credentials.NewSealer(testRing(t))
	parts := &vaultParts{vault: credentials.NewVault(store, sealer), store: store, sealer: sealer}
	log := slog.New(slog.DiscardHandler)
	model := llmtest.NewFake(t,
		llmtest.ToolCalls(llm.ToolUse{ID: "c1", Name: "exa_search", Input: json.RawMessage(`{"query": "bikes"}`)}), llmtest.Text("done"),
		llmtest.ToolCalls(llm.ToolUse{ID: "c2", Name: "exa_search", Input: json.RawMessage(`{"query": "bikes"}`)}), llmtest.Text("done"),
	)
	c := cli{getenv: func(string) string { return "" }, newModel: func(modelFlags) (llm.Model, error) { return model, nil }}

	// The toolsets component: it builds the exa toolset over the vault.
	tenv := serveEnv{components: []string{toolsetsName}, addr: "127.0.0.1:0", vault: parts.vault, vaultParts: parts}
	built, problems, err := c.buildToolsets(t.Context(), tenv, cfg, path, log)
	if err != nil || len(problems) != 0 {
		t.Fatalf("toolsets: buildToolsets: %v, %v", problems, err)
	}
	tm, err := c.newToolsets(cfg, built, problems, tenv, log)
	if err != nil {
		t.Fatal(err)
	}
	toolsSrv := httptest.NewServer(tm.Component.Handler())
	t.Cleanup(toolsSrv.Close)

	// The agent API, with its toolsets on that component.
	env := serveEnv{
		components: []string{agentapi.Name}, addr: "127.0.0.1:0", shutdownTimeout: time.Second,
		toolsetsURL: toolsSrv.URL, dependencyWait: 10 * time.Second, vault: parts.vault, vaultParts: parts,
	}
	built, problems, err = c.buildToolsets(t.Context(), env, cfg, path, log)
	if err != nil || len(problems) != 0 {
		t.Fatalf("api: buildToolsets: %v, %v", problems, err)
	}
	if _, remote := built["exa"].(remoteToolset); !remote {
		t.Fatalf("exa is %T, want the toolsets component's", built["exa"])
	}
	authn, err := c.authenticator(t.Context(), cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	st, closeStore, err := scoped.Open(t.Context(), "sqlite:"+filepath.Join(t.TempDir(), "agentkit.db"), clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	am, _, err := c.newAgents(t.Context(), cfg, built, problems, env, authn, st, nil, clock.Real(), log)
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(am.Component.Handler())
	t.Cleanup(func() {
		api.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = am.Component.Shutdown(ctx)
		_ = closeStore()
	})

	call := func(user, method, path, body string) (int, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, api.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+keyOf(user))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	const annKey = "exa-key-of-ann-0c5e9d"
	if code, body := call("ann", http.MethodPut, "/v1/credentials/exa", `{"kind":"api_key","api_key":"`+annKey+`"}`); code != http.StatusOK {
		t.Fatalf("ann connects her key: %d %s", code, body)
	}
	if code, body := call("ann", http.MethodGet, "/v1/credentials", ""); code != http.StatusOK || !strings.Contains(body, `"connected"`) || strings.Contains(body, annKey) {
		t.Errorf("ann's credentials: %d %s, want exa connected and no value", code, body)
	}
	if code, body := call("bob", http.MethodGet, "/v1/toolsets", ""); code != http.StatusOK || !strings.Contains(body, `"slot":"exa"`) || !strings.Contains(body, `"not_connected"`) {
		t.Errorf("bob's toolsets: %d %s, want exa's slot, not connected for him", code, body)
	}

	run := func(user string) wire.Event {
		t.Helper()
		ac, err := agentclient.New(api.URL, agentclient.WithHTTPClient(&http.Client{Transport: bearer{token: keyOf(user), base: http.DefaultTransport}}))
		if err != nil {
			t.Fatal(err)
		}
		s, err := ac.Stream(t.Context(), agentclient.RunRequest{Message: "search bikes"})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		var result *wire.Event
		var last wire.Event
		for {
			e, err := s.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatalf("stream: %v", err)
			}
			if e.Type == wire.TypeToolResult {
				result = &e
			}
			last = e
		}
		if last.Type != wire.TypeRunDone {
			t.Errorf("%s's run ended with %+v", user, last)
		}
		if result == nil {
			t.Fatalf("%s's run had no tool result", user)
		}
		return *result
	}
	if r := run("ann"); r.IsError || !strings.Contains(r.Content, "[redacted]") || strings.Contains(r.Content, annKey) {
		t.Errorf("ann's search: %+v, want results with her key redacted", r)
	}
	if r := run("bob"); !r.IsError || !strings.Contains(r.Content, "exa is not connected for this account") {
		t.Errorf("bob's search: %+v, want exa's not-connected error", r)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(sent, ",") != annKey {
		t.Errorf("Exa got keys %q, want only ann's, once", sent)
	}
}
