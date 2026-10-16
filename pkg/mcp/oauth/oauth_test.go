package oauth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/mcp/oauth"
	"github.com/bsamanvith/agentkit/pkg/mcp/oauth/oauthtest"
)

var (
	alice = oauth.Owner{Tenant: "acme", User: "alice"}
	bob   = oauth.Owner{Tenant: "acme", User: "bob"}
	eve   = oauth.Owner{Tenant: "evil", User: "eve"}
)

type env struct {
	as     *oauthtest.AuthServer
	srv    oauth.Server
	c      *oauth.Connector
	store  *oauth.MemoryStore
	clk    *clocktest.Fake
	logs   *syncBuffer
	public *httptest.Server
}

type setup struct {
	as      oauthtest.AuthServerOptions
	res     oauthtest.ResourceOptions
	profile oauth.Profile
	scope   oauth.Scope
	// store, if set, is shared with another env (a second replica).
	store *oauth.MemoryStore
}

// newEnv starts agentkit's public endpoint (TLS, for CIMD), a fake
// authorization server and an MCP server it protects, and a connector.
func newEnv(t *testing.T, s setup) *env {
	t.Helper()
	e := &env{clk: clocktest.NewFake(time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)), logs: &syncBuffer{}}
	public := http.NotFoundHandler()
	e.public = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { public.ServeHTTP(w, r) }))
	t.Cleanup(e.public.Close)
	s.as.HTTPClient = e.public.Client()
	e.as = oauthtest.NewAuthServer(t, s.as)
	mcpSrv := oauthtest.Protect(t, e.as, "/mcp", echoServer(), s.res)
	e.as.SetResource(mcpSrv.URL + "/mcp")
	e.srv = oauth.Server{Name: "crm", URL: mcpSrv.URL + "/mcp", Scope: s.scope, Profile: s.profile}
	e.store = s.store
	if e.store == nil {
		e.store = &oauth.MemoryStore{}
	}
	c, err := oauth.New(oauth.Config{
		PublicURL: e.public.URL,
		Tokens:    e.store, Clients: e.store, States: e.store,
		Clock:  e.clk,
		Logger: slog.New(slog.NewJSONHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	if err != nil {
		t.Fatal(err)
	}
	e.c = c
	public = c.Handler()
	return e
}

func echoServer() http.Handler {
	s := mcp.NewServer(&mcp.Implementation{Name: "fake-crm", Version: "0"}, nil)
	type in struct {
		Text string `json:"text"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "echo"}, func(_ context.Context, _ *mcp.CallToolRequest, a in) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: a.Text}}}, nil, nil
	})
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil)
}

// browse plays the user's browser: it opens the authorization URL (the fake
// grants at once) and returns what came back to the callback.
func browse(t *testing.T, authURL string) (oauth.Callback, int) {
	t.Helper()
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cl.Get(authURL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		return oauth.Callback{}, resp.StatusCode
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return oauth.CallbackFrom(loc.Query()), resp.StatusCode
}

func (e *env) lookup(name string) (oauth.Server, error) {
	if name != e.srv.Name {
		return oauth.Server{}, errors.New("no such server")
	}
	return e.srv, nil
}

// connect runs the whole flow for owner and fails the test if it does not
// end connected.
func (e *env) connect(t *testing.T, owner oauth.Owner) {
	t.Helper()
	authURL, err := e.c.Begin(t.Context(), owner, e.srv, "/connections")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	cb, status := browse(t, authURL)
	if status != http.StatusFound {
		t.Fatalf("authorization server refused the request: HTTP %d", status)
	}
	res, err := e.c.Complete(t.Context(), owner, cb, e.lookup)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if res.Server != "crm" || res.ReturnTo != "/connections" {
		t.Fatalf("Complete = %+v", res)
	}
}

// call connects an MCP client as owner and calls echo.
func (e *env) call(t *testing.T, owner oauth.Owner) error {
	t.Helper()
	cl := mcp.NewClient(&mcp.Implementation{Name: "agentkit", Version: "0"}, nil)
	cs, err := cl.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: e.srv.URL, OAuthHandler: e.c.OAuthHandler(owner, e.srv), DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		return err
	}
	defer cs.Close()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"text": "hi"}})
	if err != nil {
		return err
	}
	if got := res.Content[0].(*mcp.TextContent).Text; got != "hi" {
		t.Fatalf("echo = %q", got)
	}
	return nil
}

var spec = oauthtest.AuthServerOptions{DCR: true, CIMD: true, Scopes: []string{"crm.read", "offline_access"}, TTL: time.Hour}

func TestConnectAndCall(t *testing.T) {
	t.Parallel()
	e := newEnv(t, setup{as: spec, res: oauthtest.ResourceOptions{Scopes: []string{"crm.read"}}})
	if err := e.call(t, alice); !errors.Is(err, oauth.ErrNotConnected) {
		t.Fatalf("call before connecting: %v, want ErrNotConnected", err)
	}
	e.connect(t, alice)
	if err := e.call(t, alice); err != nil {
		t.Fatalf("call after connecting: %v", err)
	}
	if err := e.call(t, bob); !errors.Is(err, oauth.ErrNotConnected) {
		t.Fatalf("bob uses alice's connection: %v", err)
	}
	q := e.as.LastAuthorize()
	if got := q.Get("scope"); got != "crm.read offline_access" {
		t.Errorf("scope = %q, want the challenge's plus offline_access", got)
	}
}

func TestDiscovery(t *testing.T) {
	t.Parallel()
	t.Run("well-known fallback", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, setup{as: spec, res: oauthtest.ResourceOptions{NoHeader: true}})
		e.connect(t, alice)
	})
	for name, as := range map[string]oauthtest.AuthServerOptions{
		"issuer mismatch": {DCR: true, WrongIssuer: true},
		"no PKCE":         {DCR: true, NoPKCE: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, setup{as: as})
			if _, err := e.c.Begin(t.Context(), alice, e.srv, ""); !errors.Is(err, oauth.ErrDiscovery) {
				t.Fatalf("Begin: %v, want ErrDiscovery", err)
			}
		})
	}
}

func TestRegistration(t *testing.T) {
	t.Parallel()
	t.Run("pre-registered first", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, setup{
			as:      oauthtest.AuthServerOptions{CIMD: true, DCR: true, Clients: map[string]string{"pre": "s3cret"}},
			profile: oauth.Profile{ClientID: "pre", ClientSecret: oauth.NewSecret("s3cret")},
		})
		e.connect(t, alice)
		if got := e.as.LastAuthorize().Get("client_id"); got != "pre" {
			t.Errorf("client_id = %q, want pre", got)
		}
		if reg, _, cimd := e.as.Counts(); reg != 0 || cimd != 0 {
			t.Errorf("registrations %d, CIMD fetches %d; want none", reg, cimd)
		}
	})
	t.Run("CIMD before DCR", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, setup{as: oauthtest.AuthServerOptions{CIMD: true, DCR: true}})
		e.connect(t, alice)
		if got, want := e.as.LastAuthorize().Get("client_id"), e.public.URL+oauth.ClientMetadataPath; got != want {
			t.Errorf("client_id = %q, want our metadata URL %q", got, want)
		}
		if reg, _, cimd := e.as.Counts(); reg != 0 || cimd == 0 {
			t.Errorf("registrations %d, CIMD fetches %d; want CIMD only", reg, cimd)
		}
	})
	t.Run("DCR once per tenant", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, setup{as: oauthtest.AuthServerOptions{DCR: true}})
		e.connect(t, alice)
		e.connect(t, bob)
		if reg, _, _ := e.as.Counts(); reg != 1 {
			t.Errorf("registrations = %d after two users of one tenant, want 1", reg)
		}
		e.connect(t, eve)
		if reg, _, _ := e.as.Counts(); reg != 2 {
			t.Errorf("registrations = %d after another tenant, want 2", reg)
		}
	})
	t.Run("none supported", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, setup{as: oauthtest.AuthServerOptions{}})
		if _, err := e.c.Begin(t.Context(), alice, e.srv, ""); !errors.Is(err, oauth.ErrNoRegistration) {
			t.Fatalf("Begin: %v, want ErrNoRegistration", err)
		}
	})
}

func TestPKCEAndResource(t *testing.T) {
	t.Parallel()
	e := newEnv(t, setup{as: spec})
	authURL, err := e.c.Begin(t.Context(), alice, e.srv, "")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(authURL)
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("resource") != e.srv.URL {
		t.Fatalf("authorization request %v lacks PKCE S256 or resource", q)
	}
	// The fake refuses either one missing or wrong.
	for name, edit := range map[string]func(url.Values){
		"no challenge":   func(q url.Values) { q.Del("code_challenge") },
		"plain":          func(q url.Values) { q.Set("code_challenge_method", "plain") },
		"wrong resource": func(q url.Values) { q.Set("resource", "https://other.example/mcp") },
	} {
		q2 := u.Query()
		edit(q2)
		u2 := *u
		u2.RawQuery = q2.Encode()
		if _, status := browse(t, u2.String()); status != http.StatusBadRequest {
			t.Errorf("%s: HTTP %d, want 400", name, status)
		}
	}
	// OmitResource leaves it out of both requests.
	e2 := newEnv(t, setup{as: oauthtest.AuthServerOptions{DCR: true}, profile: oauth.Profile{OmitResource: true}})
	e2.as.SetResource("")
	authURL, err = e2.c.Begin(t.Context(), alice, e2.srv, "")
	if err != nil {
		t.Fatal(err)
	}
	if u, _ := url.Parse(authURL); u.Query().Has("resource") {
		t.Errorf("OmitResource: authorization URL has resource")
	}
}

func TestState(t *testing.T) {
	t.Parallel()
	e := newEnv(t, setup{as: spec})
	begin := func(owner oauth.Owner) oauth.Callback {
		authURL, err := e.c.Begin(t.Context(), owner, e.srv, "")
		if err != nil {
			t.Fatal(err)
		}
		cb, _ := browse(t, authURL)
		return cb
	}

	cb := begin(alice)
	if _, err := e.c.Complete(t.Context(), alice, cb, e.lookup); err != nil {
		t.Fatal(err)
	}
	if _, err := e.c.Complete(t.Context(), alice, cb, e.lookup); !errors.Is(err, oauth.ErrBadState) {
		t.Errorf("reused state: %v, want ErrBadState", err)
	}

	cb = begin(alice)
	if _, err := e.c.Complete(t.Context(), bob, cb, e.lookup); !errors.Is(err, oauth.ErrBadState) {
		t.Errorf("another user's state: %v, want ErrBadState", err)
	}
	if _, err := e.c.Complete(t.Context(), alice, cb, e.lookup); !errors.Is(err, oauth.ErrBadState) {
		t.Errorf("state after another user tried it: %v, want ErrBadState (used once)", err)
	}

	cb = begin(alice)
	e.clk.Advance(11 * time.Minute)
	if _, err := e.c.Complete(t.Context(), alice, cb, e.lookup); !errors.Is(err, oauth.ErrBadState) {
		t.Errorf("expired state: %v, want ErrBadState", err)
	}

	cb = begin(alice)
	cb.Code, cb.Error = "", "access_denied"
	if _, err := e.c.Complete(t.Context(), alice, cb, e.lookup); !errors.Is(err, oauth.ErrDenied) {
		t.Errorf("user declined: %v, want ErrDenied", err)
	}
}

func TestIssuerParameter(t *testing.T) {
	t.Parallel()
	as := spec
	as.IssParam = true
	e := newEnv(t, setup{as: as})
	e.connect(t, alice) // the fake sends its iss

	authURL, err := e.c.Begin(t.Context(), bob, e.srv, "")
	if err != nil {
		t.Fatal(err)
	}
	cb, _ := browse(t, authURL)
	cb.Iss = "https://attacker.example"
	if _, err := e.c.Complete(t.Context(), bob, cb, e.lookup); !errors.Is(err, oauth.ErrBadState) {
		t.Fatalf("mixed-up issuer: %v, want ErrBadState", err)
	}
}

func TestRefresh(t *testing.T) {
	t.Parallel()
	t.Run("concurrent calls refresh once", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, setup{as: spec})
		e.connect(t, alice)
		e.clk.Advance(2 * time.Hour)
		var wg sync.WaitGroup
		toks := make([]string, 8)
		for i := range toks {
			wg.Go(func() {
				s, err := e.c.Token(t.Context(), alice, e.srv)
				if err != nil {
					t.Error(err)
				}
				toks[i] = s.Reveal()
			})
		}
		wg.Wait()
		if _, n, _ := e.as.Counts(); n != 1 {
			t.Errorf("refreshes = %d, want 1", n)
		}
		for _, tok := range toks[1:] {
			if tok != toks[0] {
				t.Fatalf("callers got different tokens")
			}
		}
	})
	t.Run("two replicas keep one result", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, setup{as: spec})
		e.connect(t, alice)
		e.clk.Advance(2 * time.Hour)
		// A second connector on the same store, as another replica.
		c2, err := oauth.New(oauth.Config{PublicURL: e.public.URL, Tokens: e.store, Clients: e.store, States: e.store, Clock: e.clk})
		if err != nil {
			t.Fatal(err)
		}
		s1, err1 := e.c.Token(t.Context(), alice, e.srv)
		s2, err2 := c2.Token(t.Context(), alice, e.srv)
		if err1 != nil || err2 != nil || s1.Reveal() != s2.Reveal() {
			t.Fatalf("replicas disagree: %v %v", err1, err2)
		}
	})
	t.Run("invalid_grant needs reconnect", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, setup{as: spec})
		e.connect(t, alice)
		e.as.RevokeRefresh()
		e.clk.Advance(2 * time.Hour)
		if _, err := e.c.Token(t.Context(), alice, e.srv); !errors.Is(err, oauth.ErrReconnectNeeded) {
			t.Fatalf("Token: %v, want ErrReconnectNeeded", err)
		}
		if err := e.call(t, alice); !errors.Is(err, oauth.ErrReconnectNeeded) {
			t.Fatalf("call: %v, want ErrReconnectNeeded", err)
		}
		e.connect(t, alice)
		if err := e.call(t, alice); err != nil {
			t.Fatalf("call after reconnecting: %v", err)
		}
	})
	t.Run("server rejects a fresh token", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, setup{as: spec})
		e.connect(t, alice)
		e.as.RevokeAccess()
		if err := e.call(t, alice); err != nil {
			t.Fatalf("call after the server dropped the token: %v (want refresh and retry)", err)
		}
	})
}

func TestNeedsMoreAccess(t *testing.T) {
	t.Parallel()
	e := newEnv(t, setup{as: spec, res: oauthtest.ResourceOptions{RequireScope: "crm.write"}, profile: oauth.Profile{Scopes: []string{"crm.read"}}})
	e.connect(t, alice)
	if err := e.call(t, alice); !errors.Is(err, oauth.ErrNeedsMoreAccess) {
		t.Fatalf("call: %v, want ErrNeedsMoreAccess", err)
	}
}

func TestCrossTenant(t *testing.T) {
	t.Parallel()
	e := newEnv(t, setup{as: oauthtest.AuthServerOptions{DCR: true, TTL: time.Hour, Scopes: []string{"offline_access"}}, scope: oauth.ScopeTenant})
	acme := oauth.Owner{Tenant: "acme"}
	e.connect(t, acme)
	if _, err := e.c.Token(t.Context(), oauth.Owner{Tenant: "evil"}, e.srv); !errors.Is(err, oauth.ErrNotConnected) {
		t.Fatalf("another tenant's token: %v, want ErrNotConnected", err)
	}
	if _, err := e.c.Token(t.Context(), acme, e.srv); err != nil {
		t.Fatal(err)
	}
	// The registered client is acme's: evil registers its own.
	e.connect(t, oauth.Owner{Tenant: "evil"})
	if reg, _, _ := e.as.Counts(); reg != 2 {
		t.Fatalf("registrations = %d, want one per tenant", reg)
	}
}

func TestProfiles(t *testing.T) {
	t.Parallel()
	t.Run("fixed endpoints and auth params", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, setup{as: oauthtest.AuthServerOptions{Clients: map[string]string{"g": "gs"}, TTL: time.Hour}})
		p := oauth.Profiles()["google"]
		p.AuthURL, p.TokenURL = e.as.URL+"/authorize", e.as.URL+"/token"
		p.ClientID, p.ClientSecret, p.Scopes = "g", oauth.NewSecret("gs"), []string{"gmail.readonly"}
		e.srv.Profile = p
		e.connect(t, alice)
		q := e.as.LastAuthorize()
		if q.Get("access_type") != "offline" || q.Get("prompt") != "consent" || q.Get("scope") != "gmail.readonly" {
			t.Errorf("authorization request %v: want Google's params and no refresh scope", q)
		}
	})
	t.Run("salesforce keeps instance_url and a default lifetime", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, setup{as: oauthtest.AuthServerOptions{Clients: map[string]string{"sf": "sfs"}, Extra: map[string]any{"instance_url": "https://acme.my.salesforce.com"}}})
		p := oauth.SalesforceEndpoints(oauth.Profiles()["salesforce"], e.as.URL)
		p.AuthURL, p.TokenURL = e.as.URL+"/authorize", e.as.URL+"/token"
		p.ClientID, p.ClientSecret = "sf", oauth.NewSecret("sfs")
		e.srv.Profile = p
		e.connect(t, alice)
		tok, err := e.store.Get(t.Context(), oauth.Key{Owner: alice, Server: "crm"})
		if err != nil {
			t.Fatal(err)
		}
		if tok.Extra["instance_url"] != "https://acme.my.salesforce.com" {
			t.Errorf("Extra = %v", tok.Extra)
		}
		if want := e.clk.Now().Add(time.Hour); !tok.Expiry.Equal(want) {
			t.Errorf("Expiry = %v, want %v (DefaultTTL)", tok.Expiry, want)
		}
		if !strings.Contains(e.as.LastAuthorize().Get("scope"), "refresh_token") {
			t.Errorf("scope %q lacks refresh_token", e.as.LastAuthorize().Get("scope"))
		}
	})
	t.Run("OnToken can refuse", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, setup{as: spec, profile: oauth.Profile{OnToken: func(*oauth.Token) error { return errors.New("no") }}})
		authURL, err := e.c.Begin(t.Context(), alice, e.srv, "")
		if err != nil {
			t.Fatal(err)
		}
		cb, _ := browse(t, authURL)
		if _, err := e.c.Complete(t.Context(), alice, cb, e.lookup); err == nil {
			t.Fatal("Complete succeeded though OnToken refused")
		}
		if _, err := e.c.Token(t.Context(), alice, e.srv); !errors.Is(err, oauth.ErrNotConnected) {
			t.Fatalf("Token: %v, want ErrNotConnected", err)
		}
	})
}

func TestClientMetadataDocument(t *testing.T) {
	t.Parallel()
	e := newEnv(t, setup{as: spec})
	resp, err := e.public.Client().Get(e.public.URL + oauth.ClientMetadataPath)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var doc struct {
		ClientID     string   `json:"client_id"`
		RedirectURIs []string `json:"redirect_uris"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if doc.ClientID != e.public.URL+oauth.ClientMetadataPath || len(doc.RedirectURIs) != 1 || doc.RedirectURIs[0] != e.c.RedirectURL() {
		t.Fatalf("metadata document = %+v", doc)
	}
}

// TestCanary seeds known token values and checks none appears in a log
// line, an error, a printed or marshaled token, or a stored pending flow.
func TestCanary(t *testing.T) {
	t.Parallel()
	e := newEnv(t, setup{as: spec})
	e.connect(t, alice)
	key := oauth.Key{Owner: alice, Server: "crm"}
	tok, err := e.store.Get(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	secrets := []string{tok.Access.Reveal(), tok.Refresh.Reveal(), tok.ClientSecret.Reveal()}

	var out []string
	js, _ := json.Marshal(tok)
	out = append(out, string(js), fmt.Sprintf("%v %+v %#v %s %q", tok, tok, tok, tok.Access, tok.Refresh))
	slog.New(slog.NewTextHandler(e.logs, nil)).Info("token", "tok", tok, "access", tok.Access)

	// Errors on every failure path.
	e.as.RevokeRefresh()
	e.as.RevokeAccess()
	if err := e.call(t, alice); err != nil {
		out = append(out, err.Error())
	}
	e.clk.Advance(2 * time.Hour)
	if _, err := e.c.Token(t.Context(), alice, e.srv); err != nil {
		out = append(out, err.Error())
	}
	out = append(out, e.logs.String())

	for _, o := range out {
		for _, s := range secrets {
			if s != "" && strings.Contains(o, s) {
				t.Fatalf("a secret leaked into %q", o)
			}
		}
	}
}

// FuzzComplete sends arbitrary callback parameters: none may connect anyone
// but the owner of a real, unused state with the code issued for it.
func FuzzComplete(f *testing.F) {
	f.Add("", "", "", "")
	f.Add("state", "code", "iss", "access_denied")
	f.Add("x", "\x00", "https://evil", "\"\\\n")
	f.Fuzz(func(t *testing.T, state, code, iss, errCode string) {
		store := &oauth.MemoryStore{}
		c, err := oauth.New(oauth.Config{PublicURL: "https://agentkit.example", Tokens: store, Clients: store, States: store})
		if err != nil {
			t.Fatal(err)
		}
		_ = store.PutState(t.Context(), "known", oauth.Pending{Owner: alice, Server: "crm", Expires: time.Now().Add(time.Hour)})
		cb := oauth.Callback{State: state, Code: code, Iss: iss, Error: errCode}
		_, err = c.Complete(t.Context(), bob, cb, func(string) (oauth.Server, error) { return oauth.Server{Name: "crm"}, nil })
		if err == nil {
			t.Fatalf("Complete(%+v) connected bob", cb)
		}
		if _, err := store.Get(t.Context(), oauth.Key{Owner: bob, Server: "crm"}); !errors.Is(err, oauth.ErrNotFound) {
			t.Fatalf("a token was stored for bob")
		}
	})
}

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
