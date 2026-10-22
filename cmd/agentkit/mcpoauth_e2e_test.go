//go:build !noagent && !nomcp

package main

// End-to-end test of MCP OAuth for toolsets added at run time (T095), wired
// as serve wires it: a user adds an MCP server behind an authorization
// server with auth "oauth" and scope "user", connects through the agent
// API's connect flow (the browser keeps the binding cookie), reviews its
// tools, and the agent then calls them as each user, with each user's own
// token; a user who has not connected gets "not connected". No log line,
// API response, event or stored row holds a token (E07's canary).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/credentials/memory"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/mcp/oauth"
	"github.com/bsamanvith/agentkit/pkg/mcp/oauth/oauthtest"
	"github.com/bsamanvith/agentkit/pkg/store/scoped"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

func TestMCPOAuthE2E(t *testing.T) {
	t.Parallel()
	r := &credRig{logs: &syncLog{}, dbDir: t.TempDir()}

	// The MCP server: whoami answers with a hash of the bearer token.
	as := oauthtest.NewAuthServer(t, oauthtest.AuthServerOptions{DCR: true, Scopes: []string{"crm.read", "offline_access"}, TTL: time.Hour})
	s := mcp.NewServer(&mcp.Implementation{Name: "crm", Version: "1"}, nil)
	s.AddTool(&mcp.Tool{Name: "whoami", Description: "Who the token is.", InputSchema: map[string]any{"type": "object"}},
		func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			tok, _ := strings.CutPrefix(req.Extra.Header.Get("Authorization"), "Bearer ")
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "token " + shortHash(tok)}}}, nil
		})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{Stateless: true})
	rs := oauthtest.Protect(t, as, "/mcp", h, oauthtest.ResourceOptions{Scopes: []string{"crm.read"}})
	as.SetResource(rs.URL + "/mcp")
	t.Cleanup(rs.CloseClientConnections)

	// The public URL: the callback and the client's metadata document.
	public := http.NotFoundHandler()
	pub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { public.ServeHTTP(w, req) }))
	t.Cleanup(pub.Close)

	store := memory.New()
	sealer := credentials.NewSealer(testRing(t))
	parts := &vaultParts{vault: credentials.NewVault(store, sealer), store: store, sealer: sealer}
	var keys []string
	for name, p := range principals {
		e, _ := json.Marshal(auth.APIKey{ID: name, Hash: auth.HashKey(keyOf(name)), Tenant: p[0], User: p[1], Scopes: []string{"*"}, Toolsets: []string{"*"}})
		keys = append(keys, string(e))
	}
	config := fmt.Sprintf(`{
	  "runtime_toolsets": {"allow_hosts": ["127.0.0.1"]},
	  "agents": [{"name": "default", "model": {"id": "fake"}, "runtime_toolsets": ["crm"]}],
	  "auth": {"api_keys": [%s]}
	}`, strings.Join(keys, ","))
	path := filepath.Join(t.TempDir(), "agentkit.json")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadServeConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	call := llmtest.ToolCalls(llm.ToolUse{ID: "c", Name: "crm__whoami", Input: json.RawMessage(`{}`)})
	r.model = llmtest.NewFake(t, call, llmtest.Text("done"), call, llmtest.Text("done"), call, llmtest.Text("done"))
	c := cli{
		getenv:   func(string) string { return "" },
		newModel: func(modelFlags) (llm.Model, error) { return r.model, nil },
		catalog:  func() (*toolset.Catalog, error) { return toolset.NewCatalog() },
	}
	log := slog.New(slog.NewJSONHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	env := serveEnv{
		components: []string{agentapi.Name}, addr: "127.0.0.1:0", shutdownTimeout: time.Second,
		vault: parts.vault, vaultParts: parts, publicURL: pub.URL,
	}
	built, problems, err := c.buildToolsets(t.Context(), env, cfg, path, log)
	if err != nil || len(problems) != 0 {
		t.Fatalf("buildToolsets: %v, %v", problems, err)
	}
	authn, err := c.authenticator(t.Context(), cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	st, closeStore, err := scoped.Open(t.Context(), "sqlite:"+filepath.Join(r.dbDir, "agentkit.db"), clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	am, _, err := c.newAgents(t.Context(), cfg, built, problems, env, authn, st, nil, clock.Real(), log)
	if err != nil {
		t.Fatal(err)
	}
	ac := am.Component.(*agentComponent)
	w := &agentWiring{callback: ac.callback}
	if cm, ok := ac.admin.(interface{ ClientMetadata() http.Handler }); ok {
		w.clientMetadata = cm.ClientMetadata()
	}
	mux := http.NewServeMux()
	for _, m := range w.mounts() {
		mux.Handle(m.Pattern, m.Component.Handler())
	}
	if len(w.mounts()) != 2 || oauthClientMetadataPath != oauth.ClientMetadataPath || oauthCallbackPath != oauth.CallbackPath {
		t.Fatalf("mounts = %+v", w.mounts())
	}
	public = mux
	api := httptest.NewServer(am.Component.Handler())
	t.Cleanup(func() {
		api.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = am.Component.Shutdown(ctx)
		_ = closeStore()
	})
	r.apiURL = api.URL

	do := func(key, method, path, body string) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequestWithContext(t.Context(), method, r.apiURL+path, strings.NewReader(body))
		resp, err := r.client(keyOf(key)).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	// Mounted; served only on an https public URL (CIMD), so DCR here.
	if resp, err := http.Get(pub.URL + oauthClientMetadataPath); err != nil || resp.StatusCode != http.StatusNotFound { //nolint:noctx // test against a local server
		t.Fatalf("client metadata: %v, %v", resp, err)
	} else {
		resp.Body.Close()
	}

	// u1 adds the server: not connected yet, so not reviewed yet.
	code, body := do("u1", "POST", "/v1/toolsets", fmt.Sprintf(`{"type":"mcp","name":"crm","url":%q,"auth":"oauth","scope":"user"}`, rs.URL+"/mcp"))
	var ts wire.ServerToolset
	if code != http.StatusCreated || json.Unmarshal(body, &ts) != nil || ts.Issuer != as.URL || ts.Connection != wire.CredentialNotConnected {
		t.Fatalf("add = %d %s", code, body)
	}
	code, body = do("u1", "POST", "/v1/toolsets/crm/review", `{"tools":[{"name":"whoami","decision":"approve"}]}`)
	var e wire.ErrorResponse
	if code != http.StatusUnprocessableEntity || json.Unmarshal(body, &e) != nil || e.Error == nil || e.Error.Auth != wire.AuthOAuth {
		t.Fatalf("review before connecting = %d %s, want 422 with auth oauth", code, body)
	}

	connectAs := func(key string) {
		t.Helper()
		req, _ := http.NewRequestWithContext(t.Context(), "POST", r.apiURL+"/v1/credentials/crm/connect", strings.NewReader(`{}`))
		resp, err := r.client(keyOf(key)).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var started wire.ConnectResponse
		if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&started) != nil {
			t.Fatalf("connect = %d", resp.StatusCode)
		}
		resp.Body.Close()
		jar, _ := cookiejar.New(nil)
		jar.SetCookies(resp.Request.URL, resp.Cookies())
		nf := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err = nf.Get(started.AuthorizeURL) //nolint:noctx // test against a local server
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if loc := resp.Header.Get("Location"); !strings.HasPrefix(loc, pub.URL+oauthCallbackPath+"?") {
			t.Fatalf("the authorization server redirects to %q", loc)
		}
		resp, err = nf.Get(resp.Header.Get("Location")) //nolint:noctx // test against a local server
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("callback = %d %s", resp.StatusCode, b)
		}
	}
	tn, _ := tenancy.NewTenant("tenant-a")
	tokens := func(user string) (access, refresh string) {
		t.Helper()
		got, _, err := parts.vault.Get(tenancy.With(t.Context(), tn), user, "crm")
		if err != nil {
			t.Fatal(err)
		}
		o := got.(credentials.OAuth2)
		return o.AccessToken.Reveal(), o.RefreshToken.Reveal()
	}

	connectAs("u1")
	code, body = do("u1", "GET", "/v1/toolsets/crm/review", "")
	if code != http.StatusOK || json.Unmarshal(body, &ts) != nil || len(ts.Tools) != 1 || ts.Connection != wire.CredentialConnected {
		t.Fatalf("review = %d %s", code, body)
	}
	if code, body := do("u1", "POST", "/v1/toolsets/crm/review", fmt.Sprintf(`{"tools":[{"name":"whoami","fingerprint":%q,"decision":"approve"}]}`, ts.Tools[0].Fingerprint)); code != http.StatusOK {
		t.Fatalf("approve = %d %s", code, body)
	}
	a1, r1 := tokens("u1")

	// The agent calls the tool as u1 (u1's token), and as u2 (not
	// connected, reaching no server), then as u2 once connected.
	text, isErr, _ := r.runAgent(t, e2eCall{name: "u1", principal: "u1"})
	if isErr || !strings.Contains(text, shortHash(a1)) {
		t.Fatalf("u1's call: %q (error %v)", text, isErr)
	}
	text, isErr, _ = r.runAgent(t, e2eCall{name: "u2 not connected", principal: "u2"})
	if !isErr || !strings.Contains(text, "not connected") || strings.Contains(text, "token ") {
		t.Fatalf("u2's call before connecting: %q (error %v)", text, isErr)
	}
	connectAs("u2")
	a2, r2 := tokens("u2")
	if a2 == a1 {
		t.Fatal("u2 got u1's token")
	}
	text, isErr, _ = r.runAgent(t, e2eCall{name: "u2", principal: "u2"})
	if isErr || !strings.Contains(text, shortHash(a2)) {
		t.Fatalf("u2's call: %q (error %v)", text, isErr)
	}
	code, body = do("u2", "GET", "/v1/credentials", "")
	var l wire.CredentialList
	if code != http.StatusOK || json.Unmarshal(body, &l) != nil || len(l.Credentials) != 1 || !l.Credentials[0].OAuth || l.Credentials[0].Status != wire.CredentialConnected {
		t.Errorf("u2's credentials = %d %s", code, body)
	}

	// E07's canary: no token in the logs, the API's responses (the runs'
	// events among them) or the agent store.
	where := map[string]string{"logs": r.logs.String()}
	r.mu.Lock()
	where["HTTP responses"] = strings.Join(r.bodies, "\n")
	r.mu.Unlock()
	var db bytes.Buffer
	_ = filepath.WalkDir(r.dbDir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			db.Write(b)
		}
		return nil
	})
	where["store"] = db.String()
	for _, tok := range []string{a1, r1, a2, r2} {
		if tok == "" {
			t.Fatal("no token to look for")
		}
		for name, s := range where {
			if i := strings.Index(s, tok); i >= 0 {
				t.Errorf("a token is in the %s: ...%s...", name, s[max(0, i-200):min(len(s), i+80)])
			}
		}
	}
}
