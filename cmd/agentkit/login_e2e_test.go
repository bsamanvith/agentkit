package main

// End-to-end test of local users (T061): the built binary hashes the
// passwords (agentkit users hash-password), serves with users from the config
// and from $AGENTKIT_USERS_FILE, and is driven over HTTP as the console
// would: sign in, ask who it is, post with the CSRF token, sign out.

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/auth/authtest"
)

func TestE2EServeLogin(t *testing.T) {
	t.Parallel()
	const (
		annPassword  = "ann-CANARY-e2e-pw-91c3"
		rootPassword = "root-CANARY-e2e-pw-27d8"
		token        = "the-api-token-0123456789a"
	)
	hash := func(pw string) string {
		t.Helper()
		cmd := exec.Command(binary(t), "users", "hash-password")
		cmd.Env = []string{"HOME=" + t.TempDir()}
		cmd.Stdin = strings.NewReader(pw + "\n")
		out, err := cmd.Output()
		if err != nil || strings.Contains(string(out), pw) {
			t.Fatalf("agentkit users hash-password: %v %q", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	usersFile := filepath.Join(t.TempDir(), "users.json")
	if err := os.WriteFile(usersFile, []byte(`{"users":[{"username":"root","password_hash":"`+hash(rootPassword)+`","tenant":"acme","roles":["admin"]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	config := `{
  "mcp": {"toolsets": [{"name": "demo", "type": "demo"}]},
  "agents": [{"name": "a", "system": "E2E-SYSTEM-PROMPT", "toolsets": ["demo"]}],
  "auth": {"users": [{"username": "ann", "password_hash": "` + hash(annPassword) + `", "tenant": "acme", "roles": ["user"]}]}
}`
	p := startServe(t, config, 5*time.Second,
		envComponents+"=agent,mcp-server", envUsersFile+"="+usersFile, envAPIToken+"="+token, "ANTHROPIC_API_KEY=unused-in-this-test")

	var seen strings.Builder // every response, for the canary search
	do := func(method, path, body string, header ...string) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequestWithContext(t.Context(), method, p.url+path, strings.NewReader(body))
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		seen.WriteString(strings.Join(resp.Header.Values("Set-Cookie"), "\n") + "\n" + string(b) + "\n")
		return resp, string(b)
	}
	if resp, body := do("GET", "/v1/auth/config", ""); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"methods":["password","token"]`) {
		t.Errorf("GET /v1/auth/config = %d %s", resp.StatusCode, body)
	}
	type session struct{ cookie, csrf string }
	signIn := func(user, pw string) session {
		t.Helper()
		resp, body := do("POST", "/v1/auth/login", `{"username":"`+user+`","password":"`+pw+`"}`, "Origin", p.url)
		cs := resp.Cookies()
		if resp.StatusCode != http.StatusNoContent || len(cs) != 1 || !cs[0].HttpOnly || cs[0].SameSite != http.SameSiteStrictMode {
			t.Fatalf("signing in %s = %d %s %v", user, resp.StatusCode, body, cs)
		}
		s := session{cookie: cs[0].Name + "=" + cs[0].Value}
		_, body = do("GET", "/v1/auth/me", "", "Cookie", s.cookie)
		var me wire.Me
		if err := json.Unmarshal([]byte(body), &me); err != nil || me.Username != user || me.Tenant != "acme" || me.CSRF == "" {
			t.Fatalf("/v1/auth/me as %s: %s", user, body)
		}
		s.csrf = me.CSRF
		return s
	}
	wrong, wrongBody := do("POST", "/v1/auth/login", `{"username":"ann","password":"`+rootPassword+`"}`)
	unknown, unknownBody := do("POST", "/v1/auth/login", `{"username":"nobody","password":"`+annPassword+`"}`)
	if wrong.StatusCode != http.StatusUnauthorized || unknown.StatusCode != wrong.StatusCode || wrongBody != unknownBody {
		t.Errorf("wrong password = %d %s, unknown user = %d %s: want the same 401", wrong.StatusCode, wrongBody, unknown.StatusCode, unknownBody)
	}
	ann, root := signIn("ann", annPassword), signIn("root", rootPassword)

	// Unsafe requests need the CSRF token and Origin; the token's don't.
	if resp, body := do("POST", "/v1/conversations", `{}`, "Cookie", ann.cookie); resp.StatusCode != http.StatusForbidden || !strings.Contains(body, wire.CodeCSRF) {
		t.Errorf("a POST with only the cookie = %d %s, want 403 %s", resp.StatusCode, body, wire.CodeCSRF)
	}
	if resp, body := do("POST", "/v1/conversations", `{}`, "Cookie", ann.cookie, "Origin", p.url, "X-Agentkit-CSRF", ann.csrf); resp.StatusCode != http.StatusCreated {
		t.Errorf("a POST with the token = %d %s, want 201", resp.StatusCode, body)
	}
	if resp, body := do("POST", "/v1/conversations", `{}`, "Authorization", "Bearer "+token); resp.StatusCode != http.StatusCreated {
		t.Errorf("a bearer POST = %d %s, want 201", resp.StatusCode, body)
	}

	// Roles: the system prompt is for admins.
	if _, body := do("GET", "/v1/agents/a", "", "Cookie", ann.cookie); strings.Contains(body, "E2E-SYSTEM-PROMPT") {
		t.Errorf("a user sees the system prompt: %s", body)
	}
	if _, body := do("GET", "/v1/agents/a", "", "Cookie", root.cookie); !strings.Contains(body, "E2E-SYSTEM-PROMPT") {
		t.Errorf("an admin does not see the system prompt: %s", body)
	}

	// Sessions are for the agent API, not MCP.
	if resp, _ := do("GET", "/mcp/", "", "Cookie", root.cookie); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET /mcp/ with a session = %d, want 401", resp.StatusCode)
	}

	// Logout ends the session on the server.
	if resp, body := do("POST", "/v1/auth/logout", "", "Cookie", ann.cookie, "Origin", p.url, "X-Agentkit-CSRF", ann.csrf); resp.StatusCode != http.StatusNoContent {
		t.Errorf("logout = %d %s", resp.StatusCode, body)
	}
	if resp, _ := do("GET", "/v1/auth/me", "", "Cookie", ann.cookie); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("/v1/auth/me after logout = %d, want 401", resp.StatusCode)
	}
	if resp, _ := do("GET", "/v1/auth/me", "", "Cookie", root.cookie); resp.StatusCode != http.StatusOK {
		t.Errorf("another session after ann's logout = %d, want 200", resp.StatusCode)
	}

	p.mu.Lock()
	logs := p.logs.String()
	p.mu.Unlock()
	for _, s := range []string{annPassword, rootPassword, token} {
		if authtest.Leaks(logs, s) || authtest.Leaks(seen.String(), s) {
			t.Errorf("a secret (%s...) is in the log or a response", s[:6])
		}
	}
	for _, s := range []session{ann, root} {
		_, id, _ := strings.Cut(s.cookie, "=")
		if authtest.Leaks(logs, id) {
			t.Errorf("a session id is in the log")
		}
	}
	if !slices.ContainsFunc(strings.Split(logs, "\n"), func(l string) bool { return strings.Contains(l, "login: failed") }) {
		t.Errorf("the failed sign-in is not logged")
	}
}
