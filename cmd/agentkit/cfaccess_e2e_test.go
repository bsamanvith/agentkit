package main

// End-to-end test of serve behind Cloudflare Access: the built binary with
// auth.cloudflare_access from the environment and authentication required,
// against a fake team domain (an httptest server publishing its keys at
// /cdn-cgi/access/certs), driven over HTTP and MCP.

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/auth/cfaccess"
	"github.com/bsamanvith/agentkit/pkg/auth/jwt/jwttest"
)

func TestE2EServeCloudflareAccess(t *testing.T) {
	t.Parallel()
	team := jwttest.NewIssuer(t)
	kid := team.AddKey(t, "RS256")
	const (
		aud = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
		key = "e2e-key-0123456789abcdef" // an API key beside Access
	)
	config := `{
  "mcp": {"toolsets": [{"name": "demo", "type": "demo"}]},
  "auth": {
    "api_keys": [{"id": "ci", "hash": "` + auth.HashKey(key) + `", "tenant": "t2", "scopes": ["*"], "toolsets": ["*"]}],
    "cloudflare_access": {"emails": ["owner@example.com"]}
  }
}`
	p := startServe(t, config, 5*time.Second,
		envComponents+"=mcp-server,agent", envRequireAuth+"=1",
		envCFAccessTeamDomain+"="+team.URL(), envCFAccessAUD+"="+aud,
		"ANTHROPIC_API_KEY=unused-in-this-test")

	now := time.Now()
	assertion := func(over map[string]any) string {
		c := map[string]any{"iss": team.URL(), "aud": []string{aud}, "email": "owner@example.com", "sub": "u-1", "type": "app", "iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(time.Hour).Unix()}
		for k, v := range over {
			c[k] = v
		}
		return team.Sign(t, kid, c)
	}
	good := assertion(nil)

	do := func(method, path string, header map[string]string) (int, string) {
		t.Helper()
		var body io.Reader
		if method == http.MethodPost {
			body = strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
		}
		req, _ := http.NewRequestWithContext(t.Context(), method, p.url+path, body)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		for k, v := range header {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	cf := func(a string) map[string]string { return map[string]string{cfaccess.Header: a} }

	for _, c := range []struct {
		name, method, path string
		header             map[string]string
		want               int
	}{
		{"liveness, open", "GET", "/healthz", nil, http.StatusOK},
		{"readiness, open", "GET", "/readyz", nil, http.StatusOK},
		{"version, behind Access", "GET", "/version", nil, http.StatusUnauthorized},
		{"no assertion", "GET", "/v1/agents", nil, http.StatusUnauthorized},
		{"an API key alone", "GET", "/v1/agents", map[string]string{"Authorization": "Bearer " + key}, http.StatusUnauthorized},
		{"wrong aud", "GET", "/v1/agents", cf(assertion(map[string]any{"aud": []string{"another-app"}})), http.StatusUnauthorized},
		{"expired", "GET", "/v1/agents", cf(assertion(map[string]any{"exp": now.Add(-time.Hour).Unix()})), http.StatusUnauthorized},
		{"another email", "GET", "/v1/agents", cf(assertion(map[string]any{"email": "someone@example.com"})), http.StatusUnauthorized},
		{"valid", "GET", "/v1/agents", cf(good), http.StatusOK},
		{"valid, and an API key", "GET", "/v1/agents", map[string]string{cfaccess.Header: good, "Authorization": "Bearer " + key}, http.StatusOK},
		{"MCP, valid", "POST", "/mcp/demo", cf(good), http.StatusOK},
		{"MCP, no assertion", "POST", "/mcp/demo", nil, http.StatusUnauthorized},
		{"cross-site POST", "POST", "/mcp/demo", map[string]string{cfaccess.Header: good, "Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		// Inbound webhooks bypass Access (their signature is their auth):
		// the API answers, not the gate.
		{"webhook, no assertion", "POST", "/v1/hooks/trg_nope", nil, http.StatusNotFound},
		// Not mounted without a credential vault: the mux's 404, past the gate.
		{"OAuth callback, no assertion", "GET", "/oauth/callback", nil, http.StatusNotFound},
	} {
		got, body := do(c.method, c.path, c.header)
		if got != c.want {
			t.Errorf("%s: %s %s = %d %q, want %d", c.name, c.method, c.path, got, body, c.want)
		}
	}

	// The assertion's email is the user: GET /v1/auth/me says so.
	if code, body := do("GET", "/v1/auth/me", cf(good)); code != http.StatusOK || !strings.Contains(body, "owner@example.com") {
		t.Errorf("GET /v1/auth/me = %d %q, want the owner's email", code, body)
	}
	p.mu.Lock()
	logs := p.logs.String()
	p.mu.Unlock()
	if strings.Contains(logs, good) {
		t.Errorf("an assertion appears in the logs")
	}
}
