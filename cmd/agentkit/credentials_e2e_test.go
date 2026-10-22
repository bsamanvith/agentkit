package main

// End-to-end test of credentials in tools (T044): toolsets whose specs name
// credential slots, built as serve builds them, called over MCP and through
// the agent API (with a fake model) as several tenants and users, each
// reaching the upstream with their own credential; and the canary check:
// a secret in every credential, token and client secret never appears in a
// log line, an SSE event, an MCP response, the store's file or an error.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/credentials/credentialstest"
	"github.com/bsamanvith/agentkit/pkg/credentials/memory"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/store/scoped"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
	"github.com/bsamanvith/agentkit/pkg/toolset"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest"
)

// e2eCanary is in every secret of the test.
const e2eCanary = "t044-e2e-canary-c4f1"

// syncLog is a log sink several goroutines write.
type syncLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *syncLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *syncLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// recorder keeps every request and response body a client sends and gets,
// and adds the caller's bearer key.
type recorder struct {
	key    string
	mu     *sync.Mutex
	bodies *[]string
}

func (r recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+r.key)
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	r.mu.Lock()
	*r.bodies = append(*r.bodies, string(body))
	r.mu.Unlock()
	resp.Body = io.NopCloser(strings.NewReader(string(body)))
	return resp, err
}

type credRig struct {
	mcpURL, apiURL string
	upstream       *toolsettest.CredentialUpstream
	model          *llmtest.Fake
	logs           *syncLog
	dbDir          string
	mu             sync.Mutex
	bodies         []string
}

func (r *credRig) client(key string) *http.Client {
	return &http.Client{Transport: recorder{key: key, mu: &r.mu, bodies: &r.bodies}}
}

// principals are the API keys of the test, by name: tenant, user.
var principals = map[string][2]string{
	"a": {"tenant-a", ""}, "b": {"tenant-b", ""}, "c": {"tenant-c", ""}, "d": {"tenant-d", ""},
	"u1": {"tenant-a", "u1"}, "u2": {"tenant-a", "u2"}, "u3": {"tenant-a", "u3"}, "u4": {"tenant-a", "u4"},
}

func keyOf(name string) string { return "key-" + name + "-0123456789abcdef" }

// e2eCall is one tool call of the table: who calls which tool, and what
// it gets.
type e2eCall struct {
	name      string
	principal string
	toolset   string
	tool      string
	who       string // the upstream's name for the credential; "" for an error
	errSays   string // in the error's text
	notConn   bool   // the error is the not-connected one
}

var e2eCalls = []e2eCall{
	{name: "tenant A", principal: "a", toolset: "crm", tool: "whoami", who: "A"},
	{name: "tenant B", principal: "b", toolset: "crm", tool: "whoami", who: "B"},
	{name: "tenant scope ignores the user", principal: "u1", toolset: "crm", tool: "whoami", who: "A"},
	{name: "user U1", principal: "u1", toolset: "mail", tool: "whoami", who: "U1"},
	{name: "user U2", principal: "u2", toolset: "mail", tool: "whoami", who: "U2"},
	{name: "no credential", principal: "c", toolset: "crm", tool: "whoami", errSays: "crm is not connected for this account", notConn: true},
	{name: "no user credential", principal: "a", toolset: "mail", tool: "whoami", errSays: "mail is not connected for this account", notConn: true},
	{name: "invalid_grant", principal: "u3", toolset: "mail", tool: "whoami", errSays: "mail is not connected for this account", notConn: true},
	{name: "refresh fails", principal: "u4", toolset: "mail", tool: "whoami", errSays: "refreshing its credential failed: the token endpoint answered 503"},
	{name: "upstream 401", principal: "d", toolset: "crm", tool: "whoami", errSays: "the upstream answered 401 Unauthorized"},
	{name: "panic", principal: "a", toolset: "crm", tool: "whoami_panic", errSays: "panic"},
}

// newCredRig seeds a vault, builds the toolsets of the config as serve
// does, and serves the agent API and the MCP server over them.
func newCredRig(t *testing.T) *credRig {
	t.Helper()
	r := &credRig{upstream: toolsettest.NewCredentialUpstream(t), logs: &syncLog{}, dbDir: t.TempDir()}
	ts := credentialstest.NewTokenServer(t, credentialstest.TokenServerOptions{Prefix: e2eCanary + "-"})
	clientSecret := "client-secret-" + e2eCanary
	ts.AddClient("mail-app", clientSecret)
	ts.SetOptions(credentialstest.TokenServerOptions{
		Prefix: e2eCanary + "-",
		Hook: func(w http.ResponseWriter, _ *http.Request, form url.Values) bool {
			if strings.HasPrefix(form.Get("refresh_token"), "rt-u4-") {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, "overloaded, try "+form.Encode())
				return true
			}
			return false
		},
	})

	vault := credentials.NewVault(memory.New(), credentials.NewSealer(testRing(t)))
	seed := func(principal, slot string, c credentials.Credential) {
		p := principals[principal]
		tn, _ := tenancy.NewTenant(p[0])
		if _, err := vault.Put(tenancy.With(t.Context(), tn), p[1], slot, c, 0); err != nil {
			t.Fatal(err)
		}
	}
	secret := func(s string) credentials.Secret { return credentials.NewSecret(s + "-" + e2eCanary) }
	seed("a", "crm", credentials.APIKey{Key: secret("crm-a")})
	r.upstream.Accept("Authorization", "Bearer crm-a-"+e2eCanary, "A")
	seed("b", "crm", credentials.APIKey{Key: secret("crm-b")})
	r.upstream.Accept("Authorization", "Bearer crm-b-"+e2eCanary, "B")
	seed("d", "crm", credentials.APIKey{Key: secret("crm-d-revoked")}) // the upstream does not know it
	seed("u1", "mail", credentials.OAuth2{AccessToken: secret("mail-u1"), Expiry: time.Now().Add(time.Hour)})
	r.upstream.Accept("Authorization", "Bearer mail-u1-"+e2eCanary, "U1")
	// U2's token is about to expire: it is refreshed, and the new one used.
	ts.AddRefreshToken("rt-u2-" + e2eCanary)
	seed("u2", "mail", credentials.OAuth2{AccessToken: secret("mail-u2-old"), RefreshToken: secret("rt-u2"), Expiry: time.Now().Add(10 * time.Second)})
	r.upstream.Accept("Authorization", "Bearer "+e2eCanary+"-at-1", "U2")
	seed("u3", "mail", credentials.OAuth2{AccessToken: secret("mail-u3"), RefreshToken: secret("rt-u3-revoked"), Expiry: time.Now().Add(-time.Minute)})
	seed("u4", "mail", credentials.OAuth2{AccessToken: secret("mail-u4"), RefreshToken: secret("rt-u4"), Expiry: time.Now().Add(-time.Minute)})

	var keys []string
	for name, p := range principals {
		e, _ := json.Marshal(auth.APIKey{ID: name, Hash: auth.HashKey(keyOf(name)), Tenant: p[0], User: p[1], Scopes: []string{"*"}, Toolsets: []string{"*"}})
		keys = append(keys, string(e))
	}
	config := fmt.Sprintf(`{
	  "mcp": {"toolsets": [
	    {"name": "crm", "type": %[1]q, "settings": {"base_url": %[2]q}, "credential": {"slot": "crm"}},
	    {"name": "mail", "type": %[1]q, "settings": {"base_url": %[2]q}, "credential": {"slot": "mail", "scope": "user"}}
	  ]},
	  "agents": [{"name": "default", "model": {"id": "fake"}, "toolsets": ["crm", "mail"], "prefix_tool_names": true}],
	  "auth": {"api_keys": [%[3]s]},
	  "credentials": {"oauth2": [{"slot": "mail", "token_url": %[4]q, "client_id": "mail-app", "client_secret_env": "MAIL_CLIENT_SECRET"}]}
	}`, toolsettest.CredentialEchoType, r.upstream.URL, strings.Join(keys, ","), ts.URL)
	path := filepath.Join(t.TempDir(), "agentkit.json")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadServeConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	// The model calls each tool of the table in turn, then answers.
	var replies []llmtest.Reply
	for i, c := range e2eCalls {
		replies = append(replies,
			llmtest.ToolCalls(llm.ToolUse{ID: fmt.Sprintf("call-%d", i), Name: c.toolset + "__" + c.tool, Input: json.RawMessage(`{}`)}),
			llmtest.Text("answered without the tool"))
	}
	r.model = llmtest.NewFake(t, replies...)
	c := cli{
		getenv: func(k string) string {
			if k == "MAIL_CLIENT_SECRET" {
				return clientSecret
			}
			return ""
		},
		newModel: func(modelFlags) (llm.Model, error) { return r.model, nil },
		catalog:  func() (*toolset.Catalog, error) { return toolset.NewCatalog(toolsettest.CredentialEcho()) },
	}
	log := slog.New(slog.NewJSONHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	env := serveEnv{components: []string{agentapi.Name, mcpServerName}, addr: "127.0.0.1:0", shutdownTimeout: time.Second, vault: vault}
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
	am, agents, err := c.newAgents(t.Context(), cfg, built, problems, env, authn, st, nil, clock.Real(), log)
	if err != nil {
		t.Fatal(err)
	}
	mm, err := newMCPServer(t.Context(), cfg.MCP, built, problems, authn, agents.Handler(), env.addr, log)
	if err != nil {
		t.Fatal(err)
	}
	api, mcpSrv := httptest.NewServer(am.Component.Handler()), httptest.NewServer(mm.Component.Handler())
	t.Cleanup(func() {
		api.Close()
		mcpSrv.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = am.Component.Shutdown(ctx)
		_ = mm.Component.Shutdown(ctx)
		_ = closeStore()
	})
	r.apiURL, r.mcpURL = api.URL, mcpSrv.URL+"/mcp/"
	return r
}

// callMCP calls a tool over MCP as principal: its text, and whether the
// result is an error.
func (r *credRig) callMCP(t *testing.T, c e2eCall) (string, bool) {
	t.Helper()
	cl := mcp.NewClient(&mcp.Implementation{Name: "t044", Version: "0"}, nil)
	cs, err := cl.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: r.mcpURL + c.toolset, HTTPClient: r.client(keyOf(c.principal)), DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer cs.Close()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: c.tool, Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	var text []string
	for _, ct := range res.Content {
		if tc, ok := ct.(*mcp.TextContent); ok {
			text = append(text, tc.Text)
		}
	}
	return strings.Join(text, ""), res.IsError
}

// runAgent runs the agent as principal (the model calls the tool, then
// answers): the tool result's content, whether it is an error, and the
// run's last event.
func (r *credRig) runAgent(t *testing.T, c e2eCall) (string, bool, wire.Event) {
	t.Helper()
	ac, err := agentclient.New(r.apiURL, agentclient.WithHTTPClient(r.client(keyOf(c.principal))))
	if err != nil {
		t.Fatal(err)
	}
	s, err := ac.Stream(t.Context(), agentclient.RunRequest{Message: "go: " + c.name})
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
	if result == nil {
		t.Fatalf("no tool_result; last event %+v", last)
	}
	return result.Content, result.IsError, last
}

func checkCall(t *testing.T, path string, c e2eCall, text string, isErr bool) {
	t.Helper()
	if c.who != "" {
		var out struct{ Who string }
		if isErr || json.Unmarshal([]byte(text), &out) != nil || out.Who != c.who {
			t.Errorf("%s: %q (error %v), want who %q", path, text, isErr, c.who)
		}
		return
	}
	if !isErr || !strings.Contains(text, c.errSays) {
		t.Errorf("%s: %q (error %v), want an error saying %q", path, text, isErr, c.errSays)
	}
	if c.notConn && strings.Contains(text, "vault") {
		t.Errorf("%s: the not-connected error has detail from the vault: %q", path, text)
	}
}

// TestCredentialsInTools: each call reaches the upstream with its caller's
// credential, over MCP and through the agent; a missing or unusable one is
// a tool error the run continues past; and the canary is nowhere.
func TestCredentialsInTools(t *testing.T) {
	t.Parallel()
	r := newCredRig(t)
	// Calls run one after another: the model's script is in table order.
	for _, c := range e2eCalls {
		t.Run(c.name, func(t *testing.T) {
			text, isErr := r.callMCP(t, c)
			checkCall(t, "MCP", c, text, isErr)
			text, isErr, last := r.runAgent(t, c)
			checkCall(t, "agent", c, text, isErr)
			if last.Type != wire.TypeRunDone || last.Answer != "answered without the tool" {
				t.Errorf("agent: the run ended with %+v, want run_done with the model's answer", last)
			}
		})
	}
	// The upstream saw each known credential from its own caller only.
	seen := r.upstream.Seen()
	for _, who := range []string{"A", "B", "U1", "U2"} {
		n := 0
		for _, s := range seen {
			if s == who {
				n++
			}
		}
		want := 2
		if who == "A" {
			want = 4 // tenant A, and U1 through the tenant-scoped crm
		}
		if n != want {
			t.Errorf("the upstream saw %s's credential %d times, want %d (seen %q)", who, n, want, seen)
		}
	}
	// The model was shown each tool result, and the run went on.
	reqs := r.model.Requests()
	if len(reqs) != 2*len(e2eCalls) {
		t.Errorf("%d model requests, want %d", len(reqs), 2*len(e2eCalls))
	}

	t.Run("canary", func(t *testing.T) {
		where := map[string]string{"logs": r.logs.String()}
		r.mu.Lock()
		where["HTTP responses (SSE, JSON, MCP)"] = strings.Join(r.bodies, "\n")
		r.mu.Unlock()
		var prompts strings.Builder
		for _, req := range reqs {
			b, _ := json.Marshal(req)
			prompts.Write(b)
		}
		where["model requests"] = prompts.String()
		entries, _ := os.ReadDir(r.dbDir)
		for _, e := range entries {
			b, err := os.ReadFile(filepath.Join(r.dbDir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			where["store file "+e.Name()] = string(b)
		}
		if !strings.Contains(where["store file agentkit.db"]+where["store file agentkit.db-wal"], "answered without the tool") {
			t.Fatal("the store file holds no conversation: the check would pass on nothing")
		}
		if !strings.Contains(where["logs"], `"level":"DEBUG"`) {
			t.Fatal("no debug logs: the check would miss them")
		}
		for name, s := range where {
			if i := strings.Index(s, e2eCanary); i >= 0 {
				t.Errorf("the canary is in the %s: ...%s...", name, s[max(0, i-200):min(len(s), i+80)])
			}
		}
	})
}

// TestCredentialSlotNeedsAVault: a spec naming a credential slot with no
// vault fails startup naming the key ring when required, and is skipped
// when optional.
func TestCredentialSlotNeedsAVault(t *testing.T) {
	t.Parallel()
	for _, optional := range []bool{false, true} {
		cfg := serveConfig{MCP: defaultMCPConfig()}
		cfg.MCP.Toolsets = []toolset.Spec{
			{Name: "demo", Type: toolset.DemoType},
			{Name: "crm", Type: toolset.DemoType, Optional: optional, Credential: &toolset.CredentialRef{Slot: "crm"}},
		}
		env := serveEnv{components: []string{mcpServerName}}
		regs, problems, err := cli{getenv: func(string) string { return "" }}.buildToolsets(t.Context(), env, cfg, "agentkit.json", slog.New(slog.DiscardHandler))
		switch {
		case !optional && (!errors.Is(err, toolset.ErrNoCredentials) || !strings.Contains(err.Error(), envCredentialsKey)):
			t.Errorf("required: %v, want ErrNoCredentials naming $%s", err, envCredentialsKey)
		case optional && (err != nil || len(problems) != 1 || regs["crm"] != nil || regs["demo"] == nil):
			t.Errorf("optional: %v, %v, %v; want crm skipped", regs, problems, err)
		}
	}
}

// TestOAuth2TokenURL: the client secret and refresh tokens go to token_url,
// so it is https, or http only on a loopback host.
func TestOAuth2TokenURL(t *testing.T) {
	t.Parallel()
	vault := credentials.NewVault(memory.New(), credentials.NewSealer(testRing(t)))
	c := cli{getenv: func(string) string { return "s3cret" }}
	for u, ok := range map[string]bool{
		"https://oauth2.example.com/token": true,
		"http://127.0.0.1:8080/token":      true,
		"http://[::1]/token":               true,
		"http://localhost/token":           true,
		"http://oauth2.example.com/token":  false,
		"ftp://127.0.0.1/token":            false,
		"https:///token":                   false,
	} {
		cfg := credentialsConfig{OAuth2: []oauth2AppConfig{{Slot: "mail", TokenURL: u, ClientID: "app", ClientSecretEnv: "SECRET"}}}
		_, err := c.credentialSources(vault, cfg, slog.New(slog.DiscardHandler))
		if (err == nil) != ok {
			t.Errorf("token_url %q: %v, want accepted %v", u, err, ok)
		}
	}
}
