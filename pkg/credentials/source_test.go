package credentials_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/credentials/credentialstest"
	"github.com/bsamanvith/agentkit/pkg/credentials/memory"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// canary is in every secret these tests store or a token server issues; no
// log line or error may hold it.
const canary = "src-canary-5e1d"

// srcRig is a vault over a memory store and the sources over it, with a
// debug log that the rig checks for the canary when the test ends.
type srcRig struct {
	t       *testing.T
	store   *memory.Store
	ring    *credentials.KeyRing
	vault   credentials.Vault
	sources *credentials.Sources
	logs    *syncBuffer
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func newSrcRig(t *testing.T, opts ...credentials.SourcesOption) *srcRig {
	t.Helper()
	k := make([]byte, credentials.KeySize)
	_, _ = rand.Read(k)
	ring, err := credentials.NewKeyRing("k1", map[string][]byte{"k1": k})
	if err != nil {
		t.Fatal(err)
	}
	r := &srcRig{t: t, store: memory.New(), ring: ring, logs: &syncBuffer{}}
	r.vault = credentials.NewVault(r.store, credentials.NewSealer(ring))
	r.sources = r.replica(opts...)
	t.Cleanup(func() {
		if strings.Contains(r.logs.String(), canary) {
			t.Errorf("a secret reached the log:\n%s", r.logs.String())
		}
	})
	return r
}

// replica is another process's sources over the same store: its own vault
// and its own refreshes in flight.
func (r *srcRig) replica(opts ...credentials.SourcesOption) *credentials.Sources {
	r.t.Helper()
	v := credentials.NewVault(r.store, credentials.NewSealer(r.ring))
	log := slog.New(slog.NewJSONHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s, err := credentials.NewSources(v, append([]credentials.SourcesOption{credentials.WithLogger(log)}, opts...)...)
	if err != nil {
		r.t.Fatal(err)
	}
	return s
}

func (r *srcRig) source(s *credentials.Sources, slot string, scope credentials.Scope) credentials.Source {
	r.t.Helper()
	src, err := s.Source(slot+"-tools", slot, scope)
	if err != nil {
		r.t.Fatal(err)
	}
	return src
}

func (r *srcRig) put(ctx context.Context, user, slot string, c credentials.Credential) credentials.Meta {
	r.t.Helper()
	m, err := r.vault.Put(ctx, user, slot, c, 0)
	if err != nil {
		r.t.Fatal(err)
	}
	return m
}

func (r *srcRig) stored(ctx context.Context, user, slot string) (credentials.OAuth2, credentials.Meta) {
	r.t.Helper()
	c, m, err := r.vault.Get(ctx, user, slot)
	if err != nil {
		r.t.Fatal(err)
	}
	o, _ := c.(credentials.OAuth2)
	return o, m
}

func scopeCtx(t *testing.T, tenant, user string) context.Context {
	t.Helper()
	tn, err := tenancy.NewTenant(tenant)
	if err != nil {
		t.Fatal(err)
	}
	ctx := tenancy.With(t.Context(), tn)
	if user != "" {
		ctx = tenancy.WithUser(ctx, user)
	}
	return ctx
}

func secret(s string) credentials.Secret { return credentials.NewSecret(s + "-" + canary) }

// expiring is an OAuth2 token inside the refresh margin.
func expiring(rt string) credentials.OAuth2 {
	return credentials.OAuth2{
		AccessToken: secret("old-access"), RefreshToken: credentials.NewSecret(rt),
		Expiry: time.Now().Add(10 * time.Second), TokenType: "Bearer",
	}
}

// echoAuth is an upstream that answers with the Authorization (or
// X-Api-Key) header it got.
func echoAuth(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		if k := r.Header.Get("X-Api-Key"); k != "" {
			got = "x-api-key " + k
		}
		_, _ = io.WriteString(w, got)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func call(t *testing.T, c *http.Client, u string) (string, error) {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, u, nil)
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), nil
}

func notConnected(t *testing.T, err error, toolset string) {
	t.Helper()
	var nc *credentials.NotConnectedError
	if !errors.Is(err, credentials.ErrNotConnected) || !errors.As(err, &nc) || nc.Toolset != toolset {
		t.Fatalf("err = %v, want the NotConnectedError of %s", err, toolset)
	}
	if !strings.HasSuffix(err.Error(), toolset+" is not connected for this account") {
		t.Errorf("message %q", err.Error())
	}
}

// The tenant's credential for a tenant-scoped slot, the user's for a
// user-scoped one, chosen from the context of each call.
func TestSourceUsesTheCallersCredential(t *testing.T) {
	t.Parallel()
	r := newSrcRig(t)
	up := echoAuth(t)
	a, b := scopeCtx(t, "tenant-a", ""), scopeCtx(t, "tenant-b", "")
	r.put(a, "", "crm", credentials.APIKey{Key: secret("crm-a")})
	r.put(b, "", "crm", credentials.APIKey{Key: secret("crm-b")})
	r.put(a, "u1", "mail", credentials.OAuth2{AccessToken: secret("mail-u1")})
	r.put(a, "u2", "mail", credentials.OAuth2{AccessToken: secret("mail-u2")})
	r.put(a, "u1", "crm", credentials.APIKey{Key: secret("crm-a-u1-unused")})
	crm, mail := r.source(r.sources, "crm", ""), r.source(r.sources, "mail", credentials.ScopeUser)
	for _, tc := range []struct {
		name, tenant, user string
		src                credentials.Source
		want               string // "" for not connected
	}{
		{"tenant A", "tenant-a", "", crm, "Bearer crm-a-" + canary},
		{"tenant B", "tenant-b", "", crm, "Bearer crm-b-" + canary},
		{"tenant scope ignores the user", "tenant-a", "u1", crm, "Bearer crm-a-" + canary},
		{"user U1", "tenant-a", "u1", mail, "Bearer mail-u1-" + canary},
		{"user U2", "tenant-a", "u2", mail, "Bearer mail-u2-" + canary},
		{"U1 in another tenant", "tenant-b", "u1", mail, ""},
		{"a user with none", "tenant-a", "u3", mail, ""},
		{"no user", "tenant-a", "", mail, ""},
		{"a tenant with none", "tenant-c", "", crm, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := scopeCtx(t, tc.tenant, tc.user)
			c, err := tc.src.HTTPClient(ctx, nil)
			if tc.want == "" {
				notConnected(t, err, map[credentials.Source]string{crm: "crm-tools", mail: "mail-tools"}[tc.src])
				if _, err := tc.src.Get(ctx); !errors.Is(err, credentials.ErrNotConnected) {
					t.Errorf("Get: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got, err := call(t, c, up.URL); err != nil || got != tc.want {
				t.Errorf("upstream got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

// No credential, one marked for reconnecting, no tenant: ErrNotConnected,
// naming the toolset and nothing from the vault.
func TestSourceNotConnected(t *testing.T) {
	t.Parallel()
	r := newSrcRig(t)
	a := scopeCtx(t, "tenant-a", "")
	src := r.source(r.sources, "crm", credentials.ScopeTenant)
	notConnected(t, errOf(src.Get(a)), "crm-tools")
	_, err := src.HTTPClient(t.Context(), nil) // no tenant
	notConnected(t, err, "crm-tools")
	r.put(a, "", "other", credentials.APIKey{Key: secret("other")})
	notConnected(t, errOf(src.Get(a)), "crm-tools") // another slot is not this one
	m := r.put(a, "", "crm", credentials.APIKey{Key: secret("crm")})
	if _, err := src.Get(a); err != nil {
		t.Fatal(err)
	}
	if _, err := r.vault.MarkNeedsReconnect(a, "", "crm", m.Version); err != nil {
		t.Fatal(err)
	}
	notConnected(t, errOf(src.Get(a)), "crm-tools")
}

// A connection to an MCP server added at run time (T095), or a client
// registered for one, at a slot of the config's name is that server's: a
// toolset of the config never sends it to its own server.
func TestSourceNeverSendsAnMCPConnection(t *testing.T) {
	t.Parallel()
	r := newSrcRig(t)
	a := scopeCtx(t, "tenant-a", "")
	src := r.source(r.sources, "crm", credentials.ScopeTenant)
	r.put(a, "", "crm", credentials.OAuth2{
		AccessToken: secret("mcp-at"), Expiry: time.Now().Add(time.Hour),
		Client: &credentials.OAuth2Client{ClientID: "dcr", Issuer: "https://as.example", TokenURL: "https://as.example/token", Resource: "https://mcp.example/mcp"},
	})
	notConnected(t, errOf(src.Get(a)), "crm-tools")
	r.put(a, "", "erp", credentials.OAuth2Client{ClientID: "dcr", ClientSecret: secret("dcs"), Issuer: "https://as.example"})
	notConnected(t, errOf(r.source(r.sources, "erp", credentials.ScopeTenant).Get(a)), "erp-tools")
}

func errOf(_ credentials.Credential, err error) error { return err }

// A Source has no way to name a slot: none of its methods takes a string.
func TestSourceCannotNameASlot(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeFor[credentials.Source]()
	allowed := map[reflect.Type]bool{
		reflect.TypeFor[context.Context]():          true,
		reflect.TypeFor[*http.Client]():             true,
		reflect.TypeFor[[]credentials.AuthOption](): true,
	}
	for i := range typ.NumMethod() {
		m := typ.Method(i)
		for j := range m.Type.NumIn() {
			if in := m.Type.In(j); !allowed[in] {
				t.Errorf("Source.%s takes a %v", m.Name, in)
			}
		}
	}
	if _, err := (&credentials.Sources{}).Source("x", "no slot!", ""); !errors.Is(err, credentials.ErrInvalidKey) {
		t.Errorf("an invalid slot: %v", err)
	}
	r := newSrcRig(t)
	if _, err := r.sources.Source("x", "crm", "admin"); !errors.Is(err, credentials.ErrInvalidScope) {
		t.Errorf("an invalid scope: %v", err)
	}
}

// waitN blocks each caller until n have come, or a while has passed (so a
// correct implementation that sends fewer is not stuck, only slower).
type waitN struct {
	mu   sync.Mutex
	n    int
	done chan struct{}
}

func newWaitN(n int) *waitN { return &waitN{n: n, done: make(chan struct{})} }

func (w *waitN) arrive() {
	w.mu.Lock()
	w.n--
	if w.n == 0 {
		close(w.done)
	}
	w.mu.Unlock()
	select {
	case <-w.done:
	case <-time.After(3 * time.Second):
	}
}

// 50 concurrent calls with a token about to expire send one refresh.
func TestOneRefreshForConcurrentCalls(t *testing.T) {
	t.Parallel()
	const callers = 50
	callersIn := newWaitN(callers + 1) // the callers, and the refresh
	ts := credentialstest.NewTokenServer(t, credentialstest.TokenServerOptions{
		Prefix: canary + "-",
		Hook: func(w http.ResponseWriter, r *http.Request, _ url.Values) bool {
			callersIn.arrive() // hold the refresh until every caller is in
			return false
		},
	})
	ts.AddRefreshToken("rt-0-" + canary)
	r := newSrcRig(t, credentials.WithOAuth2App("mail", credentials.OAuth2App{ClientID: ts.ClientID, ClientSecret: credentials.NewSecret(ts.ClientSecret), TokenURL: ts.URL}))
	ctx := scopeCtx(t, "tenant-a", "u1")
	r.put(ctx, "u1", "mail", expiring("rt-0-"+canary))
	src := r.source(r.sources, "mail", credentials.ScopeUser)

	var wg sync.WaitGroup
	got := make([]string, callers)
	for i := range callers {
		wg.Go(func() {
			go callersIn.arrive()
			c, err := src.Get(ctx)
			if err != nil {
				t.Errorf("caller %d: %v", i, err)
				return
			}
			got[i] = c.(credentials.OAuth2).AccessToken.Reveal()
		})
	}
	wg.Wait()
	if n := ts.Requests(); n != 1 {
		t.Errorf("%d refresh requests, want 1", n)
	}
	stored, meta := r.stored(ctx, "u1", "mail")
	for i, g := range got {
		if g != stored.AccessToken.Reveal() {
			t.Errorf("caller %d got %q, stored %q", i, g, stored.AccessToken.Reveal())
		}
	}
	if meta.Version != 2 || !ts.Valid(stored.RefreshToken.Reveal()) || !stored.Expiry.After(time.Now().Add(time.Hour-time.Minute)) {
		t.Errorf("stored %+v, refresh token valid %v", meta, ts.Valid(stored.RefreshToken.Reveal()))
	}
	// Fresh now: no more requests.
	if _, err := src.Get(ctx); err != nil || ts.Requests() != 1 {
		t.Errorf("a call after the refresh: %v, %d requests", err, ts.Requests())
	}
}

// Two replicas refreshing the same credential at once: the loser of the
// compare-and-swap reads the winner's token and uses it, so one chain is
// stored and its refresh token is one the server accepts.
func TestTwoReplicasRefreshing(t *testing.T) {
	t.Parallel()
	both := newWaitN(2)
	ts := credentialstest.NewTokenServer(t, credentialstest.TokenServerOptions{
		Prefix: canary + "-",
		Hook: func(http.ResponseWriter, *http.Request, url.Values) bool {
			both.arrive() // both replicas have read the old token
			return false
		},
	})
	ts.AddRefreshToken("rt-0-" + canary)
	app := credentials.WithOAuth2App("crm", credentials.OAuth2App{ClientID: ts.ClientID, ClientSecret: credentials.NewSecret(ts.ClientSecret), TokenURL: ts.URL})
	r := newSrcRig(t, app)
	ctx := scopeCtx(t, "tenant-a", "")
	r.put(ctx, "", "crm", expiring("rt-0-"+canary))
	srcs := []credentials.Source{r.source(r.sources, "crm", ""), r.source(r.replica(app), "crm", "")}
	got := make([]string, 2)
	var wg sync.WaitGroup
	for i, src := range srcs {
		wg.Go(func() {
			c, err := src.Get(ctx)
			if err != nil {
				t.Errorf("replica %d: %v", i, err)
				return
			}
			got[i] = c.(credentials.OAuth2).AccessToken.Reveal()
		})
	}
	wg.Wait()
	stored, meta := r.stored(ctx, "", "crm")
	if got[0] != stored.AccessToken.Reveal() || got[1] != stored.AccessToken.Reveal() {
		t.Errorf("replicas use %q and %q, stored %q", got[0], got[1], stored.AccessToken.Reveal())
	}
	if meta.Version != 2 || meta.NeedsReconnect || !ts.Valid(stored.RefreshToken.Reveal()) || stored.RefreshToken.Reveal() == "rt-0-"+canary {
		t.Errorf("stored %+v with refresh token valid: %v", meta, ts.Valid(stored.RefreshToken.Reveal()))
	}
	if n := ts.Grants("refresh_token"); n != 2 {
		t.Errorf("%d refreshes, want one per replica", n)
	}
}

// With single-use refresh tokens, the replica whose refresh is refused
// marks the credential, but the one whose refresh succeeded stores its
// token over the mark: the chain is not lost.
func TestReplicaRefusedRefreshDoesNotLoseTheChain(t *testing.T) {
	t.Parallel()
	release, held := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	first := true
	ts := credentialstest.NewTokenServer(t, credentialstest.TokenServerOptions{Prefix: canary + "-", RevokeUsed: true})
	ts.SetOptions(credentialstest.TokenServerOptions{
		Prefix: canary + "-", RevokeUsed: true,
		Hook: func(w http.ResponseWriter, _ *http.Request, _ url.Values) bool {
			mu.Lock()
			isFirst := first
			first = false
			mu.Unlock()
			if isFirst {
				close(held)
				<-release // answered (with new tokens) once the other has marked
				return false
			}
			credentialstest.TokenError(w, http.StatusBadRequest, "invalid_grant", "used")
			return true
		},
	})
	ts.AddRefreshToken("rt-0-" + canary)
	app := credentials.WithOAuth2App("crm", credentials.OAuth2App{ClientID: ts.ClientID, ClientSecret: credentials.NewSecret(ts.ClientSecret), TokenURL: ts.URL})
	r := newSrcRig(t, app)
	ctx := scopeCtx(t, "tenant-a", "")
	r.put(ctx, "", "crm", expiring("rt-0-"+canary))
	winner := make(chan error, 1)
	go func() {
		_, err := r.source(r.sources, "crm", "").Get(ctx)
		winner <- err
	}()
	// Wait for the first refresh to be held, then let the other replica's
	// be refused, and its mark land.
	<-held
	_, err := r.source(r.replica(app), "crm", "").Get(ctx)
	notConnected(t, err, "crm-tools")
	if _, m := r.stored(ctx, "", "crm"); !m.NeedsReconnect {
		t.Fatalf("the refused replica did not mark: %+v", m)
	}
	close(release)
	if err := <-winner; err != nil {
		t.Fatalf("the replica whose refresh succeeded: %v", err)
	}
	stored, meta := r.stored(ctx, "", "crm")
	if meta.NeedsReconnect || !ts.Valid(stored.RefreshToken.Reveal()) {
		t.Errorf("stored %+v, refresh token valid %v: the chain was lost", meta, ts.Valid(stored.RefreshToken.Reveal()))
	}
}

// A write that loses to another writer during the refresh: the stored
// token is used when fresh, and the refresh is tried once more when not.
func TestRefreshConflict(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		theirs       func(rt string) credentials.OAuth2
		wantRequests int
	}{
		{"theirs is fresh", func(rt string) credentials.OAuth2 {
			return credentials.OAuth2{AccessToken: secret("theirs"), RefreshToken: credentials.NewSecret(rt), Expiry: time.Now().Add(time.Hour)}
		}, 1},
		{"theirs is stale", expiring, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var r *srcRig
			var ctx context.Context
			var once sync.Once
			ts := credentialstest.NewTokenServer(t, credentialstest.TokenServerOptions{Prefix: canary + "-"})
			ts.AddRefreshToken("rt-0-" + canary)
			ts.AddRefreshToken("rt-theirs-" + canary)
			ts.SetOptions(credentialstest.TokenServerOptions{
				Prefix: canary + "-",
				Hook: func(http.ResponseWriter, *http.Request, url.Values) bool {
					once.Do(func() { // another writer gets in first
						_, m := r.stored(ctx, "", "crm")
						if _, err := r.vault.Put(ctx, "", "crm", tc.theirs("rt-theirs-"+canary), m.Version); err != nil {
							t.Error(err)
						}
					})
					return false
				},
			})
			r = newSrcRig(t, credentials.WithOAuth2App("crm", credentials.OAuth2App{ClientID: ts.ClientID, ClientSecret: credentials.NewSecret(ts.ClientSecret), TokenURL: ts.URL}))
			ctx = scopeCtx(t, "tenant-a", "")
			r.put(ctx, "", "crm", expiring("rt-0-"+canary))
			c, err := r.source(r.sources, "crm", "").Get(ctx)
			if err != nil {
				t.Fatal(err)
			}
			stored, _ := r.stored(ctx, "", "crm")
			if got := c.(credentials.OAuth2).AccessToken.Reveal(); got != stored.AccessToken.Reveal() || ts.Requests() != tc.wantRequests {
				t.Errorf("got %q, stored %q, %d requests; want the stored one after %d", got, stored.AccessToken.Reveal(), ts.Requests(), tc.wantRequests)
			}
			if !ts.Valid(stored.RefreshToken.Reveal()) {
				t.Error("the stored refresh token is not valid")
			}
		})
	}
}

// invalid_grant: not connected, marked, and no second request, then or later.
func TestInvalidGrant(t *testing.T) {
	t.Parallel()
	ts := credentialstest.NewTokenServer(t, credentialstest.TokenServerOptions{}) // knows no refresh token
	r := newSrcRig(t, credentials.WithOAuth2App("mail", credentials.OAuth2App{ClientID: ts.ClientID, ClientSecret: credentials.NewSecret(ts.ClientSecret), TokenURL: ts.URL, AuthStyle: credentials.AuthInParams}))
	ctx := scopeCtx(t, "tenant-a", "u1")
	r.put(ctx, "u1", "mail", expiring("rt-revoked-"+canary))
	src := r.source(r.sources, "mail", credentials.ScopeUser)
	notConnected(t, errOf(src.Get(ctx)), "mail-tools")
	if _, m := r.stored(ctx, "u1", "mail"); !m.NeedsReconnect {
		t.Errorf("meta %+v, want NeedsReconnect", m)
	}
	_, err := src.HTTPClient(ctx, nil)
	notConnected(t, err, "mail-tools")
	if n := ts.Requests(); n != 1 {
		t.Errorf("%d token requests, want 1", n)
	}
	// Connecting again (a write) clears it.
	ts.AddRefreshToken("rt-new-" + canary)
	m, _ := r.vault.List(ctx, "u1")
	if _, err := r.vault.Put(ctx, "u1", "mail", expiring("rt-new-"+canary), m[0].Version); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Get(ctx); err != nil {
		t.Errorf("after reconnecting: %v", err)
	}
}

// A refresh that fails another way: a short reason, never the endpoint's
// body, and the current token while it lasts.
func TestRefreshFailure(t *testing.T) {
	t.Parallel()
	ts := credentialstest.NewTokenServer(t, credentialstest.TokenServerOptions{
		Hook: func(w http.ResponseWriter, r *http.Request, form url.Values) bool {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, "overloaded; you sent "+form.Encode()+" "+r.Header.Get("Authorization"))
			return true
		},
	})
	r := newSrcRig(t, credentials.WithOAuth2App("crm", credentials.OAuth2App{ClientID: ts.ClientID, ClientSecret: secret("client"), TokenURL: ts.URL}))
	ctx := scopeCtx(t, "tenant-a", "")
	m := r.put(ctx, "", "crm", expiring("rt-"+canary))
	src := r.source(r.sources, "crm", "")
	c, err := src.Get(ctx)
	if err != nil || c.(credentials.OAuth2).AccessToken.Reveal() != "old-access-"+canary {
		t.Fatalf("within its expiry: %v, %v; want the current token", c, err)
	}
	expired := expiring("rt-" + canary)
	expired.Expiry = time.Now().Add(-time.Second)
	if _, err := r.vault.Put(ctx, "", "crm", expired, m.Version); err != nil {
		t.Fatal(err)
	}
	_, err = src.Get(ctx)
	var se *credentials.SourceError
	if !errors.As(err, &se) || errors.Is(err, credentials.ErrNotConnected) || !strings.Contains(err.Error(), "503") {
		t.Fatalf("expired: %v, want a SourceError with the status", err)
	}
	if strings.Contains(err.Error(), canary) || strings.Contains(err.Error(), "overloaded") {
		t.Errorf("the error carries the endpoint's body: %q", err.Error())
	}
	if _, m := r.stored(ctx, "", "crm"); m.NeedsReconnect {
		t.Error("a 503 marked the credential for reconnecting")
	}
}

// A client credentials grant: one token per tenant and slot, cached until
// it expires or the credential changes.
func TestClientCredentials(t *testing.T) {
	t.Parallel()
	ts := credentialstest.NewTokenServer(t, credentialstest.TokenServerOptions{Prefix: canary + "-"})
	ts.AddClient("client-b", "secret-b-"+canary)
	r := newSrcRig(t)
	up := echoAuth(t)
	a, b := scopeCtx(t, "tenant-a", "u1"), scopeCtx(t, "tenant-b", "")
	m := r.put(a, "", "api", credentials.ClientCredentials{ClientID: ts.ClientID, ClientSecret: credentials.NewSecret(ts.ClientSecret), TokenURL: ts.URL, Scopes: []string{"read"}})
	r.put(b, "", "api", credentials.ClientCredentials{ClientID: "client-b", ClientSecret: secret("secret-b"), TokenURL: ts.URL})
	src := r.source(r.sources, "api", "")
	var seen []string
	for _, ctx := range []context.Context{a, a, b, b} {
		c, err := src.HTTPClient(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		got, err := call(t, c, up.URL)
		if err != nil || !strings.HasPrefix(got, "Bearer "+canary+"-at-") {
			t.Fatalf("upstream got %q, %v", got, err)
		}
		seen = append(seen, got)
	}
	if seen[0] != seen[1] || seen[2] != seen[3] || seen[0] == seen[2] || ts.Requests() != 2 {
		t.Errorf("tokens %q after %d requests; want one per tenant, cached", seen, ts.Requests())
	}
	// A new version of A's credential fetches again.
	if _, err := r.vault.Put(a, "", "api", credentials.ClientCredentials{ClientID: ts.ClientID, ClientSecret: credentials.NewSecret(ts.ClientSecret), TokenURL: ts.URL}, m.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Get(a); err != nil || ts.Requests() != 3 {
		t.Errorf("after a change: %v, %d requests", err, ts.Requests())
	}
	// A wrong secret: a short reason.
	r.put(a, "", "bad", credentials.ClientCredentials{ClientID: ts.ClientID, ClientSecret: secret("wrong"), TokenURL: ts.URL})
	_, err := r.source(r.sources, "bad", "").Get(a)
	if se := (*credentials.SourceError)(nil); !errors.As(err, &se) || !strings.Contains(err.Error(), "401 invalid_client") || strings.Contains(err.Error(), canary) {
		t.Errorf("wrong secret: %v", err)
	}
}

// roundTripper is an http.RoundTripper from a func.
type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A token URL read from the vault (a stored client credentials') is called
// with WithStoredTokenClient's client, the guarded one, while the token
// URLs of config (an OAuth2 app's refresh) keep WithTokenClient's (T045).
func TestStoredTokenClient(t *testing.T) {
	t.Parallel()
	ts := credentialstest.NewTokenServer(t, credentialstest.TokenServerOptions{Prefix: canary + "-"})
	var mu sync.Mutex
	via := map[string]int{}
	counting := func(name string) *http.Client {
		return &http.Client{Transport: roundTripper(func(req *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(req.Body)
			req.Body = io.NopCloser(bytes.NewReader(body))
			form, _ := url.ParseQuery(string(body))
			mu.Lock()
			via[name+" "+form.Get("grant_type")]++
			mu.Unlock()
			return http.DefaultTransport.RoundTrip(req)
		})}
	}
	r := newSrcRig(t, credentials.WithTokenClient(counting("config")), credentials.WithStoredTokenClient(counting("stored")),
		credentials.WithOAuth2App("mail", credentials.OAuth2App{ClientID: ts.ClientID, ClientSecret: credentials.NewSecret(ts.ClientSecret), TokenURL: ts.URL}))
	a := scopeCtx(t, "tenant-a", "")
	r.put(a, "", "api", credentials.ClientCredentials{ClientID: ts.ClientID, ClientSecret: credentials.NewSecret(ts.ClientSecret), TokenURL: ts.URL})
	ts.AddRefreshToken("rt-" + canary)
	r.put(a, "", "mail", credentials.OAuth2{RefreshToken: secret("rt")})
	for _, slot := range []string{"api", "mail"} {
		if _, err := r.source(r.sources, slot, "").Get(a); err != nil {
			t.Fatalf("%s: %v", slot, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if via["stored client_credentials"] != 1 || via["config refresh_token"] != 1 || len(via) != 2 {
		t.Errorf("token requests by client: %v", via)
	}
}

// How each kind is sent, the caller's request left as it was, and no
// redirect to another host.
func TestHTTPClientSendsTheCredential(t *testing.T) {
	t.Parallel()
	r := newSrcRig(t)
	up := echoAuth(t)
	ctx := scopeCtx(t, "tenant-a", "")
	r.put(ctx, "", "key", credentials.APIKey{Key: secret("k")})
	r.put(ctx, "", "basic", credentials.Basic{User: "ann", Password: secret("pw")})
	r.put(ctx, "", "oauth", credentials.OAuth2{AccessToken: secret("at"), TokenType: "bearer"})
	for _, tc := range []struct {
		slot string
		opts []credentials.AuthOption
		want string
	}{
		{"key", nil, "Bearer k-" + canary},
		{"key", []credentials.AuthOption{credentials.APIKeyHeader("x-api-key")}, "x-api-key k-" + canary},
		{"basic", nil, "Basic "},
		{"oauth", nil, "Bearer at-" + canary},
	} {
		c, err := r.source(r.sources, tc.slot, "").HTTPClient(ctx, &http.Client{Timeout: time.Minute}, tc.opts...)
		if err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, up.URL, nil)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.HasPrefix(string(b), tc.want) || len(req.Header) != 0 || c.Timeout != time.Minute {
			t.Errorf("%s %v: upstream got %q (want %q); caller's header %v", tc.slot, tc.opts, b, tc.want, req.Header)
		}
	}

	var leaked bool
	other := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("Authorization") != ""
	}))
	defer other.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/same" {
			http.Redirect(w, r, "/echo", http.StatusFound)
			return
		}
		if r.URL.Path == "/echo" {
			_, _ = io.WriteString(w, r.Header.Get("Authorization"))
			return
		}
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer redirect.Close()
	c, err := r.source(r.sources, "key", "").HTTPClient(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := call(t, c, redirect.URL+"/same"); err != nil || got != "Bearer k-"+canary {
		t.Errorf("same-host redirect: %q, %v", got, err)
	}
	if _, err := call(t, c, redirect.URL+"/away"); err == nil || leaked {
		t.Errorf("redirect to another host: %v, credential sent there: %v", err, leaked)
	}
}

// A redirect from https to http on the same host is refused: the transport
// would send the credential in the clear.
func TestHTTPClientRefusesADowngradeRedirect(t *testing.T) {
	t.Parallel()
	r := newSrcRig(t)
	ctx := scopeCtx(t, "tenant-a", "")
	r.put(ctx, "", "key", credentials.APIKey{Key: secret("k")})
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Same host and port as the TLS server, plain http.
		http.Redirect(w, r, "http://"+r.Host+"/down", http.StatusFound)
	}))
	defer tls.Close()
	c, err := r.source(r.sources, "key", "").HTTPClient(ctx, tls.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, c, tls.URL+"/up"); err == nil || !strings.Contains(err.Error(), "in the clear") {
		t.Errorf("https to http redirect: %v, want it refused", err)
	}
}

// Two toolsets sharing a slot: a caller that joins the refresh another
// toolset started gets an error naming its own toolset. (Should B load the
// credential after A marked it, it is not connected on its own: the test
// passes either way, and catches the bug whenever B joins A's refresh.)
func TestSharedSlotErrorsNameTheCaller(t *testing.T) {
	t.Parallel()
	reached, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	ts := credentialstest.NewTokenServer(t, credentialstest.TokenServerOptions{
		Hook: func(http.ResponseWriter, *http.Request, url.Values) bool {
			once.Do(func() { close(reached) })
			<-release
			return false
		},
	})
	r := newSrcRig(t, credentials.WithOAuth2App("mail", credentials.OAuth2App{ClientID: ts.ClientID, ClientSecret: credentials.NewSecret(ts.ClientSecret), TokenURL: ts.URL}))
	ctx := scopeCtx(t, "tenant-a", "")
	r.put(ctx, "", "mail", expiring("rt-unknown")) // invalid_grant
	srcs := map[string]credentials.Source{}
	for _, name := range []string{"mail-a", "mail-b"} {
		src, err := r.sources.Source(name, "mail", "")
		if err != nil {
			t.Fatal(err)
		}
		srcs[name] = src
	}
	errs := make(chan error, 1)
	go func() {
		_, err := srcs["mail-a"].Get(ctx)
		errs <- err
	}()
	<-reached // mail-a's refresh is at the token endpoint
	time.AfterFunc(100*time.Millisecond, func() { close(release) })
	_, errB := srcs["mail-b"].Get(ctx)
	notConnected(t, <-errs, "mail-a")
	notConnected(t, errB, "mail-b")
}

// A token endpoint that redirects is not followed: a 307 would resend the
// refresh token and the client secret (in the form) to another host.
func TestTokenEndpointRedirectNotFollowed(t *testing.T) {
	t.Parallel()
	got := make(chan string, 4)
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- string(b) + r.Header.Get("Authorization")
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(elsewhere.Close)
	ts := credentialstest.NewTokenServer(t, credentialstest.TokenServerOptions{
		Hook: func(w http.ResponseWriter, r *http.Request, _ url.Values) bool {
			http.Redirect(w, r, elsewhere.URL+"/token", http.StatusTemporaryRedirect)
			return true
		},
	})
	for _, style := range []credentials.AuthStyle{credentials.AuthInHeader, credentials.AuthInParams} {
		r := newSrcRig(t, credentials.WithOAuth2App("crm", credentials.OAuth2App{ClientID: ts.ClientID, ClientSecret: secret("client"), TokenURL: ts.URL, AuthStyle: style}),
			credentials.WithTokenClient(&http.Client{}))
		ctx := scopeCtx(t, "tenant-a", "")
		expired := expiring("rt-" + canary)
		expired.Expiry = time.Now().Add(-time.Second)
		r.put(ctx, "", "crm", expired)
		_, err := r.source(r.sources, "crm", "").Get(ctx)
		var se *credentials.SourceError
		if !errors.As(err, &se) || !strings.Contains(err.Error(), "307") {
			t.Errorf("style %d: %v, want a SourceError naming the 307", style, err)
		}
		r.put(ctx, "", "api", credentials.ClientCredentials{ClientID: "c", ClientSecret: secret("cc"), TokenURL: ts.URL})
		if _, err := r.source(r.sources, "api", "").Get(ctx); err == nil {
			t.Errorf("style %d: client credentials through a redirect succeeded", style)
		}
	}
	select {
	case b := <-got:
		t.Errorf("the redirect was followed; the other host got %q", b)
	default:
	}
}
