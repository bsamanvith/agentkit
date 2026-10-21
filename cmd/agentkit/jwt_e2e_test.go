package main

// End-to-end test of serve with an identity provider's tokens (T041): the
// built binary with auth.jwt beside an API key, driven over HTTP and MCP,
// against a fake provider.

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/auth/authtest"
	"github.com/bsamanvith/agentkit/pkg/auth/jwt/jwttest"
)

func TestE2EServeJWT(t *testing.T) {
	t.Parallel()
	iss := jwttest.NewIssuer(t)
	kid := iss.AddKey(t, "RS256")

	// The audience is the server's origin, so its port is chosen first.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	origin := "http://" + addr

	const key = "e2e-key-0123456789abcdef" // an API key beside the tokens
	config := `{
  "mcp": {"toolsets": [
    {"name": "demo-a", "type": "demo", "settings": {"echo_prefix": "[a] "}},
    {"name": "demo-b", "type": "demo"}
  ]},
  "auth": {
    "api_keys": [{"id": "ci", "hash": "` + auth.HashKey(key) + `", "tenant": "t2", "scopes": ["*"], "toolsets": ["*"]}],
    "jwt": {"issuer": "` + iss.URL() + `", "audience": "` + origin + `", "leeway": "30s", "scopes_supported": ["agent:read", "mcp:demo-a", "mcp:demo-b"]}
  }
}`
	p := startServe(t, config, 5*time.Second,
		envComponents+"=mcp-server,agent", envAddr+"="+addr, "ANTHROPIC_API_KEY=unused-in-this-test")
	if p.url != origin {
		t.Fatalf("serving on %s, want %s", p.url, origin)
	}

	now := time.Now()
	claims := func(over map[string]any) map[string]any {
		c := map[string]any{"iss": iss.URL(), "aud": origin, "sub": "ann", "tenant": "t1", "scope": "agent:read mcp:demo-a", "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()}
		for k, v := range over {
			c[k] = v
		}
		return c
	}
	tok := iss.Sign(t, kid, claims(nil))
	// What an MCP client gets when it asks for the endpoint's own URL
	// (RFC 8707).
	endpointTok := iss.Sign(t, kid, claims(map[string]any{"aud": origin + "/mcp/demo-a"}))
	wrongAud := iss.Sign(t, kid, claims(map[string]any{"aud": "https://elsewhere.example"}))

	do := func(method, path, bearer string) (*http.Response, string) {
		t.Helper()
		var body io.Reader
		if method == http.MethodPost {
			body = strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
		}
		req, _ := http.NewRequestWithContext(t.Context(), method, p.url+path, body)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	metadata := func(url string) map[string]any {
		t.Helper()
		resp, err := http.Get(url)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&m); err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %d, %v", url, resp.StatusCode, err)
		}
		return m
	}

	// The metadata of the server, open to all.
	m := metadata(origin + "/.well-known/oauth-protected-resource")
	if m["resource"] != origin || !slices.Equal(anyStrings(m["authorization_servers"]), []string{iss.URL()}) ||
		!slices.Equal(anyStrings(m["scopes_supported"]), []string{"agent:read", "mcp:demo-a", "mcp:demo-b"}) {
		t.Errorf("the server's metadata: %v", m)
	}

	// A 401 from an endpoint points at that endpoint's metadata.
	challenge := regexp.MustCompile(`resource_metadata="([^"]+)"`)
	for _, bearer := range []string{"", wrongAud} {
		resp, _ := do(http.MethodPost, "/mcp/demo-a", bearer)
		h := resp.Header.Get("WWW-Authenticate")
		match := challenge.FindStringSubmatch(h)
		if resp.StatusCode != http.StatusUnauthorized || match == nil || match[1] != origin+"/.well-known/oauth-protected-resource/mcp/demo-a" {
			t.Fatalf("POST /mcp/demo-a with %d-byte token: %d %q", len(bearer), resp.StatusCode, h)
		}
		if bearer != "" && !strings.Contains(h, `error="invalid_token"`) {
			t.Errorf("a token for another audience: %q", h)
		}
		if m := metadata(match[1]); m["resource"] != origin+"/mcp/demo-a" {
			t.Errorf("the endpoint's metadata: %v", m)
		}
	}
	// The agent API answers 401 to a bad token too.
	if resp, _ := do(http.MethodGet, "/v1/toolsets", wrongAud); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET /v1/toolsets with a token for another audience: %d", resp.StatusCode)
	}

	// The token's scopes grant demo-a only, over MCP and in the API; the API
	// key, beside it, still grants everything.
	for name, c := range map[string]struct {
		bearer string
		want   []string
	}{"token": {tok, []string{"demo-a"}}, "endpoint token": {endpointTok, []string{"demo-a"}}, "api key": {key, []string{"demo-a", "demo-b"}}} {
		for _, path := range []string{"/mcp/", "/v1/toolsets"} {
			if name == "endpoint token" && path != "/mcp/" {
				continue // its audience is /mcp/demo-a: the listing is outside it
			}
			resp, body := do(http.MethodGet, path, c.bearer)
			var v struct{ Toolsets []struct{ Name string } }
			_ = json.Unmarshal([]byte(body), &v)
			var got []string
			for _, ts := range v.Toolsets {
				got = append(got, ts.Name)
			}
			if name == "endpoint token" {
				if resp.StatusCode != http.StatusUnauthorized {
					t.Errorf("GET %s with a token for /mcp/demo-a: %d, want 401", path, resp.StatusCode)
				}
				continue
			}
			if resp.StatusCode != http.StatusOK || !slices.Equal(got, c.want) {
				t.Errorf("GET %s with the %s: %d %v, want %v", path, name, resp.StatusCode, got, c.want)
			}
		}
	}
	for _, bearer := range []string{tok, endpointTok} {
		hc := &http.Client{Transport: bearerTransport(bearer)}
		if got, err := echo(t, connectMCP(t, p.url+"/mcp/demo-a", hc), "hi"); err != nil || got != "[a] hi" {
			t.Errorf("echo on demo-a with a token = %q, %v", got, err)
		}
	}
	hidden, hb := do(http.MethodPost, "/mcp/demo-b", tok)
	unknown, ub := do(http.MethodPost, "/mcp/nope", tok)
	if hidden.StatusCode != http.StatusNotFound || hidden.StatusCode != unknown.StatusCode || hb != ub {
		t.Errorf("demo-b with a token for demo-a: %d %q; unknown: %d %q", hidden.StatusCode, hb, unknown.StatusCode, ub)
	}

	p.mu.Lock()
	logs := p.logs.String()
	p.mu.Unlock()
	for _, secret := range []string{tok, endpointTok, wrongAud, key} {
		if authtest.Leaks(logs, secret) {
			t.Errorf("a credential is in the log")
		}
	}
}

func anyStrings(v any) []string {
	l, _ := v.([]any)
	var out []string
	for _, x := range l {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

func bearerTransport(token string) http.RoundTripper { return bearer{token, http.DefaultTransport} }
