package oauth_test

// T095 part 2: OIDC discovery, the pinned issuer, the DCR flight per
// tenant, the split flow (Start, AuthCodeURL, Finish) that the agent API's
// connect flows use, and the vault as the token and client store.

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/credentials/memory"
	"github.com/bsamanvith/agentkit/pkg/mcp/oauth"
	"github.com/bsamanvith/agentkit/pkg/mcp/oauth/oauthtest"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

func TestDiscoveryOIDC(t *testing.T) {
	t.Parallel()
	t.Run("found", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, setup{as: oauthtest.AuthServerOptions{DCR: true, OIDC: true}})
		e.connect(t, alice)
	})
	t.Run("issuer mismatch refused", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, setup{as: oauthtest.AuthServerOptions{DCR: true, OIDC: true, WrongIssuer: true}})
		if _, err := e.c.Begin(t.Context(), alice, e.srv, ""); !errors.Is(err, oauth.ErrDiscovery) {
			t.Fatalf("Begin: %v, want ErrDiscovery", err)
		}
		if _, err := e.c.Issuer(t.Context(), e.srv); !errors.Is(err, oauth.ErrDiscovery) {
			t.Fatalf("Issuer: %v, want ErrDiscovery", err)
		}
	})
}

func TestIssuer(t *testing.T) {
	t.Parallel()
	e := newEnv(t, setup{as: spec})
	iss, err := e.c.Issuer(t.Context(), e.srv)
	if err != nil || iss != e.as.URL {
		t.Fatalf("Issuer = %q, %v; want %q", iss, err, e.as.URL)
	}
	// Pinned to another issuer (the one an admin added the server with):
	// refused, and a pre-registered client is never sent to it.
	pinned := e.srv
	pinned.Profile = oauth.Profile{Issuer: "https://as.example", ClientID: "pre", ClientSecret: oauth.NewSecret("s")}
	if _, err := e.c.Issuer(t.Context(), pinned); !errors.Is(err, oauth.ErrDiscovery) {
		t.Fatalf("Issuer with another pinned: %v, want ErrDiscovery", err)
	}
	if _, err := e.c.Start(t.Context(), alice, pinned); err == nil {
		t.Fatal("Start sent a pre-registered client to an authorization server it is not pinned to")
	}
	// Without a pre-registered client too (CIMD or DCR): the browser is
	// never sent to, and no client registered at, another authorization
	// server than the pinned one.
	dcr := newEnv(t, setup{as: oauthtest.AuthServerOptions{DCR: true}})
	pinned = dcr.srv
	pinned.Profile = oauth.Profile{Issuer: "https://as.example"}
	if _, err := dcr.c.Start(t.Context(), alice, pinned); !errors.Is(err, oauth.ErrDiscovery) {
		t.Fatalf("Start with another issuer pinned: %v, want ErrDiscovery", err)
	}
	if _, err := dcr.store.GetClient(t.Context(), alice.Tenant, dcr.as.URL); !errors.Is(err, oauth.ErrNotFound) {
		t.Fatalf("a client was registered at an authorization server the server is not pinned to: %v", err)
	}
}

// Two tenants registering at the same moment each register their own
// client: the registration flight is per tenant. Both requests must reach
// the authorization server while the first is still held there.
func TestDCRFlightPerTenant(t *testing.T) {
	t.Parallel()
	var (
		mu       sync.Mutex
		arrived  int
		bothIn   = make(chan struct{})
		released = make(chan struct{})
	)
	hold := func() {
		mu.Lock()
		arrived++
		if arrived == 2 {
			close(bothIn)
		}
		mu.Unlock()
		<-released
	}
	e := newEnv(t, setup{as: oauthtest.AuthServerOptions{DCR: true, OnRegister: hold}})
	errs := make(chan error, 2)
	for _, o := range []oauth.Owner{alice, eve} {
		go func() {
			_, err := e.c.Begin(t.Context(), o, e.srv, "")
			errs <- err
		}()
	}
	select {
	case <-bothIn:
	case <-time.After(5 * time.Second):
		mu.Lock()
		n := arrived
		mu.Unlock()
		close(released)
		<-errs
		<-errs
		t.Fatalf("%d registration requests reached the server while the first was held, want 2 (one per tenant)", n)
	}
	close(released)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	a, _ := e.store.GetClient(t.Context(), alice.Tenant, e.as.URL)
	b, _ := e.store.GetClient(t.Context(), eve.Tenant, e.as.URL)
	if a.ID == "" || b.ID == "" || a.ID == b.ID {
		t.Fatalf("clients %q and %q: want one of each tenant's own", a.ID, b.ID)
	}
}

// The flow as the agent API keeps it: Start, the URL with a state of the
// caller's, the pending flow encoded (to be sealed), decoded and finished.
func TestStartFinish(t *testing.T) {
	t.Parallel()
	start := func(t *testing.T, e *env) (oauth.Pending, oauth.Callback) {
		t.Helper()
		p, err := e.c.Start(t.Context(), alice, e.srv)
		if err != nil {
			t.Fatal(err)
		}
		b, err := oauth.EncodePending(p)
		if err != nil {
			t.Fatal(err)
		}
		if p, err = oauth.DecodePending(b); err != nil {
			t.Fatal(err)
		}
		authURL := e.c.AuthCodeURL(p, "caller-state")
		u, _ := url.Parse(authURL)
		if u.Query().Get("state") != "caller-state" || u.Query().Get("code_challenge_method") != "S256" || u.Query().Get("resource") != e.srv.URL {
			t.Fatalf("authorization URL %s", authURL)
		}
		cb, _ := browse(t, authURL)
		if cb.State != "caller-state" {
			t.Fatalf("callback state %q", cb.State)
		}
		return p, cb
	}
	t.Run("connects", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, setup{as: spec})
		p, cb := start(t, e)
		if err := e.c.Finish(t.Context(), p, cb, e.srv); err != nil {
			t.Fatal(err)
		}
		if err := e.call(t, alice); err != nil {
			t.Fatal(err)
		}
	})
	for name, spoil := range map[string]func(*env, *oauth.Pending, *oauth.Callback, *oauth.Server){
		"expired":        func(e *env, _ *oauth.Pending, _ *oauth.Callback, _ *oauth.Server) { e.clk.Advance(11 * time.Minute) },
		"another server": func(_ *env, _ *oauth.Pending, _ *oauth.Callback, s *oauth.Server) { s.Name = "other" },
		"another issuer pinned": func(_ *env, _ *oauth.Pending, _ *oauth.Callback, s *oauth.Server) {
			s.Profile.Issuer = "https://as.example"
		},
		"issuer mixed up": func(_ *env, _ *oauth.Pending, cb *oauth.Callback, _ *oauth.Server) {
			cb.Iss = "https://attacker.example"
		},
		"another verifier": func(_ *env, p *oauth.Pending, _ *oauth.Callback, _ *oauth.Server) {
			p.Verifier = oauth.NewSecret(strings.Repeat("v", 43))
		},
		"denied": func(_ *env, _ *oauth.Pending, cb *oauth.Callback, _ *oauth.Server) {
			cb.Code, cb.Error = "", "access_denied"
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, setup{as: spec})
			p, cb := start(t, e)
			srv := e.srv
			spoil(e, &p, &cb, &srv)
			if err := e.c.Finish(t.Context(), p, cb, srv); err == nil {
				t.Fatal("Finish connected")
			}
			if _, err := e.c.Token(t.Context(), alice, e.srv); !errors.Is(err, oauth.ErrNotConnected) {
				t.Fatalf("Token: %v, want ErrNotConnected", err)
			}
		})
	}
	t.Run("owner and scope", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, setup{as: spec})
		if _, err := e.c.Start(t.Context(), oauth.Owner{Tenant: "acme"}, e.srv); !errors.Is(err, oauth.ErrNoUser) {
			t.Errorf("a user server with no user: %v", err)
		}
		tenantSrv := e.srv
		tenantSrv.Scope = oauth.ScopeTenant
		if _, err := e.c.Start(t.Context(), alice, tenantSrv); err == nil {
			t.Error("a tenant server's connection started for a user")
		}
	})
	t.Run("not a flow", func(t *testing.T) {
		t.Parallel()
		for _, b := range []string{"", "{}", `{"v":2,"server":"x","verifier":"y"}`, "not json"} {
			if _, err := oauth.DecodePending([]byte(b)); err == nil {
				t.Errorf("DecodePending(%q) succeeded", b)
			}
		}
	})
}

type vaultRig struct {
	blobs *countingBlobs
	vault credentials.Vault
	store *oauth.VaultStore
}

// countingBlobs records every sealed blob written, to look for plaintext.
type countingBlobs struct {
	credentials.BlobStore
	mu      sync.Mutex
	written [][]byte
}

func (c *countingBlobs) Put(ctx context.Context, user, slot string, b credentials.Blob, ifVersion int64) (credentials.Meta, error) {
	c.mu.Lock()
	c.written = append(c.written, bytes.Clone(b.Data))
	c.mu.Unlock()
	return c.BlobStore.Put(ctx, user, slot, b, ifVersion)
}

func newVaultRig(t *testing.T) *vaultRig {
	t.Helper()
	ring, err := credentials.NewKeyRing("k1", map[string][]byte{"k1": bytes.Repeat([]byte{7}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	r := &vaultRig{blobs: &countingBlobs{BlobStore: memory.New()}}
	r.vault = credentials.NewVault(r.blobs, credentials.NewSealer(ring))
	r.store = oauth.NewVaultStore(r.vault)
	return r
}

func TestVaultStore(t *testing.T) {
	t.Parallel()
	t.Run("connect, call, refresh once, reconnect", func(t *testing.T) {
		t.Parallel()
		v := newVaultRig(t)
		e := newEnv(t, setup{as: spec, tokens: v.store, clients: v.store})
		e.connect(t, alice)
		if err := e.call(t, alice); err != nil {
			t.Fatal(err)
		}
		if err := e.call(t, bob); !errors.Is(err, oauth.ErrNotConnected) {
			t.Fatalf("bob: %v, want ErrNotConnected", err)
		}
		// In the vault: an OAuth2 credential at the server's slot, for
		// alice of acme.
		ctx := tenancy.With(t.Context(), mustTenant(t, "acme"))
		cred, meta, err := v.vault.Get(ctx, "alice", "crm")
		if err != nil {
			t.Fatal(err)
		}
		o, ok := cred.(credentials.OAuth2)
		if !ok || o.Client == nil || o.Client.TokenURL != e.as.URL+"/token" || o.Client.Resource != e.srv.URL || meta.Kind != credentials.KindOAuth2 {
			t.Fatalf("stored %T %+v", cred, meta)
		}
		access := o.AccessToken.Reveal()

		e.clk.Advance(2 * time.Hour)
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				if _, err := e.c.Token(t.Context(), alice, e.srv); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		if _, n, _ := e.as.Counts(); n != 1 {
			t.Errorf("refreshes = %d, want 1", n)
		}
		// A second replica on the same vault sees the refreshed token.
		c2, err := oauth.New(oauth.Config{PublicURL: e.public.URL, Tokens: v.store, Clients: v.store, Clock: e.clk})
		if err != nil {
			t.Fatal(err)
		}
		s1, _ := e.c.Token(t.Context(), alice, e.srv)
		s2, err := c2.Token(t.Context(), alice, e.srv)
		if err != nil || s1.Reveal() != s2.Reveal() || s1.Reveal() == access {
			t.Fatalf("replicas: %v; same %v; refreshed %v", err, s1.Reveal() == s2.Reveal(), s1.Reveal() != access)
		}

		e.as.RevokeRefresh()
		e.clk.Advance(2 * time.Hour)
		if _, err := e.c.Token(t.Context(), alice, e.srv); !errors.Is(err, oauth.ErrReconnectNeeded) {
			t.Fatalf("Token after invalid_grant: %v, want ErrReconnectNeeded", err)
		}
		if _, meta, _ := v.vault.Get(ctx, "alice", "crm"); !meta.NeedsReconnect {
			t.Error("the vault's credential is not marked needs_reconnect")
		}
		e.connect(t, alice)
		if _, meta, _ := v.vault.Get(ctx, "alice", "crm"); meta.NeedsReconnect {
			t.Error("reconnecting left the mark")
		}
		if err := e.call(t, alice); err != nil {
			t.Fatal(err)
		}
		if err := e.c.Disconnect(t.Context(), alice, e.srv); err != nil {
			t.Fatal(err)
		}
		if err := e.call(t, alice); !errors.Is(err, oauth.ErrNotConnected) {
			t.Fatalf("after Disconnect: %v", err)
		}
	})
	t.Run("registered client kept per tenant", func(t *testing.T) {
		t.Parallel()
		v := newVaultRig(t)
		e := newEnv(t, setup{as: oauthtest.AuthServerOptions{DCR: true}, tokens: v.store, clients: v.store})
		e.connect(t, alice)
		e.connect(t, bob)
		// A new replica reads the client from the vault, not registering again.
		c2, err := oauth.New(oauth.Config{PublicURL: e.public.URL, Tokens: v.store, Clients: v.store, States: e.store, Clock: e.clk})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c2.Begin(t.Context(), alice, e.srv, ""); err != nil {
			t.Fatal(err)
		}
		if reg, _, _ := e.as.Counts(); reg != 1 {
			t.Fatalf("registrations = %d, want 1 for the tenant", reg)
		}
		e.connect(t, eve)
		if reg, _, _ := e.as.Counts(); reg != 2 {
			t.Fatalf("registrations = %d after another tenant, want 2", reg)
		}
		a, err1 := v.store.GetClient(t.Context(), "acme", e.as.URL)
		b, err2 := v.store.GetClient(t.Context(), "evil", e.as.URL)
		if err1 != nil || err2 != nil || a.ID == b.ID || a.Secret.Reveal() == b.Secret.Reveal() {
			t.Fatalf("clients: %v %v; acme %q evil %q", err1, err2, a.ID, b.ID)
		}
		// A reserved slot: no toolset can name it.
		metas, _ := v.vault.List(tenancy.With(t.Context(), mustTenant(t, "acme")), "")
		if len(metas) != 1 || !strings.HasPrefix(metas[0].Slot, oauth.ClientSlotPrefix) || metas[0].Kind != credentials.KindOAuth2Client {
			t.Fatalf("acme's tenant credentials: %+v", metas)
		}
	})
	t.Run("the key's tenant, never the context's", func(t *testing.T) {
		t.Parallel()
		v := newVaultRig(t)
		e := newEnv(t, setup{as: spec, tokens: v.store, clients: v.store})
		e.connect(t, alice)
		ctxAcme := tenancy.With(t.Context(), mustTenant(t, "acme"))
		if _, err := v.store.Get(ctxAcme, oauth.Key{Owner: oauth.Owner{Tenant: "evil", User: "alice"}, Server: "crm"}); !errors.Is(err, oauth.ErrNotFound) {
			t.Fatalf("evil's key in acme's context: %v, want ErrNotFound", err)
		}
		ctxEvil := tenancy.With(t.Context(), mustTenant(t, "evil"))
		if tok, err := v.store.Get(ctxEvil, oauth.Key{Owner: alice, Server: "crm"}); err != nil || tok.Access.IsZero() {
			t.Fatalf("acme's key in evil's context: %v", err)
		}
	})
	t.Run("swap is compare-and-swap", func(t *testing.T) {
		t.Parallel()
		v := newVaultRig(t)
		k := oauth.Key{Owner: alice, Server: "crm"}
		tok := oauth.Token{Access: oauth.NewSecret("a1"), TokenURL: "https://as.example/token", ClientID: "c"}
		if err := v.store.Put(t.Context(), k, tok); err != nil {
			t.Fatal(err)
		}
		cur, err := v.store.Get(t.Context(), k)
		if err != nil {
			t.Fatal(err)
		}
		next := cur
		next.Access = oauth.NewSecret("a2")
		if err := v.store.Swap(t.Context(), k, cur, next); err != nil {
			t.Fatal(err)
		}
		if err := v.store.Swap(t.Context(), k, cur, next); !errors.Is(err, oauth.ErrConflict) {
			t.Fatalf("stale swap: %v, want ErrConflict", err)
		}
		cur, _ = v.store.Get(t.Context(), k)
		mark := cur
		mark.ReconnectNeeded = true
		if err := v.store.Swap(t.Context(), k, cur, mark); err != nil {
			t.Fatal(err)
		}
		if got, _ := v.store.Get(t.Context(), k); !got.ReconnectNeeded || got.Access.Reveal() != "a2" {
			t.Fatalf("after the mark: %+v", got)
		}
		if err := v.store.Swap(t.Context(), oauth.Key{Owner: bob, Server: "crm"}, cur, next); !errors.Is(err, oauth.ErrConflict) {
			t.Fatalf("swap of nothing: %v", err)
		}
		if err := v.store.Delete(t.Context(), k); err != nil {
			t.Fatal(err)
		}
		if err := v.store.Delete(t.Context(), k); err != nil {
			t.Fatalf("second delete: %v", err)
		}
		// Another kind of credential at the slot is no connection.
		ctx := tenancy.With(t.Context(), mustTenant(t, "acme"))
		if _, err := v.vault.Put(ctx, "alice", "crm", credentials.APIKey{Key: credentials.NewSecret("k")}, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := v.store.Get(t.Context(), k); !errors.Is(err, oauth.ErrNotFound) {
			t.Fatalf("an API key read as a connection: %v", err)
		}
		if err := v.store.Put(t.Context(), k, tok); err != nil {
			t.Fatalf("connecting over an API key: %v", err)
		}
	})
	t.Run("sealed at rest", func(t *testing.T) {
		t.Parallel()
		v := newVaultRig(t)
		e := newEnv(t, setup{as: oauthtest.AuthServerOptions{DCR: true, Scopes: []string{"offline_access"}, TTL: time.Hour}, tokens: v.store, clients: v.store})
		e.connect(t, alice)
		tok, err := v.store.Get(t.Context(), oauth.Key{Owner: alice, Server: "crm"})
		if err != nil {
			t.Fatal(err)
		}
		cl, _ := v.store.GetClient(t.Context(), "acme", e.as.URL)
		secrets := []string{tok.Access.Reveal(), tok.Refresh.Reveal(), tok.ClientSecret.Reveal(), cl.Secret.Reveal()}
		v.blobs.mu.Lock()
		defer v.blobs.mu.Unlock()
		for _, b := range v.blobs.written {
			for _, s := range secrets {
				if s == "" {
					t.Fatal("an empty secret to look for")
				}
				if bytes.Contains(b, []byte(s)) {
					t.Fatal("a token or client secret is in a blob in the clear")
				}
			}
		}
	})
}

func mustTenant(t *testing.T, id string) tenancy.Tenant {
	t.Helper()
	tn, err := tenancy.NewTenant(id)
	if err != nil {
		t.Fatal(err)
	}
	return tn
}

// A connection is used only for the server it was issued for: its URL (or
// the origin, which protected resource metadata at the root names), and
// its pinned issuer. A toolset removed and added again under the same name
// at another URL or authorization server is not connected.
func TestIssuedFor(t *testing.T) {
	t.Parallel()
	store := &oauth.MemoryStore{}
	c, err := oauth.New(oauth.Config{PublicURL: "https://agentkit.example", Tokens: store, Clients: store})
	if err != nil {
		t.Fatal(err)
	}
	k := oauth.Key{Owner: alice, Server: "crm"}
	for _, tc := range []struct {
		name             string
		resource, issuer string
		srv              oauth.Server
		ok               bool
	}{
		{"its URL", "https://crm.example/mcp", "https://as.example", oauth.Server{URL: "https://crm.example/mcp"}, true},
		{"its origin", "https://crm.example", "https://as.example", oauth.Server{URL: "https://crm.example/mcp"}, true},
		{"its origin with a slash", "https://crm.example/", "", oauth.Server{URL: "https://crm.example/mcp"}, true},
		{"no resource kept", "", "", oauth.Server{URL: "https://crm.example/mcp"}, true},
		{"its pinned issuer", "https://crm.example/mcp", "https://as.example", oauth.Server{URL: "https://crm.example/mcp", Profile: oauth.Profile{Issuer: "https://as.example/"}}, true},
		{"another path", "https://crm.example/mcp", "", oauth.Server{URL: "https://crm.example/other"}, false},
		{"another host", "https://crm.example/mcp", "", oauth.Server{URL: "https://evil.example/mcp"}, false},
		{"another scheme", "https://crm.example", "", oauth.Server{URL: "http://crm.example/mcp"}, false},
		{"another issuer", "https://crm.example/mcp", "https://as.example", oauth.Server{URL: "https://crm.example/mcp", Profile: oauth.Profile{Issuer: "https://evil-as.example"}}, false},
	} {
		tc.srv.Name = "crm"
		if err := store.Put(t.Context(), k, oauth.Token{Access: oauth.NewSecret("at"), Resource: tc.resource, Issuer: tc.issuer}); err != nil {
			t.Fatal(err)
		}
		err := c.Connected(t.Context(), alice, tc.srv)
		_, terr := c.Token(t.Context(), alice, tc.srv)
		if tc.ok && (err != nil || terr != nil) {
			t.Errorf("%s: %v, %v; want connected", tc.name, err, terr)
		}
		if !tc.ok && (!errors.Is(err, oauth.ErrNotConnected) || !errors.Is(terr, oauth.ErrNotConnected)) {
			t.Errorf("%s: %v, %v; want ErrNotConnected", tc.name, err, terr)
		}
	}
}
