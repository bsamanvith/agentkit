package main

// End-to-end test of serve behind Cloudflare Access: the built binary with
// auth.cloudflare_access from the environment and authentication required,
// against a fake team domain (an httptest server publishing its keys at
// /cdn-cgi/access/certs), driven over HTTP and MCP.

import (
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"syscall"
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

	assertion, sent := accessAssertions(t, team, kid, aud)
	good := assertion(nil)
	now := time.Now()

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
	// Paths that look like the public ones but are not never get past the
	// gate (ServeMux's redirects included).
	for _, path := range []string{"/v1/hooks/..", "/v1/hooks/.", "/v1/hooks/%2e%2e", "/v1/hooks/%2e%2e/runs", "/v1/hooks/../runs", "//healthz", "/healthz/", "/readyz/"} {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, p.url+path, strings.NewReader("{}"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("POST %s with no assertion = 200", path)
		}
	}
	if code, _ := do("HEAD", "/healthz", nil); code != http.StatusOK {
		t.Errorf("HEAD /healthz with no assertion = %d, want 200", code)
	}

	// No assertion, accepted or refused, nor a refused email, is logged
	// (at debug, the level the server runs at here). The log is read once
	// the process has ended, so it is whole.
	logs := stopServe(t, p)
	for _, a := range sent() {
		if strings.Contains(logs, a) || strings.Contains(logs, a[strings.LastIndexByte(a, '.')+1:]) {
			t.Errorf("an assertion (or its signature) appears in the logs")
		}
	}
	if strings.Contains(logs, "someone@example.com") {
		t.Errorf("a refused email appears in the logs")
	}
}

// TestE2EServeCloudflareAccessOnly: with authentication required and Access
// the only credential, serve starts, Access's users get in, and /readyz tells
// others its status alone.
func TestE2EServeCloudflareAccessOnly(t *testing.T) {
	t.Parallel()
	team := jwttest.NewIssuer(t)
	kid := team.AddKey(t, "RS256")
	const aud = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	config := `{"auth": {"required": true, "cloudflare_access": {"team_domain": "` + team.URL() + `", "aud": "` + aud + `", "emails": ["owner@example.com"]}}}`
	p := startServe(t, config, 5*time.Second, envComponents+"=agent,web", "ANTHROPIC_API_KEY=unused-in-this-test")
	assertion, _ := accessAssertions(t, team, kid, aud)
	good := assertion(nil)

	get := func(path, a string) (int, string) {
		t.Helper()
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, p.url+path, nil)
		if a != "" {
			req.Header.Set(cfaccess.Header, a)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, strings.TrimSpace(string(b))
	}
	if code, _ := get("/v1/agents", ""); code != http.StatusUnauthorized {
		t.Errorf("GET /v1/agents with no assertion = %d, want 401", code)
	}
	if code, body := get("/v1/agents", good); code != http.StatusOK {
		t.Errorf("GET /v1/agents with an assertion = %d %q", code, body)
	}
	if code, _ := get("/", good); code != http.StatusOK {
		t.Errorf("the console with an assertion = %d", code)
	}
	if code, body := get("/readyz", ""); code != http.StatusOK || body != `{"status":"ready"}` {
		t.Errorf("GET /readyz with no assertion = %d %q, want the status alone", code, body)
	}
	if code, body := get("/readyz", good); code != http.StatusOK || !strings.Contains(body, `"components"`) {
		t.Errorf("GET /readyz with an assertion = %d %q, want the components", code, body)
	}
}

// accessAssertions returns a maker of Access assertions of team for aud
// (owner@example.com, valid for an hour, claims changed by over), and the
// assertions it made so far.
func accessAssertions(t *testing.T, team *jwttest.Issuer, kid, aud string) (func(over map[string]any) string, func() []string) {
	now := time.Now()
	var mu sync.Mutex
	var made []string
	return func(over map[string]any) string {
			c := map[string]any{"iss": team.URL(), "aud": []string{aud}, "email": "owner@example.com", "sub": "u-1", "type": "app", "iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(time.Hour).Unix()}
			for k, v := range over {
				c[k] = v
			}
			a := team.Sign(t, kid, c)
			mu.Lock()
			made = append(made, a)
			mu.Unlock()
			return a
		}, func() []string {
			mu.Lock()
			defer mu.Unlock()
			return slices.Clone(made)
		}
}

// stopServe stops p with SIGTERM, waits for it to exit, and returns its
// whole log.
func stopServe(t *testing.T, p *serveProcess) string {
	t.Helper()
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
	case <-time.After(30 * time.Second):
		t.Fatal("agentkit serve did not stop within 30s")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.logs.String()
}
