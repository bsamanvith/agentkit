package agentapi_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/auth/authtest"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/credentials/connect"
	"github.com/bsamanvith/agentkit/pkg/credentials/credentialstest"
	"github.com/bsamanvith/agentkit/pkg/credentials/memory"
	"github.com/bsamanvith/agentkit/pkg/store/scoped"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// credCanary is in every secret of the credentials tests.
const credCanary = "t045-api-canary-5d21"

// The keys of credRig: tenant A's admin, users u1 and u2, and a service key
// (no user); tenant B's user u1, who holds every scope; and a key of the
// tenant reserved for the connect flows, which also holds every scope.
const (
	keyCredAdmin   = "key-cred-admin-a-0123456"
	keyCredU1      = "key-cred-u1-a-0123456789"
	keyCredU2      = "key-cred-u2-a-0123456789"
	keyCredService = "key-cred-service-a-01234"
	keyCredB       = "key-cred-u1-b-0123456789"
	keyCredFlows   = "key-cred-flows-012345678"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// credRig serves the agent API with credentials over a memory vault: slot
// "mail" (each user's own, used by toolset "mail") and slot "crm" (the
// tenant's, used by "crm"), both connectable at the fake provider, whose
// callback is served beside the API.
type credRig struct {
	*rig
	ts       *credentialstest.TokenServer
	vault    credentials.Vault
	clk      *clocktest.Fake
	a, b     tenancy.Tenant
	logs     *lockedBuffer
	callback *httptest.Server
}

func newCredRig(t *testing.T, opts ...agentapi.Option) *credRig {
	t.Helper()
	ten := func(name string) tenancy.Tenant {
		tn, err := tenancy.NewTenant(fmt.Sprintf("%s%d", name, time.Now().UnixNano()))
		if err != nil {
			t.Fatal(err)
		}
		return tn
	}
	cr := &credRig{a: ten("a"), b: ten("b"), logs: &lockedBuffer{}, clk: clocktest.NewFake(time.Now())}
	flows, err := tenancy.NewTenant(connect.FlowTenant)
	if err != nil {
		t.Fatal(err)
	}
	read, run := agentapi.ScopeRead, agentapi.ScopeRun
	all := []string{"*"}
	authn := authtest.NewFake(map[string]auth.Principal{
		keyCredAdmin:   {Tenant: cr.a, User: "admin", Scopes: []string{read, run, agentapi.ScopeCredentialsAdmin}, Toolsets: all},
		keyCredU1:      {Tenant: cr.a, User: "u1", Scopes: []string{read, run}, Toolsets: all},
		keyCredU2:      {Tenant: cr.a, User: "u2", Scopes: []string{read, run}, Toolsets: all},
		keyCredService: {Tenant: cr.a, Scopes: []string{read, run}, Toolsets: all},
		keyCredB:       {Tenant: cr.b, User: "u1", Scopes: all, Toolsets: all},
		keyCredFlows:   {Tenant: flows, User: "flows-00", Scopes: all, Toolsets: all},
	})
	cr.ts = credentialstest.NewTokenServer(t, credentialstest.TokenServerOptions{Prefix: credCanary + "-"})
	cr.ts.AddClient("app", "app-secret-"+credCanary)
	store := memory.New()
	ring, err := credentials.ParseKeyRing("k1:AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8")
	if err != nil {
		t.Fatal(err)
	}
	cr.vault = credentials.NewVault(store, credentials.NewSealer(ring))
	var svc *connect.Service
	cr.callback = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { svc.Callback().ServeHTTP(w, r) }))
	t.Cleanup(cr.callback.Close)
	app := connect.App{
		ClientID: "app", ClientSecret: credentials.NewSecret("app-secret-" + credCanary),
		AuthURL: cr.ts.AuthURL, TokenURL: cr.ts.URL, RedirectURL: cr.callback.URL + connect.CallbackPath,
	}
	log := slog.New(slog.NewJSONHandler(cr.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	svc, err = connect.New(connect.Config{
		Vault: cr.vault, Store: store, Sealer: credentials.NewSealer(ring),
		Slots: []connect.Slot{
			{Name: "mail", Scope: credentials.ScopeUser, Toolsets: []string{"mail"}},
			{Name: "crm", Scope: credentials.ScopeTenant, Toolsets: []string{"crm"}},
		},
		Apps:       map[string]connect.App{"mail": app, "crm": app},
		ReturnURLs: []string{"https://console.example.com/connections/done"},
		Clock:      cr.clk, Logger: log,
	})
	if err != nil {
		t.Fatal(err)
	}
	sets := demoTools(t)
	slot := func(name, slot, scope string) agentapi.Toolset {
		return agentapi.Toolset{Name: name, Type: "demo", Tools: sets["a"], Credential: agentapi.Credential{Status: wire.CredentialMissing, Slot: slot, Scope: scope}}
	}
	cr.rig = newRig(t, echo(t), rigConfig{store: scoped.Memory(nil), api: append([]agentapi.Option{
		agentapi.WithTenant(nil),
		agentapi.WithAuthenticator(authn),
		agentapi.WithToolsets(slot("mail", "mail", wire.ScopeUser), slot("crm", "crm", wire.ScopeTenant),
			agentapi.Toolset{Name: "plain", Type: "demo", Tools: sets["a"], Credential: agentapi.Credential{Status: wire.CredentialNotNeeded}}),
		agentapi.WithCredentials(svc),
		agentapi.WithLogger(log),
	}, opts...)})
	cr.models, cr.token = nil, keyCredU1
	t.Cleanup(func() {
		if strings.Contains(cr.logs.String(), credCanary) {
			t.Errorf("the canary is in the log:\n%s", cr.logs.String())
		}
	})
	return cr
}

// as sends a request with key, checking no answer holds the canary.
func (cr *credRig) as(key, method, path, body string) (*http.Response, []byte) {
	cr.t.Helper()
	resp, got := cr.do(method, path, body, "Authorization", "Bearer "+key)
	if bytes.Contains(got, []byte(credCanary)) {
		cr.t.Errorf("%s %s answered with the canary: %s", method, path, got)
	}
	return resp, got
}

func (cr *credRig) expect(key, method, path, body string, status int) []byte {
	cr.t.Helper()
	resp, got := cr.as(key, method, path, body)
	if resp.StatusCode != status {
		cr.t.Errorf("%s %s as %s = %d %s, want %d", method, path, key, resp.StatusCode, got, status)
	}
	return got
}

// list is what GET /v1/credentials says of each slot, as key.
func (cr *credRig) list(key string) map[string]wire.CredentialInfo {
	cr.t.Helper()
	l := decodeAs[wire.CredentialList](cr.t, cr.expect(key, "GET", "/v1/credentials", "", 200))
	out := map[string]wire.CredentialInfo{}
	for _, c := range l.Credentials {
		out[c.Slot+"/"+c.Scope] = c
	}
	return out
}

// toolsetStatus is GET /v1/toolsets' credential status of toolset, as key.
func (cr *credRig) toolsetStatus(key, toolset string) string {
	cr.t.Helper()
	for _, ts := range decodeAs[wire.ToolsetList](cr.t, cr.expect(key, "GET", "/v1/toolsets", "", 200)).Toolsets {
		if ts.Name == toolset {
			return ts.Credential.Status
		}
	}
	cr.t.Fatalf("no toolset %s", toolset)
	return ""
}

// connect runs the OAuth flow for key's own (or, with scope tenant, its
// tenant's) slot through the API, the provider and the callback, in one
// browser: it keeps the cookies the API sets.
func (cr *credRig) connect(key, slot string) {
	cr.t.Helper()
	resp, body := cr.as(key, "POST", "/v1/credentials/"+slot+"/connect", `{}`)
	if resp.StatusCode != http.StatusOK {
		cr.t.Fatalf("connect = %d %s", resp.StatusCode, body)
	}
	c := decodeAs[wire.ConnectResponse](cr.t, body)
	jar, _ := cookiejar.New(nil)
	jar.SetCookies(resp.Request.URL, resp.Cookies())
	nf := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := nf.Get(c.AuthorizeURL)
	if err != nil {
		cr.t.Fatal(err)
	}
	resp.Body.Close()
	resp, err = nf.Get(resp.Header.Get("Location"))
	if err != nil {
		cr.t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		cr.t.Fatalf("callback = %d", resp.StatusCode)
	}
}

// Criterion: scopes. A user sets their own user-scoped credential, but not
// the tenant's without credentials:admin; a key that is no user's has no
// own credential; and tenant B can neither list, set nor delete A's.
// The tenant the connect flows are kept in is no caller's: a key of that
// tenant, even with every scope, is refused (403) by every credentials
// endpoint, and GET /v1/toolsets does not read its slots' status.
func TestCredentialReservedTenant(t *testing.T) {
	t.Parallel()
	cr := newCredRig(t)
	key := `{"kind":"api_key","api_key":"k-` + credCanary + `"}`
	cr.expect(keyCredFlows, "GET", "/v1/credentials", "", 403)
	cr.expect(keyCredFlows, "PUT", "/v1/credentials/mail", key, 403)
	cr.expect(keyCredFlows, "PUT", "/v1/credentials/crm", key, 403)
	cr.expect(keyCredFlows, "DELETE", "/v1/credentials/crm", "", 403)
	cr.expect(keyCredFlows, "POST", "/v1/credentials/mail/connect", `{}`, 403)
	if l := cr.list(keyCredAdmin); l["crm/tenant"].Status != wire.CredentialNotConnected {
		t.Errorf("tenant A's list after the reserved tenant's calls = %+v", l)
	}
}

func TestCredentialScopes(t *testing.T) {
	t.Parallel()
	cr := newCredRig(t)
	key := `{"kind":"api_key","api_key":"k-` + credCanary + `"}`
	cr.expect(keyCredU1, "PUT", "/v1/credentials/mail", key, 200)
	cr.expect(keyCredU1, "PUT", "/v1/credentials/crm", key, 403)
	cr.expect(keyCredU1, "DELETE", "/v1/credentials/crm", "", 403)
	cr.expect(keyCredU1, "POST", "/v1/credentials/crm/connect", `{}`, 403)
	cr.expect(keyCredService, "PUT", "/v1/credentials/mail", key, 403)
	cr.expect(keyCredService, "POST", "/v1/credentials/mail/connect", `{}`, 403)
	cr.expect(keyCredAdmin, "PUT", "/v1/credentials/crm", key, 200)
	cr.expect(keyCredU1, "PUT", "/v1/credentials/mail", `{"scope":"tenant","kind":"api_key","api_key":"k"}`, 400) // mail is a user slot
	cr.expect(keyCredU1, "PUT", "/v1/credentials/nope", key, 404)

	if l := cr.list(keyCredU1); l["mail/user"].Status != wire.CredentialConnected || l["crm/tenant"].Status != wire.CredentialConnected || len(l) != 2 {
		t.Errorf("u1's list = %+v", l)
	}
	if l := cr.list(keyCredU2); l["mail/user"].Status != wire.CredentialNotConnected || l["crm/tenant"].Status != wire.CredentialConnected {
		t.Errorf("u2's list = %+v", l)
	}
	if l := cr.list(keyCredService); len(l) != 1 || l["crm/tenant"].Status != wire.CredentialConnected {
		t.Errorf("the service key's list = %+v", l)
	}

	// Tenant B: A's credentials are not there to list, replace or delete.
	if l := cr.list(keyCredB); l["mail/user"].Status != wire.CredentialNotConnected || l["crm/tenant"].Status != wire.CredentialNotConnected {
		t.Errorf("B's list = %+v", l)
	}
	cr.expect(keyCredB, "DELETE", "/v1/credentials/mail", "", 404)
	cr.expect(keyCredB, "DELETE", "/v1/credentials/crm", "", 404)
	cr.expect(keyCredB, "PUT", "/v1/credentials/crm", `{"kind":"api_key","api_key":"b's"}`, 200)
	ctxA := tenancy.With(context.Background(), cr.a)
	for _, at := range [][2]string{{"u1", "mail"}, {"", "crm"}} {
		c, _, err := cr.vault.Get(ctxA, at[0], at[1])
		if k, ok := c.(credentials.APIKey); err != nil || !ok || k.Key.Reveal() != "k-"+credCanary {
			t.Errorf("A's %v after B's requests: %v %v", at, c, err)
		}
	}
	// Deleting: a user their own, an admin the tenant's.
	cr.expect(keyCredU1, "DELETE", "/v1/credentials/mail", "", 204)
	cr.expect(keyCredU1, "DELETE", "/v1/credentials/mail", "", 404)
	cr.expect(keyCredAdmin, "DELETE", "/v1/credentials/crm?scope=tenant", "", 204)
	cr.expect(keyCredAdmin, "DELETE", "/v1/credentials/crm?scope=user", "", 400)
}

// Criterion (security fix, RFC 9700 §4.7): POST .../connect sets the
// cookie that binds the flow to the browser: HttpOnly, SameSite=Lax, the
// callback's path, Secure when the request came over TLS (to the server or
// a proxy saying so), never cached. The callback refuses a browser without
// it.
func TestCredentialConnectCookie(t *testing.T) {
	t.Parallel()
	cr := newCredRig(t)
	for _, tc := range []struct {
		name   string
		header []string
		secure bool
	}{
		{"http", nil, false},
		{"behind TLS", []string{"X-Forwarded-Proto", "https"}, true},
		{"proxied http", []string{"X-Forwarded-Proto", "http"}, false},
	} {
		resp, body := cr.do("POST", "/v1/credentials/mail/connect", `{}`, append([]string{"Authorization", "Bearer " + keyCredU1}, tc.header...)...)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: connect = %d %s", tc.name, resp.StatusCode, body)
		}
		cs := resp.Cookies()
		if len(cs) != 1 {
			t.Fatalf("%s: cookies %v", tc.name, resp.Header.Values("Set-Cookie"))
		}
		c := cs[0]
		if !strings.HasPrefix(c.Name, "agentkit_oauth_") || c.Value == "" || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode ||
			c.Path != connect.CallbackPath || c.MaxAge <= 0 || c.Secure != tc.secure || resp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: Set-Cookie %q, Cache-Control %q", tc.name, resp.Header.Values("Set-Cookie"), resp.Header.Get("Cache-Control"))
		}
		if strings.Contains(string(body), c.Value) {
			t.Errorf("%s: the body has the cookie's value", tc.name)
		}
	}
	// Another browser (no cookie) opening the authorization URL is refused.
	c := decodeAs[wire.ConnectResponse](t, cr.expect(keyCredU1, "POST", "/v1/credentials/mail/connect", `{}`, 200))
	nf := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := nf.Get(c.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp, err = nf.Get(resp.Header.Get("Location")); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || cr.list(keyCredU1)["mail/user"].Status != wire.CredentialNotConnected {
		t.Errorf("a callback from another browser = %d", resp.StatusCode)
	}
}

// Criterion: listing. GET /v1/credentials (and the connect answer, and
// GET /v1/toolsets) never hold a secret, and show needs_reconnect once a
// refresh is refused with invalid_grant (T044).
func TestCredentialListing(t *testing.T) {
	t.Parallel()
	cr := newCredRig(t)
	cr.expect(keyCredAdmin, "PUT", "/v1/credentials/crm", `{"kind":"basic","username":"me","password":"pw-`+credCanary+`"}`, 200)
	if cr.toolsetStatus(keyCredU1, "mail") != wire.CredentialNotConnected {
		t.Error("mail is connected before connecting it")
	}
	cr.connect(keyCredU1, "mail")
	l := cr.list(keyCredU1)
	if m := l["mail/user"]; m.Status != wire.CredentialConnected || m.Kind != wire.KindOAuth2 || !m.OAuth || m.Expiry.IsZero() || len(m.Toolsets) != 1 {
		t.Errorf("mail after connecting = %+v", m)
	}
	if l["crm/tenant"].Kind != wire.KindBasic {
		t.Errorf("crm = %+v", l["crm/tenant"])
	}
	for key, want := range map[string]string{keyCredU1: wire.CredentialConnected, keyCredU2: wire.CredentialNotConnected, keyCredService: wire.CredentialNotConnected} {
		if got := cr.toolsetStatus(key, "mail"); got != want {
			t.Errorf("toolset mail's status for %s = %s, want %s", key, got, want)
		}
	}
	if got := cr.toolsetStatus(keyCredU2, "crm"); got != wire.CredentialConnected {
		t.Errorf("toolset crm's status = %s", got)
	}

	// The provider now refuses the refresh token: the source marks it.
	cr.ts.SetOptions(credentialstest.TokenServerOptions{Hook: func(w http.ResponseWriter, _ *http.Request, form url.Values) bool {
		credentialstest.TokenError(w, http.StatusBadRequest, "invalid_grant", "revoked "+form.Encode())
		return true
	}})
	srcs, err := credentials.NewSources(cr.vault, credentials.WithClock(cr.clk), credentials.WithOAuth2App("mail", credentials.OAuth2App{
		ClientID: "app", ClientSecret: credentials.NewSecret("app-secret-" + credCanary), TokenURL: cr.ts.URL,
	}))
	if err != nil {
		t.Fatal(err)
	}
	src, err := srcs.Source("mail", "mail", credentials.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	cr.clk.Advance(2 * time.Hour)
	ctx := tenancy.WithUser(tenancy.With(context.Background(), cr.a), "u1")
	if _, err := src.Get(ctx); !errors.Is(err, credentials.ErrNotConnected) {
		t.Fatalf("a refused refresh = %v", err)
	}
	if m := cr.list(keyCredU1)["mail/user"]; m.Status != wire.CredentialNeedsReconnect {
		t.Errorf("mail after invalid_grant = %+v", m)
	}
	if got := cr.toolsetStatus(keyCredU1, "mail"); got != wire.CredentialNeedsReconnect {
		t.Errorf("toolset mail after invalid_grant = %s", got)
	}
	// Connecting again clears it.
	cr.ts.SetOptions(credentialstest.TokenServerOptions{Prefix: credCanary + "-"})
	cr.connect(keyCredU1, "mail")
	if m := cr.list(keyCredU1)["mail/user"]; m.Status != wire.CredentialConnected {
		t.Errorf("mail after connecting again = %+v", m)
	}
}

// Without WithCredentials the endpoints answer 404, and slot toolsets keep
// the status they were given.
func TestCredentialsOff(t *testing.T) {
	t.Parallel()
	r := newRig(t, echo(t), rigConfig{})
	for _, rt := range [][2]string{{"GET", "/v1/credentials"}, {"PUT", "/v1/credentials/x"}, {"DELETE", "/v1/credentials/x"}, {"POST", "/v1/credentials/x/connect"}} {
		if resp, body := r.do(rt[0], rt[1], `{}`); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s = %d %s", rt[0], rt[1], resp.StatusCode, body)
		}
	}
}
