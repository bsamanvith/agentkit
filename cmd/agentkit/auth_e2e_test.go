package main

// End-to-end test of serve's authentication (T040): the built binary with API
// keys from the config, from $AGENTKIT_API_KEYS_FILE and $AGENTKIT_API_TOKEN,
// driven over HTTP and MCP.

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/auth/authtest"
)

func TestE2EServeAuth(t *testing.T) {
	t.Parallel()
	const (
		narrow = "narrow-key-0123456789abcd" // the config's: toolset demo-a
		wide   = "wide-key-0123456789abcdef" // the file's: demo-*
		token  = "the-api-token-0123456789a" // $AGENTKIT_API_TOKEN: everything
	)
	keysFile := filepath.Join(t.TempDir(), "keys.json")
	if err := os.WriteFile(keysFile, []byte(`{"api_keys":[{"id":"wide","hash":"`+auth.HashKey(wide)+`","tenant":"t2","scopes":["*"],"toolsets":["demo-*"]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	config := `{
  "mcp": {"toolsets": [
    {"name": "demo-a", "type": "demo", "settings": {"echo_prefix": "[a] "}},
    {"name": "demo-b", "type": "demo"},
    {"name": "other", "type": "demo"}
  ]},
  "agents": [{"name": "a", "toolsets": ["demo-a"]}, {"name": "ab", "toolsets": ["demo-a", "demo-b"], "prefix_tool_names": true}],
  "auth": {"api_keys": [
    {"id": "narrow", "hash": "` + auth.HashKey(narrow) + `", "tenant": "t1", "user": "ann", "scopes": ["agent:read"], "toolsets": ["demo-a"]}
  ]}
}`
	p := startServe(t, config, 5*time.Second,
		envComponents+"=mcp-server,agent", envAPIKeysFile+"="+keysFile, envAPIToken+"="+token, "ANTHROPIC_API_KEY=unused-in-this-test")

	get := func(path, key string) (int, string, string) {
		t.Helper()
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, p.url+path, nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get("Content-Type"), string(b)
	}
	names := func(body, field string) []string {
		t.Helper()
		var v map[string]json.RawMessage
		var list []struct{ Name string }
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			t.Fatalf("%v in %s", err, body)
		}
		if err := json.Unmarshal(v[field], &list); err != nil {
			t.Fatalf("%s: %v in %s", field, err, body)
		}
		var out []string
		for _, x := range list {
			out = append(out, x.Name)
		}
		return out
	}

	for _, path := range []string{"/healthz", "/readyz", "/version"} {
		if code, _, _ := get(path, ""); code != http.StatusOK {
			t.Errorf("GET %s with no credentials = %d, want 200", path, code)
		}
	}
	for _, path := range []string{"/mcp/", "/mcp/demo-a", "/mcp/nope", "/v1/agents", "/v1/nope"} {
		for _, key := range []string{"", "not-a-key-0123456789"} {
			if code, _, _ := get(path, key); code != http.StatusUnauthorized {
				t.Errorf("GET %s with key %q = %d, want 401", path, key, code)
			}
		}
	}

	// Each key lists its toolsets, over MCP and in the API.
	for key, want := range map[string][]string{narrow: {"demo-a"}, wide: {"demo-a", "demo-b"}, token: {"demo-a", "demo-b", "other"}} {
		_, _, body := get("/mcp/", key)
		if got := names(body, "toolsets"); !slices.Equal(got, want) {
			t.Errorf("GET /mcp/ as %s = %v, want %v", key[:6], got, want)
		}
		_, _, body = get("/v1/toolsets", key)
		if got := names(body, "toolsets"); !slices.Equal(got, want) {
			t.Errorf("GET /v1/toolsets as %s = %v, want %v", key[:6], got, want)
		}
	}
	_, _, body := get("/v1/agents", narrow)
	if got := names(body, "agents"); !slices.Equal(got, []string{"a"}) {
		t.Errorf("agents of the narrow key = %v, want [a]", got)
	}

	// The narrow key calls demo-a's tools; demo-b is, to it, a toolset that
	// does not exist.
	hc := &http.Client{Transport: bearer{narrow, http.DefaultTransport}}
	if got, err := echo(t, connectMCP(t, p.url+"/mcp/demo-a", hc), "hi"); err != nil || got != "[a] hi" {
		t.Errorf("echo on demo-a = %q, %v", got, err)
	}
	post := func(path string) (int, string, string) {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, p.url+path, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"text":"x"}}}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get("Content-Type"), string(b)
	}
	hs, hct, hb := post("/mcp/demo-b")
	us, uct, ub := post("/mcp/nope")
	if hs != http.StatusNotFound || hs != us || hct != uct || hb != ub {
		t.Errorf("hidden /mcp/demo-b = %d %q %q, unknown = %d %q %q: want the same 404", hs, hct, hb, us, uct, ub)
	}

	// The narrow key only reads.
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, p.url+"/v1/runs", strings.NewReader(`{"message":"hi","agent":"a"}`))
	req.Header.Set("Authorization", "Bearer "+narrow)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("a run with the read-only key = %d, want 403", resp.StatusCode)
	}

	p.mu.Lock()
	logs := p.logs.String()
	p.mu.Unlock()
	for _, key := range []string{narrow, wide, token} {
		if authtest.Leaks(logs, key) {
			t.Errorf("key %s... is in the log", key[:6])
		}
	}
}
