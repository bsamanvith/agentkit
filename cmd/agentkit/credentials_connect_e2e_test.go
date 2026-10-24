//go:build !noagent

package main

// End-to-end test of connecting accounts (T045), wired as serve wires it:
// a user connects their own mail account through the agent API, the fake
// provider and the callback, an admin sets the tenant's CRM key, and the
// tools called over MCP then use them; GET /v1/toolsets and GET
// /v1/credentials show who is connected, and no response or log line holds
// a secret. Also: agentkit credentials put, and the config's checks.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/credentials/connect"
	"github.com/bsamanvith/agentkit/pkg/credentials/credentialstest"
	"github.com/bsamanvith/agentkit/pkg/credentials/memory"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/store/scoped"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
	"github.com/bsamanvith/agentkit/pkg/toolset"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest"
)

const connectCanary = "t045-e2e-canary-a7c2"

func TestConnectAccounts(t *testing.T) {
	t.Parallel()
	r := &credRig{upstream: toolsettest.NewCredentialUpstream(t), logs: &syncLog{}, dbDir: t.TempDir()}
	ts := credentialstest.NewTokenServer(t, credentialstest.TokenServerOptions{Prefix: connectCanary + "-"})
	clientSecret := "client-secret-" + connectCanary
	ts.AddClient("mail-app", clientSecret)
	var callback http.Handler
	cb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { callback.ServeHTTP(w, req) }))
	t.Cleanup(cb.Close)

	store := memory.New()
	sealer := credentials.NewSealer(testRing(t))
	parts := &vaultParts{vault: credentials.NewVault(store, sealer), store: store, sealer: sealer}
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
	  "credentials": {
	    "oauth2": [{"slot": "mail", "auth_url": %[4]q, "token_url": %[5]q, "client_id": "mail-app", "client_secret_env": "MAIL_CLIENT_SECRET",
	                "scopes": ["mail.read"], "redirect_url": %[6]q}],
	    "return_urls": ["https://console.example.com/connections/done"]
	  }
	}`, toolsettest.CredentialEchoType, r.upstream.URL, strings.Join(keys, ","), ts.AuthURL, ts.URL, cb.URL+connect.CallbackPath)
	path := filepath.Join(t.TempDir(), "agentkit.json")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadServeConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	r.model = llmtest.NewFake(t, llmtest.Text("unused"))
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
	env := serveEnv{components: []string{agentapi.Name, mcpServerName}, addr: "127.0.0.1:0", shutdownTimeout: time.Second, vault: parts.vault, vaultParts: parts}
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
	ac := am.Component.(*agentComponent)
	if ac.callback == nil {
		t.Fatal("no callback with a vault")
	}
	callback = ac.callback
	if m := (&agentWiring{callback: ac.callback}).mounts(); len(m) != 1 || m[0].Pattern != connect.CallbackPath {
		t.Errorf("mounts = %+v, want the callback at %s", m, connect.CallbackPath)
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

	call := func(key, method, path, body string) (*http.Response, []byte) {
		t.Helper()
		req, _ := http.NewRequestWithContext(t.Context(), method, r.apiURL+path, strings.NewReader(body))
		resp, err := r.client(keyOf(key)).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var b bytes.Buffer
		_, _ = b.ReadFrom(resp.Body)
		return resp, b.Bytes()
	}
	status := func(key, name string) string {
		t.Helper()
		_, body := call(key, "GET", "/v1/toolsets", "")
		var l wire.ToolsetList
		_ = json.Unmarshal(body, &l)
		for _, ts := range l.Toolsets {
			if ts.Name == name {
				return ts.Credential.Status
			}
		}
		return ""
	}
	if s := status("u1", "mail"); s != wire.CredentialNotConnected {
		t.Errorf("mail for u1 before connecting: %s", s)
	}

	// u1 connects their mail account.
	resp, body := call("u1", "POST", "/v1/credentials/mail/connect", `{"return_url":"https://console.example.com/connections/done"}`)
	var started wire.ConnectResponse
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &started) != nil {
		t.Fatalf("connect = %d %s", resp.StatusCode, body)
	}
	// The browser that started the flow finishes it: it keeps the binding
	// cookie the API set, which the callback (same host) needs.
	jar, _ := cookiejar.New(nil)
	jar.SetCookies(resp.Request.URL, resp.Cookies())
	nf := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err = nf.Get(started.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	resp, err = nf.Get(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "https://console.example.com/connections/done?") || !strings.Contains(loc, "result=connected") {
		t.Fatalf("callback = %d %s", resp.StatusCode, loc)
	}
	tn, _ := tenancy.NewTenant("tenant-a")
	got, _, err := parts.vault.Get(tenancy.With(t.Context(), tn), "u1", "mail")
	if err != nil {
		t.Fatal(err)
	}
	r.upstream.Accept("Authorization", "Bearer "+got.(credentials.OAuth2).AccessToken.Reveal(), "U1")

	// An admin of tenant A sets the CRM key.
	if resp, body := call("a", "PUT", "/v1/credentials/crm", `{"kind":"api_key","api_key":"crm-`+connectCanary+`"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("put = %d %s", resp.StatusCode, body)
	}
	r.upstream.Accept("Authorization", "Bearer crm-"+connectCanary, "A")

	for _, cl := range []e2eCall{
		{name: "connected mail", principal: "u1", toolset: "mail", tool: "whoami", who: "U1"},
		{name: "another user's mail", principal: "u2", toolset: "mail", tool: "whoami", errSays: "mail is not connected for this account", notConn: true},
		{name: "the tenant's crm", principal: "u2", toolset: "crm", tool: "whoami", who: "A"},
		{name: "tenant B's crm", principal: "b", toolset: "crm", tool: "whoami", errSays: "crm is not connected for this account", notConn: true},
	} {
		text, isErr := r.callMCP(t, cl)
		checkCall(t, cl.name, cl, text, isErr)
	}
	for key, want := range map[string]string{"u1": wire.CredentialConnected, "u2": wire.CredentialNotConnected, "b": wire.CredentialNotConnected} {
		if s := status(key, "mail"); s != want {
			t.Errorf("mail for %s: %s, want %s", key, s, want)
		}
	}
	_, body = call("u1", "GET", "/v1/credentials", "")
	var l wire.CredentialList
	if json.Unmarshal(body, &l) != nil || len(l.Credentials) != 2 || l.Credentials[0].Status != wire.CredentialConnected || l.Credentials[1].Status != wire.CredentialConnected || !l.Credentials[1].OAuth {
		t.Errorf("credentials for u1 = %s", body)
	}

	where := map[string]string{"logs": r.logs.String()}
	r.mu.Lock()
	where["HTTP responses"] = strings.Join(r.bodies, "\n")
	r.mu.Unlock()
	for name, s := range where {
		if i := strings.Index(s, connectCanary); i >= 0 {
			t.Errorf("the canary is in the %s: ...%s...", name, s[max(0, i-200):min(len(s), i+80)])
		}
	}
}

// agentkit credentials put writes to the configured vault directly, the
// secret from stdin; it refuses a vault that would not outlive it.
func TestCredentialsPutCLI(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agentkit.json")
	vaultPath := filepath.Join(dir, "credentials.json")
	if err := os.WriteFile(cfgPath, []byte(`{"credentials": {"backend": "file", "path": `+fmt.Sprintf("%q", vaultPath)+`}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(stdin string, env map[string]string, args ...string) (int, string) {
		var out, errOut bytes.Buffer
		c := cli{stdout: &out, stderr: &errOut, getenv: func(k string) string { return env[k] }, term: clientTerm{stdin: strings.NewReader(stdin)}}
		code := c.main(t.Context(), append([]string{"credentials", "put"}, args...))
		return code, out.String() + errOut.String()
	}
	env := map[string]string{envCredentialsKey: testCredentialsKey, envConfig: cfgPath}
	if code, out := run("key-"+connectCanary+"\n", env, "crm", "-tenant", "acme"); code != 0 || strings.Contains(out, connectCanary) {
		t.Fatalf("put = %d %s", code, out)
	}
	if code, out := run("pw\n", env, "-tenant", "acme", "-user", "u1", "-kind", "basic", "-username", "me", "mail"); code != 0 {
		t.Fatalf("put basic = %d %s", code, out)
	}
	if code, out := run("key-2\n", env, "crm", "-tenant", "acme"); code != 0 || !strings.Contains(out, "version 2") {
		t.Fatalf("put again = %d %s", code, out)
	}
	for name, tc := range map[string]struct {
		stdin string
		env   map[string]string
		args  []string
	}{
		"no tenant":     {"k\n", env, []string{"crm"}},
		"no secret":     {"", env, []string{"crm", "-tenant", "acme"}},
		"no key ring":   {"k\n", map[string]string{envConfig: cfgPath}, []string{"crm", "-tenant", "acme"}},
		"memory vault":  {"k\n", map[string]string{envCredentialsKey: testCredentialsKey}, []string{"crm", "-tenant", "acme"}},
		"bad kind":      {"k\n", env, []string{"crm", "-tenant", "acme", "-kind", "token"}},
		"bad slot":      {"k\n", env, []string{"has space", "-tenant", "acme"}},
		"flows' tenant": {"k\n", env, []string{"crm", "-tenant", "agentkit-oauth-flows"}},
		"http tokenurl": {"k\n", env, []string{"api", "-tenant", "acme", "-kind", "client_credentials", "-client-id", "c", "-token-url", "http://x/token"}},
	} {
		if code, _ := run(tc.stdin, tc.env, tc.args...); code == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
	parts, closeFn, err := cli{}.openVaultParts(t.Context(), credentialsConfig{Backend: "file", Path: vaultPath}, testRing(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeFn() }()
	tn, _ := tenancy.NewTenant("acme")
	ctx := tenancy.With(t.Context(), tn)
	if c, _, err := parts.vault.Get(ctx, "", "crm"); err != nil || c.(credentials.APIKey).Key.Reveal() != "key-2" {
		t.Errorf("crm = %v, %v", c, err)
	}
	if c, _, err := parts.vault.Get(ctx, "u1", "mail"); err != nil || c.(credentials.Basic).User != "me" {
		t.Errorf("mail = %v, %v", c, err)
	}
}

// The connect flow's config is checked at startup: endpoints https (http
// only on loopback), a redirect URL at /oauth/callback, a known profile,
// and the profile's presets applied.
func TestConnectConfig(t *testing.T) {
	t.Parallel()
	c := cli{getenv: func(string) string { return "s3cret" }}
	base := oauth2AppConfig{
		Slot: "mail", TokenURL: "https://idp.example.com/token", ClientID: "app", ClientSecretEnv: "S",
		AuthURL: "https://idp.example.com/authorize", RedirectURL: "https://agents.example.com/oauth/callback",
	}
	for name, tc := range map[string]struct {
		change func(*oauth2AppConfig)
		ok     bool
	}{
		"ok": {func(*oauth2AppConfig) {}, true},
		"loopback": {func(a *oauth2AppConfig) {
			a.AuthURL, a.RedirectURL = "http://127.0.0.1:9/authorize", "http://127.0.0.1:8080/oauth/callback"
		}, true},
		"http auth url":             {func(a *oauth2AppConfig) { a.AuthURL = "http://idp.example.com/authorize" }, false},
		"no redirect url":           {func(a *oauth2AppConfig) { a.RedirectURL = "" }, false},
		"redirect elsewhere":        {func(a *oauth2AppConfig) { a.RedirectURL = "https://agents.example.com/callback" }, false},
		"redirect without auth url": {func(a *oauth2AppConfig) { a.AuthURL = "" }, false},
		"unknown profile":           {func(a *oauth2AppConfig) { a.Profile = "acme" }, false},
		"the mcp profile":           {func(a *oauth2AppConfig) { a.Profile = "mcp" }, false},
	} {
		a := base
		tc.change(&a)
		_, err := c.oauth2Apps(credentialsConfig{OAuth2: []oauth2AppConfig{a}})
		if (err == nil) != tc.ok {
			t.Errorf("%s: %v, want accepted %v", name, err, tc.ok)
		}
	}
	g := oauth2AppConfig{
		Slot: "gmail", Profile: "google", ClientID: "app", ClientSecretEnv: "S", Scopes: []string{"gmail.readonly"},
		RedirectURL: "https://agents.example.com/oauth/callback", AuthParams: map[string]string{"prompt": "select_account"},
	}
	apps, err := c.oauth2Apps(credentialsConfig{OAuth2: []oauth2AppConfig{g}})
	if err != nil || len(apps) != 1 {
		t.Fatal(err)
	}
	if a := apps[0]; a.authURL != "https://accounts.google.com/o/oauth2/v2/auth" || a.app.TokenURL != "https://oauth2.googleapis.com/token" ||
		a.params["access_type"] != "offline" || a.params["prompt"] != "select_account" || len(a.scope) != 1 {
		t.Errorf("google = %+v", a)
	}
	s := oauth2AppConfig{
		Slot: "sf", Profile: "salesforce", ClientID: "app", ClientSecretEnv: "S", TokenURL: "https://login.salesforce.com/services/oauth2/token",
		AuthURL: "https://login.salesforce.com/services/oauth2/authorize", RedirectURL: "https://agents.example.com/oauth/callback", Scopes: []string{"api"},
	}
	if apps, err := c.oauth2Apps(credentialsConfig{OAuth2: []oauth2AppConfig{s}}); err != nil || strings.Join(apps[0].scope, " ") != "api refresh_token" {
		t.Errorf("salesforce = %+v, %v", apps, err)
	}
	h := oauth2AppConfig{
		Slot: "hubspot", Profile: "hubspot", ClientID: "app", ClientSecretEnv: "S", Scopes: []string{"oauth", "crm.objects.contacts.read"},
		RedirectURL: "https://agents.example.com/oauth/callback",
	}
	if apps, err := c.oauth2Apps(credentialsConfig{OAuth2: []oauth2AppConfig{h}}); err != nil ||
		apps[0].authURL != "https://app.hubspot.com/oauth/authorize" || apps[0].app.TokenURL != "https://api.hubapi.com/oauth/v1/token" ||
		apps[0].app.AuthStyle != credentials.AuthInParams || strings.Join(apps[0].scope, " ") != "oauth crm.objects.contacts.read" {
		t.Errorf("hubspot = %+v, %v", apps, err)
	}
	if oauthCallbackPath != connect.CallbackPath {
		t.Errorf("serve mounts the callback at %s, the flows redirect to %s", oauthCallbackPath, connect.CallbackPath)
	}
}
