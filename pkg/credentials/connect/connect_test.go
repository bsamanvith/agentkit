package connect_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/credentials/connect"
	"github.com/bsamanvith/agentkit/pkg/credentials/credentialstest"
	"github.com/bsamanvith/agentkit/pkg/credentials/file"
	"github.com/bsamanvith/agentkit/pkg/credentials/memory"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
	"github.com/bsamanvith/agentkit/pkg/toolset"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest"
)

// canary is in every secret of the tests: the client secret, every token
// the fake provider issues and every key a test stores. The rig checks that
// no log line has it.
const canary = "t045-canary-9b3e"

const (
	appID     = "connect-app"
	appSecret = "app-secret-" + canary
	returnTo  = "https://console.example.com/connections/done"
)

// syncBuffer is a log sink several goroutines write.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// rig is a service over a vault, the fake provider, and the callback on an
// httptest server (the redirect URI).
type rig struct {
	t     testing.TB
	ts    *credentialstest.TokenServer
	store credentials.BlobStore
	ring  *credentials.KeyRing
	vault credentials.Vault
	svc   *connect.Service
	clk   *clocktest.Fake
	cb    *httptest.Server
	logs  *syncBuffer
	// wrap, if set, is put around the callback handler (a test's
	// middleware).
	wrap func(http.Handler) http.Handler
	// cookies are the binding cookies Connect gave, by state: the browser
	// that started each flow.
	mu      sync.Mutex
	cookies map[string]*http.Cookie
}

func testRing(t testing.TB) *credentials.KeyRing {
	t.Helper()
	r, err := credentials.ParseKeyRing("k1:AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

type rigOption func(*rig, *connect.Config)

func withStore(s credentials.BlobStore) rigOption {
	return func(r *rig, _ *connect.Config) { r.store = s }
}

func newRig(t testing.TB, opts ...rigOption) *rig {
	t.Helper()
	r := &rig{t: t, clk: clocktest.NewFake(time.Now()), logs: &syncBuffer{}, ring: testRing(t), cookies: map[string]*http.Cookie{}}
	r.ts = credentialstest.NewTokenServer(t, credentialstest.TokenServerOptions{Prefix: canary + "-"})
	r.ts.AddClient(appID, appSecret)
	var svc *connect.Service
	r.cb = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		h := svc.Callback()
		if r.wrap != nil {
			h = r.wrap(h)
		}
		h.ServeHTTP(w, req)
	}))
	t.Cleanup(r.cb.Close)
	cfg := r.config()
	for _, o := range opts {
		o(r, &cfg)
	}
	if r.store == nil {
		r.store = memory.New()
	}
	r.vault = credentials.NewVault(r.store, credentials.NewSealer(r.ring))
	cfg.Vault, cfg.Store, cfg.Sealer = r.vault, r.store, credentials.NewSealer(r.ring)
	var err error
	if svc, err = connect.New(cfg); err != nil {
		t.Fatal(err)
	}
	r.svc = svc
	t.Cleanup(func() {
		if strings.Contains(r.logs.String(), canary) {
			t.Errorf("the canary is in the log:\n%s", r.logs.String())
		}
	})
	return r
}

func (r *rig) app() connect.App {
	return connect.App{
		ClientID: appID, ClientSecret: credentials.NewSecret(appSecret),
		AuthURL: r.ts.AuthURL, TokenURL: r.ts.URL, RedirectURL: r.cb.URL + connect.CallbackPath,
		Scopes: []string{"mail.read"}, AuthParams: map[string]string{"access_type": "offline"},
	}
}

func (r *rig) config() connect.Config {
	return connect.Config{
		Slots: []connect.Slot{
			{Name: "mail", Scope: credentials.ScopeUser, Toolsets: []string{"mail"}},
			{Name: "crm", Scope: credentials.ScopeTenant, Toolsets: []string{"crm", "crm-reports"}},
		},
		Apps:       map[string]connect.App{"mail": r.app(), "crm": r.app()},
		ReturnURLs: []string{returnTo},
		Clock:      r.clk,
		Logger:     slog.New(slog.NewJSONHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
}

func ctxOf(t testing.TB, tenant string) context.Context {
	t.Helper()
	tn, err := tenancy.NewTenant(tenant)
	if err != nil {
		t.Fatal(err)
	}
	return tenancy.With(context.Background(), tn)
}

// noFollow is a client that returns redirects instead of following them.
var noFollow = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// start connects (tenant, user, slot) and returns the authorization URL;
// the rig keeps the binding cookie for the callback.
func (r *rig) start(tenant, user, slot, ret string) string {
	r.t.Helper()
	return r.startOn(r.svc, tenant, user, slot, ret)
}

// startOn is start on another service (a replica).
func (r *rig) startOn(svc *connect.Service, tenant, user, slot, ret string) string {
	r.t.Helper()
	resp, cookie, err := svc.Connect(ctxOf(r.t, tenant), user, slot, wire.ConnectRequest{ReturnURL: ret})
	if err != nil {
		r.t.Fatalf("Connect: %v", err)
	}
	u, err := url.Parse(resp.AuthorizeURL)
	if err != nil || cookie == nil {
		r.t.Fatalf("Connect = %q, cookie %v", resp.AuthorizeURL, cookie)
	}
	r.mu.Lock()
	r.cookies[u.Query().Get("state")] = cookie
	r.mu.Unlock()
	return resp.AuthorizeURL
}

// cookieFor is the binding cookie of the flow whose state target's query
// has, nil for none.
func (r *rig) cookieFor(target string) *http.Cookie {
	u, err := url.Parse(target)
	if err != nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cookies[u.Query().Get("state")]
}

// browserRequest is the request of the browser that started the flow of
// target's state: target with its binding cookie.
func (r *rig) browserRequest(target string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if c := r.cookieFor(target); c != nil {
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
	return req
}

// authorize follows an authorization URL at the fake provider and returns
// where it sends the browser: the callback, with the code and state.
func (r *rig) authorize(authURL string) string {
	r.t.Helper()
	resp, err := noFollow.Get(authURL)
	if err != nil {
		r.t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		r.t.Fatalf("authorize: %d", resp.StatusCode)
	}
	return resp.Header.Get("Location")
}

// callback sends the request of the browser that started the flow to the
// callback.
func (r *rig) callback(target string) (*http.Response, string) {
	r.t.Helper()
	var cookies []*http.Cookie
	if c := r.cookieFor(target); c != nil {
		cookies = append(cookies, &http.Cookie{Name: c.Name, Value: c.Value})
	}
	return r.callbackWith(target, cookies...)
}

// callbackWith sends target to the callback from a browser holding
// cookies.
func (r *rig) callbackWith(target string, cookies ...*http.Cookie) (*http.Response, string) {
	r.t.Helper()
	req, err := http.NewRequestWithContext(r.t.Context(), http.MethodGet, target, nil)
	if err != nil {
		r.t.Fatal(err)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := noFollow.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), canary) || strings.Contains(resp.Header.Get("Location"), canary) {
		r.t.Errorf("the callback's answer has the canary: %s %s", resp.Header.Get("Location"), body)
	}
	return resp, string(body)
}

// stored returns the credential at (tenant, user, slot), nil for none.
func (r *rig) stored(tenant, user, slot string) credentials.Credential {
	r.t.Helper()
	c, _, err := r.vault.Get(ctxOf(r.t, tenant), user, slot)
	if errors.Is(err, credentials.ErrNotFound) {
		return nil
	} else if err != nil {
		r.t.Fatal(err)
	}
	return c
}

// connected runs a whole flow and checks it ends connected.
func (r *rig) connected(tenant, user, slot string) credentials.OAuth2 {
	r.t.Helper()
	resp, body := r.callback(r.authorize(r.start(tenant, user, slot, "")))
	if resp.StatusCode != http.StatusOK {
		r.t.Fatalf("callback: %d %s", resp.StatusCode, body)
	}
	o, ok := r.stored(tenant, user, slot).(credentials.OAuth2)
	if !ok {
		r.t.Fatalf("stored %T, want OAuth2", r.stored(tenant, user, slot))
	}
	return o
}

// whoami calls the test connector's whoami tool, built on a source over
// the rig's vault, as tenant and user: whom the upstream took it for.
func (r *rig) whoami(up *toolsettest.CredentialUpstream, slot string, scope credentials.Scope, tenant, user string) (string, error) {
	r.t.Helper()
	srcs, err := credentials.NewSources(r.vault, credentials.WithOAuth2App(slot, credentials.OAuth2App{
		ClientID: appID, ClientSecret: credentials.NewSecret(appSecret), TokenURL: r.ts.URL,
	}), credentials.WithClock(r.clk))
	if err != nil {
		r.t.Fatal(err)
	}
	src, err := srcs.Source(slot, slot, scope)
	if err != nil {
		r.t.Fatal(err)
	}
	settings, _ := json.Marshal(map[string]string{"base_url": up.URL})
	tools, err := toolsettest.CredentialEcho().Build(r.t.Context(), toolset.BuildInput{Settings: settings, Credentials: src})
	if err != nil {
		r.t.Fatal(err)
	}
	ctx := ctxOf(r.t, tenant)
	if user != "" {
		ctx = tenancy.WithUser(ctx, user)
	}
	for _, tl := range tools {
		if tl.Definition().Name == "whoami" {
			out, err := tl.Invoke(ctx, json.RawMessage(`{}`))
			if err != nil {
				return "", err
			}
			var v struct{ Who string }
			if err := json.Unmarshal(out, &v); err != nil {
				r.t.Fatal(err)
			}
			return v.Who, nil
		}
	}
	r.t.Fatal("no whoami tool")
	return "", nil
}

// Criterion: the full flow against the fake provider. A user connects
// their own account and an admin the tenant's; the vault holds the tokens
// for that tenant and user only, and a tool call uses them.
func TestFullFlow(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	up := toolsettest.NewCredentialUpstream(t)

	authURL := r.start("tenant-a", "u1", "mail", "")
	q := mustQuery(t, authURL)
	for k, want := range map[string]string{
		"response_type": "code", "client_id": appID, "redirect_uri": r.cb.URL + connect.CallbackPath,
		"code_challenge_method": "S256", "scope": "mail.read", "access_type": "offline",
	} {
		if q.Get(k) != want {
			t.Errorf("authorize URL %s = %q, want %q", k, q.Get(k), want)
		}
	}
	if len(q.Get("state")) < 43 { // 32 bytes, base64url
		t.Errorf("state %q is shorter than 256 bits", q.Get("state"))
	}
	resp, body := r.callback(r.authorize(authURL))
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "connected") {
		t.Fatalf("callback = %d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("Referrer-Policy") != "no-referrer" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("callback headers %v", resp.Header)
	}
	mail, ok := r.stored("tenant-a", "u1", "mail").(credentials.OAuth2)
	if !ok || !strings.HasPrefix(mail.AccessToken.Reveal(), canary) || mail.RefreshToken.IsZero() || mail.Expiry.IsZero() {
		t.Fatalf("stored %+v", r.stored("tenant-a", "u1", "mail"))
	}
	for _, other := range [][2]string{{"tenant-a", ""}, {"tenant-a", "u2"}, {"tenant-b", "u1"}} {
		if c := r.stored(other[0], other[1], "mail"); c != nil {
			t.Errorf("%v has a credential: %T", other, c)
		}
	}
	up.Accept("Authorization", "Bearer "+mail.AccessToken.Reveal(), "U1")
	if who, err := r.whoami(up, "mail", credentials.ScopeUser, "tenant-a", "u1"); err != nil || who != "U1" {
		t.Errorf("whoami as u1 = %q, %v", who, err)
	}
	if _, err := r.whoami(up, "mail", credentials.ScopeUser, "tenant-a", "u2"); !errors.Is(err, credentials.ErrNotConnected) {
		t.Errorf("whoami as u2 = %v, want not connected", err)
	}

	// The tenant's shared account: stored with no user.
	crm := r.connected("tenant-b", "", "crm")
	up.Accept("Authorization", "Bearer "+crm.AccessToken.Reveal(), "B")
	if who, err := r.whoami(up, "crm", credentials.ScopeTenant, "tenant-b", "someone"); err != nil || who != "B" {
		t.Errorf("whoami in tenant B = %q, %v", who, err)
	}
	if r.ts.Grants("authorization_code") != 2 {
		t.Errorf("%d code exchanges, want 2", r.ts.Grants("authorization_code"))
	}
	// Connecting again replaces the tokens.
	again := r.connected("tenant-a", "u1", "mail")
	if again.AccessToken.Reveal() == mail.AccessToken.Reveal() {
		t.Error("connecting again kept the old token")
	}
}

func mustQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query()
}

// Flows are kept in the vault's BlobStore: a flow started on one replica
// finishes on another over the same store, on each backend.
func TestFlowAcrossReplicas(t *testing.T) {
	t.Parallel()
	for name, store := range map[string]func(t *testing.T) (credentials.BlobStore, string){
		"memory": func(*testing.T) (credentials.BlobStore, string) { return memory.New(), "" },
		"file": func(t *testing.T) (credentials.BlobStore, string) {
			path := filepath.Join(t.TempDir(), "credentials.json")
			s, err := file.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			return s, path
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			bs, path := store(t)
			one := newRig(t, withStore(bs))
			// The other replica: its own vault and service over the store.
			cfg := one.config()
			cfg.Vault = credentials.NewVault(bs, credentials.NewSealer(one.ring))
			cfg.Store, cfg.Sealer = bs, credentials.NewSealer(one.ring)
			two, err := connect.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			cb := one.authorize(one.start("tenant-a", "u1", "mail", ""))
			state := mustQuery(t, cb).Get("state")
			if path != "" {
				if raw, err := os.ReadFile(path); err != nil || strings.Contains(string(raw), state) || !strings.Contains(string(raw), connect.FlowSlot) {
					t.Errorf("the file (%v) has the state in the clear, or no flow", err)
				}
			}
			// The browser comes back to the other replica.
			req := one.browserRequest(cb)
			rec := httptest.NewRecorder()
			two.Callback().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("callback on the other replica = %d %s", rec.Code, rec.Body)
			}
			if _, ok := one.stored("tenant-a", "u1", "mail").(credentials.OAuth2); !ok {
				t.Error("the tokens are not in the shared vault")
			}
			// And the state is used up for the first replica too.
			if resp, _ := one.callback(cb); resp.StatusCode != http.StatusBadRequest {
				t.Errorf("the state used on one replica = %d on the other", resp.StatusCode)
			}
		})
	}
}

// Criterion: PKCE. The token request carries the verifier whose S256 is
// the authorization request's challenge; a verifier that does not match is
// refused by the provider, and the callback reports a failure and stores
// nothing.
func TestPKCE(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	var mu sync.Mutex
	var verifiers []string
	tamper := false
	r.ts.SetOptions(credentialstest.TokenServerOptions{Prefix: canary + "-", Hook: func(_ http.ResponseWriter, _ *http.Request, form url.Values) bool {
		mu.Lock()
		defer mu.Unlock()
		verifiers = append(verifiers, form.Get("code_verifier"))
		if tamper {
			form.Set("code_verifier", strings.Repeat("x", 43))
		}
		return false
	}})

	authURL := r.start("tenant-a", "u1", "mail", "")
	challenge := mustQuery(t, authURL).Get("code_challenge")
	r.connectedFrom(authURL)
	if len(verifiers) != 1 || credentialstest.Challenge(verifiers[0]) != challenge {
		t.Fatalf("verifiers %q: want one whose S256 is the challenge %q", verifiers, challenge)
	}

	mu.Lock()
	tamper = true
	mu.Unlock()
	for _, ret := range []string{"", returnTo} {
		resp, body := r.callback(r.authorize(r.start("tenant-a", "u2", "mail", ret)))
		switch ret {
		case "":
			if resp.StatusCode != http.StatusBadGateway || !strings.Contains(body, "failed") {
				t.Errorf("a refused exchange = %d %s", resp.StatusCode, body)
			}
		default:
			if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusSeeOther || mustQuery(t, loc).Get("result") != wire.CallbackFailed {
				t.Errorf("a refused exchange with a return URL = %d %s", resp.StatusCode, loc)
			}
		}
	}
	if c := r.stored("tenant-a", "u2", "mail"); c != nil {
		t.Errorf("a refused exchange stored %T", c)
	}

	// A provider whose error echoes what it was sent: the body never
	// reaches the log or the page (the rig checks both for the canary).
	r.ts.SetOptions(credentialstest.TokenServerOptions{Prefix: canary + "-", Hook: func(w http.ResponseWriter, _ *http.Request, form url.Values) bool {
		credentialstest.TokenError(w, http.StatusBadRequest, "invalid_grant", "you sent "+form.Encode()+" "+canary)
		return true
	}})
	if resp, body := r.callback(r.authorize(r.start("tenant-a", "u3", "mail", ""))); resp.StatusCode != http.StatusBadGateway {
		t.Errorf("an echoing refusal = %d %s", resp.StatusCode, body)
	}
}

func (r *rig) connectedFrom(authURL string) {
	r.t.Helper()
	if resp, body := r.callback(r.authorize(authURL)); resp.StatusCode != http.StatusOK {
		r.t.Fatalf("callback = %d %s", resp.StatusCode, body)
	}
}

// Criterion: bad states. A used state is refused (after a success and
// after a failure), an expired one is, an unknown one is, and one from
// another tenant's flow stores into its own tenant whatever the request
// carries.
func TestBadState(t *testing.T) {
	t.Parallel()
	t.Run("reused", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		cb := r.authorize(r.start("tenant-a", "u1", "mail", ""))
		if resp, _ := r.callback(cb); resp.StatusCode != http.StatusOK {
			t.Fatalf("first use = %d", resp.StatusCode)
		}
		_, meta, _ := r.vault.Get(ctxOf(t, "tenant-a"), "u1", "mail")
		if resp, body := r.callback(cb); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "unknown") {
			t.Errorf("second use = %d %s", resp.StatusCode, body)
		}
		if _, again, _ := r.vault.Get(ctxOf(t, "tenant-a"), "u1", "mail"); again.Version != meta.Version {
			t.Errorf("the second use wrote: version %d, then %d", meta.Version, again.Version)
		}
	})
	t.Run("reused after a failure", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		cb := r.authorize(r.start("tenant-a", "u1", "mail", ""))
		u, _ := url.Parse(cb)
		good := u.Query()
		failed := url.Values{"state": {good.Get("state")}, "error": {"access_denied"}}
		u.RawQuery = failed.Encode()
		if resp, body := r.callback(u.String()); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "did not allow") {
			t.Fatalf("a denied first use = %d %s", resp.StatusCode, body)
		}
		// The right code, after the state failed once: refused.
		if resp, body := r.callback(cb); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "unknown") {
			t.Errorf("the code after a failure = %d %s", resp.StatusCode, body)
		}
		if c := r.stored("tenant-a", "u1", "mail"); c != nil || r.ts.Grants("authorization_code") != 0 {
			t.Errorf("stored %T after %d exchanges", c, r.ts.Grants("authorization_code"))
		}
	})
	t.Run("expired", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		cb := r.authorize(r.start("tenant-a", "u1", "mail", returnTo))
		r.clk.Advance(connect.DefaultStateTTL)
		resp, _ := r.callback(cb)
		if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusSeeOther || mustQuery(t, loc).Get("result") != wire.CallbackExpired {
			t.Errorf("an expired state = %d %s", resp.StatusCode, loc)
		}
		if c := r.stored("tenant-a", "u1", "mail"); c != nil || r.ts.Grants("authorization_code") != 0 {
			t.Errorf("stored %T after %d exchanges", c, r.ts.Grants("authorization_code"))
		}
		// Just before the expiry it still works.
		cb = r.authorize(r.start("tenant-a", "u1", "mail", ""))
		r.clk.Advance(connect.DefaultStateTTL - time.Second)
		if resp, body := r.callback(cb); resp.StatusCode != http.StatusOK {
			t.Errorf("a state a second before its expiry = %d %s", resp.StatusCode, body)
		}
	})
	t.Run("unknown", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		cb := r.authorize(r.start("tenant-a", "u1", "mail", ""))
		u, _ := url.Parse(cb)
		for _, q := range []url.Values{
			{"code": {u.Query().Get("code")}, "state": {"not-a-state-" + strings.Repeat("A", 40)}},
			{"code": {u.Query().Get("code")}},
			{"code": {u.Query().Get("code")}, "state": {""}},
			{"code": {u.Query().Get("code")}, "state": {u.Query().Get("state") + "x"}},
			{"code": {u.Query().Get("code")}, "state": {u.Query().Get("state"), u.Query().Get("state")}},
		} {
			u.RawQuery = q.Encode()
			if resp, body := r.callback(u.String()); resp.StatusCode != http.StatusBadRequest {
				t.Errorf("%v = %d %s", q, resp.StatusCode, body)
			}
		}
		if c := r.stored("tenant-a", "u1", "mail"); c != nil {
			t.Errorf("an unknown state stored %T", c)
		}
	})
	t.Run("two codes", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		u, _ := url.Parse(r.authorize(r.start("tenant-a", "u1", "mail", "")))
		q := u.Query()
		q.Add("code", q.Get("code"))
		u.RawQuery = q.Encode()
		if resp, body := r.callback(u.String()); resp.StatusCode != http.StatusBadGateway {
			t.Errorf("a callback with two codes = %d %s", resp.StatusCode, body)
		}
		if c := r.stored("tenant-a", "u1", "mail"); c != nil || r.ts.Grants("authorization_code") != 0 {
			t.Errorf("stored %T after %d exchanges", c, r.ts.Grants("authorization_code"))
		}
	})
	t.Run("another tenant's flow", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		// The callback's request comes as tenant A's user u1 (a console
		// session, say): the flow is tenant B's user u9.
		r.wrap = func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				ctx := tenancy.WithUser(ctxOf(t, "tenant-a"), "u1")
				next.ServeHTTP(w, req.WithContext(ctx))
			})
		}
		r.connectedFrom(r.start("tenant-b", "u9", "mail", ""))
		if _, ok := r.stored("tenant-b", "u9", "mail").(credentials.OAuth2); !ok {
			t.Error("tenant B's flow did not store into tenant B")
		}
		for _, u := range []string{"u1", "u9", ""} {
			if c := r.stored("tenant-a", u, "mail"); c != nil {
				t.Errorf("tenant A's %q got %T", u, c)
			}
		}
	})
}

// barrierStore holds every write of the flows until n reads of them have
// happened, once armed: n callbacks then all read the flow before any of
// them writes, the race a compare-and-swap has to settle.
type barrierStore struct {
	credentials.BlobStore
	n     int
	armed atomic.Bool
	mu    sync.Mutex
	gets  int
	all   chan struct{}
}

func (b *barrierStore) Get(ctx context.Context, user, slot string) (credentials.Blob, error) {
	if b.armed.Load() && slot == connect.FlowSlot {
		b.mu.Lock()
		if b.gets++; b.gets == b.n {
			close(b.all)
		}
		b.mu.Unlock()
	}
	return b.BlobStore.Get(ctx, user, slot)
}

func (b *barrierStore) Put(ctx context.Context, user, slot string, blob credentials.Blob, ifVersion int64) (credentials.Meta, error) {
	if b.armed.Load() && slot == connect.FlowSlot {
		<-b.all
	}
	return b.BlobStore.Put(ctx, user, slot, blob, ifVersion)
}

// Criterion: a state is used once, also by callbacks that come at the same
// time: each reads the flow before any writes, and only one exchanges the
// code and connects.
func TestStateRace(t *testing.T) {
	t.Parallel()
	const n = 6
	bs := &barrierStore{BlobStore: memory.New(), n: n, all: make(chan struct{})}
	r := newRig(t, withStore(bs))
	cb := r.authorize(r.start("tenant-a", "u1", "mail", ""))
	bs.armed.Store(true)
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			rec := httptest.NewRecorder()
			r.svc.Callback().ServeHTTP(rec, r.browserRequest(cb))
			codes[i] = rec.Code
		})
	}
	wg.Wait()
	ok := 0
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			ok++
		case http.StatusBadRequest:
		default:
			t.Errorf("a callback answered %d", c)
		}
	}
	if ok != 1 || r.ts.Grants("authorization_code") != 1 {
		t.Errorf("%d callbacks connected, %d exchanged the code (answers %v): want one", ok, r.ts.Grants("authorization_code"), codes)
	}
}

// After the key ring drops the key the pending flows were sealed with, new
// flows still start and finish (the old buckets are replaced), and an old
// flow's state is unknown, not a server error.
func TestFlowsAfterKeyRotation(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	// u1's flows are kept in one blob, sealed under the old key.
	old := r.authorize(r.start("tenant-a", "u1", "mail", ""))
	ring, err := credentials.ParseKeyRing("k2:ICEiIyQlJicoKSorLC0uLzAxMjM0NTY3ODk6Ozw9Pj8")
	if err != nil {
		t.Fatal(err)
	}
	cfg := r.config()
	cfg.Store, cfg.Sealer = r.store, credentials.NewSealer(ring)
	cfg.Vault = credentials.NewVault(r.store, cfg.Sealer)
	rotated, err := connect.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	serve := func(target string) int {
		rec := httptest.NewRecorder()
		rotated.Callback().ServeHTTP(rec, r.browserRequest(target))
		return rec.Code
	}
	// u1 starts again: their blob, unreadable now, is replaced.
	again := r.authorize(r.startOn(rotated, "tenant-a", "u1", "mail", ""))
	if code := serve(old); code != http.StatusBadRequest {
		t.Errorf("a flow sealed under the dropped key = %d, want 400", code)
	}
	if code := serve(again); code != http.StatusOK {
		t.Errorf("a flow started after the rotation = %d", code)
	}
	if _, _, err := cfg.Vault.Get(ctxOf(t, "tenant-a"), "u1", "mail"); err != nil {
		t.Errorf("the tokens after the rotation: %v", err)
	}
}

// No request acts as the tenant the flows are kept under, whatever
// credential put it there.
func TestReservedTenant(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := ctxOf(t, connect.FlowTenant)
	_, errList := r.svc.List(ctx, "flows-00")
	_, errStatus := r.svc.Status(ctx, "flows-00", "mail")
	_, errPut := r.svc.Put(ctx, "flows-00", "mail", wire.PutCredentialRequest{Kind: wire.KindAPIKey, APIKey: "k"})
	errDelete := r.svc.Delete(ctx, "", "crm")
	_, _, errConnect := r.svc.Connect(ctx, "flows-00", "mail", wire.ConnectRequest{})
	for name, err := range map[string]error{"List": errList, "Status": errStatus, "Put": errPut, "Delete": errDelete, "Connect": errConnect} {
		if !errors.Is(err, agentapi.ErrCredentialForbidden) {
			t.Errorf("%s in the flows' tenant = %v, want forbidden", name, err)
		}
	}
}

// Criterion: a return URL not on the allowlist is refused when the flow
// starts; one on it gets the result in its query, its own query kept.
func TestReturnURL(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	for _, bad := range []string{
		"https://evil.example.com/connections/done",
		"https://console.example.com/other",
		"https://console.example.com/connections/done/more",
		"http://console.example.com/connections/done",
		"https://user@console.example.com/connections/done",
		"https://console.example.com/connections/done#x",
		"https://console.example.com.evil.example.com/connections/done",
		"//console.example.com/connections/done",
		"javascript:alert(1)",
		"/connections/done",
	} {
		_, _, err := r.svc.Connect(ctxOf(t, "tenant-a"), "u1", "mail", wire.ConnectRequest{ReturnURL: bad})
		if !errors.Is(err, agentapi.ErrCredentialInvalid) {
			t.Errorf("return URL %q: %v, want refused", bad, err)
		}
	}
	resp, _ := r.callback(r.authorize(r.start("tenant-a", "u1", "mail", returnTo+"?popup=1")))
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("callback = %d %v", resp.StatusCode, resp.Header)
	}
	if q := loc.Query(); loc.Host != "console.example.com" || q.Get("result") != wire.CallbackConnected || q.Get("slot") != "mail" || q.Get("popup") != "1" || q.Has("code") {
		t.Errorf("returned to %s", loc)
	}
	r.ts.SetOptions(credentialstest.TokenServerOptions{Deny: true})
	resp, _ = r.callback(r.authorize(r.start("tenant-a", "u1", "mail", returnTo)))
	if q := mustQuery(t, resp.Header.Get("Location")); q.Get("result") != wire.CallbackDenied {
		t.Errorf("a denied flow returned with %v", q)
	}
}

// Setting, listing and deleting through the service: the kinds, their
// checks, and statuses; a stored value is never listed.
func TestPutListDelete(t *testing.T) {
	t.Parallel()
	var checked []string
	r := newRig(t, func(_ *rig, c *connect.Config) {
		c.CheckTokenURL = func(raw string) error {
			checked = append(checked, raw)
			if strings.Contains(raw, "169.254.169.254") {
				return errors.New("blocked")
			}
			return nil
		}
	})
	ctx := ctxOf(t, "tenant-a")
	info, err := r.svc.Put(ctx, "", "crm", wire.PutCredentialRequest{Kind: wire.KindAPIKey, APIKey: "key-" + canary})
	if err != nil || info.Status != wire.CredentialConnected || info.Kind != wire.KindAPIKey || info.Scope != wire.ScopeTenant || info.Version != 1 {
		t.Fatalf("Put = %+v, %v", info, err)
	}
	if info, err = r.svc.Put(ctx, "", "crm", wire.PutCredentialRequest{Kind: wire.KindBasic, Username: "me", Password: "pw-" + canary}); err != nil || info.Version != 2 {
		t.Fatalf("Put over it = %+v, %v", info, err)
	}
	for name, req := range map[string]wire.PutCredentialRequest{
		"no kind":                    {APIKey: "k"},
		"unknown kind":               {Kind: "token", APIKey: "k"},
		"oauth2":                     {Kind: wire.KindOAuth2},
		"empty key":                  {Kind: wire.KindAPIKey},
		"a key with a password":      {Kind: wire.KindAPIKey, APIKey: "k", Password: "p"},
		"client credentials, no url": {Kind: wire.KindClientCredentials, ClientID: "c", ClientSecret: "s"},
		"http token url":             {Kind: wire.KindClientCredentials, ClientID: "c", ClientSecret: "s", TokenURL: "http://idp.example.com/token"},
		"blocked token url":          {Kind: wire.KindClientCredentials, ClientID: "c", ClientSecret: "s", TokenURL: "https://169.254.169.254/token"},
		"token url with user":        {Kind: wire.KindClientCredentials, ClientID: "c", ClientSecret: "s", TokenURL: "https://u:" + canary + "@idp.example.com/token"},
	} {
		if _, err := r.svc.Put(ctx, "u1", "mail", req); !errors.Is(err, agentapi.ErrCredentialInvalid) || strings.Contains(err.Error(), canary) {
			t.Errorf("%s: %v, want invalid without the value", name, err)
		}
	}
	if len(checked) != 1 || !strings.Contains(checked[0], "169.254") {
		t.Errorf("token URLs checked: %q", checked)
	}
	if _, err := r.svc.Put(ctx, "u1", "mail", wire.PutCredentialRequest{Kind: wire.KindClientCredentials, ClientID: "c", ClientSecret: "s-" + canary, TokenURL: "https://idp.example.com/token", Scopes: []string{"a"}}); err != nil {
		t.Fatal(err)
	}

	list, err := r.svc.List(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(list)
	if strings.Contains(string(b), canary) || len(list) != 2 {
		t.Fatalf("list = %s", b)
	}
	if list[0].Slot != "crm" || list[0].Kind != wire.KindBasic || len(list[0].Toolsets) != 2 || !list[0].OAuth ||
		list[1].Slot != "mail" || list[1].Scope != wire.ScopeUser || list[1].Kind != wire.KindClientCredentials {
		t.Errorf("list = %s", b)
	}
	// Without a user, only the tenant's; another tenant sees nothing.
	if l, _ := r.svc.List(ctx, ""); len(l) != 1 {
		t.Errorf("list with no user = %+v", l)
	}
	if l, _ := r.svc.List(ctxOf(t, "tenant-b"), "u1"); len(l) != 2 || l[0].Status != wire.CredentialNotConnected || l[1].Status != wire.CredentialNotConnected {
		t.Errorf("tenant B's list = %+v", l)
	}
	if err := r.svc.Delete(ctxOf(t, "tenant-b"), "", "crm"); !errors.Is(err, agentapi.ErrCredentialNotFound) {
		t.Errorf("tenant B deleting A's = %v", err)
	}
	if err := r.svc.Delete(ctx, "", "crm"); err != nil {
		t.Fatal(err)
	}
	if st, _ := r.svc.Status(ctx, "", "crm"); st != wire.CredentialNotConnected {
		t.Errorf("after delete: %s", st)
	}
	if err := r.svc.Delete(ctx, "", "crm"); !errors.Is(err, agentapi.ErrCredentialNotFound) {
		t.Errorf("deleting twice = %v", err)
	}
	if _, _, err := r.svc.Connect(ctx, "u1", "nope", wire.ConnectRequest{}); !errors.Is(err, agentapi.ErrCredentialInvalid) {
		t.Errorf("connecting a slot without an app = %v", err)
	}
}

// New refuses what cannot work.
func TestNewRefuses(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	base := func() connect.Config {
		c := r.config()
		c.Vault, c.Store, c.Sealer = r.vault, r.store, credentials.NewSealer(r.ring)
		return c
	}
	for name, change := range map[string]func(*connect.Config){
		"no vault":         func(c *connect.Config) { c.Vault = nil },
		"reserved slot":    func(c *connect.Config) { c.Slots = append(c.Slots, connect.Slot{Name: connect.FlowSlot}) },
		"bad scope":        func(c *connect.Config) { c.Slots = append(c.Slots, connect.Slot{Name: "x", Scope: "all"}) },
		"app of no slot":   func(c *connect.Config) { c.Apps["other"] = r.app() },
		"app with no URL":  func(c *connect.Config) { a := r.app(); a.AuthURL = ""; c.Apps["mail"] = a },
		"bad return URL":   func(c *connect.Config) { c.ReturnURLs = []string{"/relative"} },
		"app, no secret":   func(c *connect.Config) { a := r.app(); a.ClientSecret = credentials.Secret{}; c.Apps["mail"] = a },
		"negative timeout": func(c *connect.Config) { c.StateTTL = -time.Second },
	} {
		c := base()
		change(&c)
		if _, err := connect.New(c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Criterion: the callback's query, fuzzed, never panics and never stores.
// Each input runs against a live flow: "{state}" in it is the flow's
// state, so inputs reach the code exchange too (with codes the provider
// never issued).
func FuzzCallback(f *testing.F) {
	for _, seed := range []string{
		"", "state={state}", "state={state}&code=code-1", "state={state}&error=access_denied",
		"state={state}&state={state}&code=x", "code=x", "state=%zz", "state={state}&code=",
		"state={state}&code=" + strings.Repeat("a", 9000), "state=" + strings.Repeat("%", 300),
	} {
		f.Add(seed)
	}
	r := newRig(f)
	f.Fuzz(func(t *testing.T, raw string) {
		authURL, cookie, err := r.svc.Connect(ctxOf(t, "tenant-a"), "u1", "mail", wire.ConnectRequest{})
		if err != nil {
			t.Fatal(err)
		}
		state := mustQuery(t, authURL.AuthorizeURL).Get("state")
		req := httptest.NewRequest(http.MethodGet, connect.CallbackPath, nil)
		req.URL.RawQuery = strings.ReplaceAll(raw, "{state}", state)
		req.AddCookie(&http.Cookie{Name: cookie.Name, Value: cookie.Value}) // the browser that started it
		rec := httptest.NewRecorder()
		r.svc.Callback().ServeHTTP(rec, req)
		if rec.Code == http.StatusOK || rec.Code == http.StatusSeeOther {
			t.Errorf("%q: %d", raw, rec.Code)
		}
		if _, _, err := r.vault.Get(ctxOf(t, "tenant-a"), "u1", "mail"); !errors.Is(err, credentials.ErrNotFound) {
			t.Fatalf("%q stored something (%v)", raw, err)
		}
	})
}

// Criterion (security fix, RFC 9700 §4.7): a flow is bound to the browser
// that started it. Connect gives a cookie (random, HttpOnly, SameSite=Lax,
// the callback's path only, the flow's lifetime) and keeps only its hash;
// the callback refuses a browser without it, or with another's, before
// anything is exchanged or stored, and clears it.
func TestBrowserBinding(t *testing.T) {
	t.Parallel()
	t.Run("the cookie", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		resp, c, err := r.svc.Connect(ctxOf(t, "tenant-a"), "u1", "mail", wire.ConnectRequest{})
		if err != nil {
			t.Fatal(err)
		}
		state := mustQuery(t, resp.AuthorizeURL).Get("state")
		if c.Name != connect.CookieName(state) || !strings.HasPrefix(c.Name, "agentkit_oauth_") || len(c.Value) < 43 ||
			!c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != connect.CallbackPath ||
			c.MaxAge != int(connect.DefaultStateTTL/time.Second) || c.Domain != "" {
			t.Errorf("cookie %+v", c)
		}
		if strings.Contains(resp.AuthorizeURL, c.Value) {
			t.Error("the authorization URL has the cookie's value")
		}
		// A browser keeps it for the callback only.
		jar, _ := cookiejar.New(nil)
		api, _ := url.Parse(r.cb.URL + "/v1/credentials/mail/connect")
		jar.SetCookies(api, []*http.Cookie{c})
		cbURL, _ := url.Parse(r.cb.URL + connect.CallbackPath)
		if got := jar.Cookies(cbURL); len(got) != 1 || got[0].Value != c.Value {
			t.Errorf("the callback gets %v", got)
		}
		if got := jar.Cookies(api); len(got) != 0 {
			t.Errorf("the API gets %v", got)
		}
		// The flow keeps the SHA-256 of the value, never the value.
		bucket := "flows-" + state[:32]
		b, err := r.store.Get(ctxOf(t, connect.FlowTenant), bucket, connect.FlowSlot)
		if err != nil {
			t.Fatal(err)
		}
		ft, _ := tenancy.NewTenant(connect.FlowTenant)
		plain, _, err := credentials.NewSealer(r.ring).Open(credentials.Key{Tenant: ft, User: bucket, Slot: connect.FlowSlot}, b.Data)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(c.Value))
		if strings.Contains(string(plain), c.Value) || !strings.Contains(string(plain), hex.EncodeToString(sum[:])) {
			t.Errorf("the flow keeps %s", plain)
		}
		// That browser finishes the flow, and the callback clears the cookie.
		cl := &http.Client{Jar: jar, CheckRedirect: noFollow.CheckRedirect}
		got, err := cl.Get(r.authorize(resp.AuthorizeURL))
		if err != nil {
			t.Fatal(err)
		}
		got.Body.Close()
		if got.StatusCode != http.StatusOK {
			t.Fatalf("callback with the cookie = %d", got.StatusCode)
		}
		cleared := false
		for _, sc := range got.Cookies() {
			cleared = cleared || (sc.Name == c.Name && sc.MaxAge < 0 && sc.Path == c.Path)
		}
		if !cleared || len(jar.Cookies(cbURL)) != 0 {
			t.Errorf("the cookie is not cleared: %v", got.Header.Values("Set-Cookie"))
		}
		if _, ok := r.stored("tenant-a", "u1", "mail").(credentials.OAuth2); !ok {
			t.Error("the browser that started the flow did not connect")
		}
	})
	// The attack: a caller starts a flow for their own slot and gets
	// someone else to open its authorization URL and consent.
	refused := func(t *testing.T, r *rig, target string, cookies ...*http.Cookie) {
		t.Helper()
		resp, body := r.callbackWith(target, cookies...)
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "not started in this browser") || resp.Header.Get("Location") != "" {
			t.Errorf("callback with %v = %d %s %s", cookies, resp.StatusCode, resp.Header.Get("Location"), body)
		}
		if c := r.stored("tenant-a", "attacker", "mail"); c != nil || r.ts.Grants("authorization_code") != 0 {
			t.Errorf("stored %T after %d exchanges", c, r.ts.Grants("authorization_code"))
		}
		// The state is used up, also for the browser that started it.
		if resp, _ := r.callback(target); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("the state after a refused callback = %d", resp.StatusCode)
		}
	}
	t.Run("no cookie", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		for _, ret := range []string{"", returnTo} {
			refused(t, r, r.authorize(r.start("tenant-a", "attacker", "mail", ret)))
		}
	})
	t.Run("another browser's cookie", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		mine := r.authorize(r.start("tenant-a", "u1", "mail", ""))
		own := r.cookieFor(mine)
		// The victim's own cookie, under its own name and under the flow's.
		theirs := r.authorize(r.start("tenant-a", "attacker", "mail", ""))
		refused(t, r, theirs, &http.Cookie{Name: own.Name, Value: own.Value})
		theirs = r.authorize(r.start("tenant-a", "attacker", "mail", ""))
		refused(t, r, theirs, &http.Cookie{Name: r.cookieFor(theirs).Name, Value: own.Value})
		// The flow's own cookie name, its value changed.
		theirs = r.authorize(r.start("tenant-a", "attacker", "mail", ""))
		refused(t, r, theirs, &http.Cookie{Name: r.cookieFor(theirs).Name, Value: r.cookieFor(theirs).Value + "x"})
		// The victim's own flow still connects their account.
		if resp, body := r.callback(mine); resp.StatusCode != http.StatusOK {
			t.Errorf("the victim's own flow = %d %s", resp.StatusCode, body)
		}
	})
}

// Criterion (security fix): pending flows are bounded per caller. One
// caller starting many flows drops only their own oldest: other users'
// and other tenants' pending flows still finish.
func TestPendingFlowsPerCaller(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	start := func(tenant, user, slot string) string {
		r.clk.Advance(time.Millisecond) // each flow is newer than the last
		return r.start(tenant, user, slot, "")
	}
	victims := [][3]string{
		{"tenant-a", "u1", "mail"},
		{"tenant-a", "", "crm"},
		{"tenant-b", "u1", "mail"},
		{"tenant-b", "attacker", "mail"}, // same user id, another tenant
	}
	pending := make([]string, len(victims))
	for i, v := range victims {
		pending[i] = start(v[0], v[1], v[2])
	}
	// Enough that, when flows were kept in 64 buckets by state, every
	// victim's bucket got some of them, and a full one dropped its oldest.
	const n = 1000
	first := start("tenant-a", "attacker", "mail")
	var last string
	for range n {
		last = start("tenant-a", "attacker", "mail")
	}
	// No victim's flows share a blob with the caller's, so no write of
	// the caller's can drop them.
	ft, _ := tenancy.NewTenant(connect.FlowTenant)
	for i, v := range victims {
		bucket := "flows-" + mustQuery(t, pending[i]).Get("state")[:32]
		b, err := r.store.Get(ctxOf(t, connect.FlowTenant), bucket, connect.FlowSlot)
		if err != nil {
			t.Fatal(err)
		}
		plain, _, err := credentials.NewSealer(r.ring).Open(credentials.Key{Tenant: ft, User: bucket, Slot: connect.FlowSlot}, b.Data)
		if err != nil {
			t.Fatal(err)
		}
		var kept map[string]struct{ Tenant, User string }
		if err := json.Unmarshal(plain, &kept); err != nil {
			t.Fatal(err)
		}
		for _, fl := range kept {
			if fl.Tenant != v[0] || fl.User != v[1] {
				t.Errorf("%v's flows are kept with %s/%s's", v, fl.Tenant, fl.User)
				break
			}
		}
	}
	for i, v := range victims {
		if resp, body := r.callback(r.authorize(pending[i])); resp.StatusCode != http.StatusOK {
			t.Errorf("%v's flow after %d flows of another caller = %d %s", v, n, resp.StatusCode, body)
		}
	}
	if resp, _ := r.callback(r.authorize(first)); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("the caller's own oldest flow = %d, want dropped", resp.StatusCode)
	}
	if resp, body := r.callback(r.authorize(last)); resp.StatusCode != http.StatusOK {
		t.Errorf("the caller's newest flow = %d %s", resp.StatusCode, body)
	}
	// A caller keeps 16 pending: starting a 17th drops the oldest.
	recent := make([]string, 17)
	for i := range recent {
		recent[i] = start("tenant-a", "attacker", "mail")
	}
	if resp, _ := r.callback(r.authorize(recent[0])); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("the 17th newest flow of a caller = %d, want dropped", resp.StatusCode)
	}
	if resp, body := r.callback(r.authorize(recent[1])); resp.StatusCode != http.StatusOK {
		t.Errorf("the 16th newest flow of a caller = %d %s, want kept", resp.StatusCode, body)
	}
}
