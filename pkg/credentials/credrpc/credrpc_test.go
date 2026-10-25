package credrpc_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/credentials/credrpc"
	"github.com/bsamanvith/agentkit/pkg/credentials/memory"
	"github.com/bsamanvith/agentkit/pkg/rpc"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

const canary = "credrpc-canary-91fa"

// recorder is a conn that keeps every frame the server answers with.
type recorder struct {
	conn rpc.Conn
	mu   sync.Mutex
	seen strings.Builder
}

func (r *recorder) Call(ctx context.Context, method string, req json.RawMessage) iter.Seq2[json.RawMessage, error] {
	return func(yield func(json.RawMessage, error) bool) {
		for data, err := range r.conn.Call(ctx, method, req) {
			r.mu.Lock()
			r.seen.Write(data)
			r.mu.Unlock()
			if !yield(data, err) {
				return
			}
		}
	}
}

func (r *recorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seen.String()
}

// rig is an agent API's vault and sources served over credrpc, and a
// toolsets component's remote sources calling them.
type rig struct {
	vault  credentials.Vault
	remote *credentials.Sources
	wire   *recorder
}

func newRig(t *testing.T, slots map[string]credentials.Scope) *rig {
	t.Helper()
	vault := credentials.NewVault(memory.New(), credentials.NewSealer(mustRing(t)))
	local, err := credentials.NewSources(vault)
	if err != nil {
		t.Fatal(err)
	}
	srv := rpc.NewServer()
	if err := credrpc.Register(srv, local, slots); err != nil {
		t.Fatal(err)
	}
	wire := &recorder{conn: rpc.Pipe(srv)}
	remote, err := credentials.NewRemoteSources(credrpc.NewClient(wire))
	if err != nil {
		t.Fatal(err)
	}
	return &rig{vault: vault, remote: remote, wire: wire}
}

func as(t *testing.T, tenant, user string) context.Context {
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

func (r *rig) put(ctx context.Context, t *testing.T, user, slot string, c credentials.Credential) {
	t.Helper()
	if _, err := r.vault.Put(ctx, user, slot, c, 0); err != nil {
		t.Fatal(err)
	}
}

// A toolset on the remote side sends the calling user's own key, and only
// theirs: another user, or the same user in another tenant, is not
// connected.
func TestRemoteSourceUsesTheCallersKey(t *testing.T) {
	t.Parallel()
	r := newRig(t, map[string]credentials.Scope{"exa": credentials.ScopeUser})
	r.put(as(t, "t1", "u1"), t, "u1", "exa", credentials.APIKey{Key: credentials.NewSecret("key-" + canary)})
	var got []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		got = append(got, req.Header.Get("X-Api-Key"))
	}))
	t.Cleanup(api.Close)
	src, err := r.remote.Source("search", "exa", credentials.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	hc, err := src.HTTPClient(as(t, "t1", "u1"), nil, credentials.APIKeyHeader("X-API-Key"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := hc.Get(api.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if len(got) != 1 || got[0] != "key-"+canary {
		t.Errorf("the provider got %q, want the user's key", got)
	}
	for _, ctx := range []context.Context{as(t, "t1", "u2"), as(t, "t2", "u1"), as(t, "t1", "")} {
		_, err := src.Get(ctx)
		var nc *credentials.NotConnectedError
		if !errors.As(err, &nc) || nc.Toolset != "search" {
			t.Errorf("%v: %v, want search not connected", tenancy.Carry(ctx), err)
		}
	}
}

// An OAuth2 token crosses with its type, expiry and extras; its refresh
// token stays with the vault.
func TestRefreshTokenStaysWithTheVault(t *testing.T) {
	t.Parallel()
	r := newRig(t, map[string]credentials.Scope{"salesforce": credentials.ScopeUser})
	expiry := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	r.put(as(t, "t1", "u1"), t, "u1", "salesforce", credentials.OAuth2{
		AccessToken: credentials.NewSecret("access-" + canary), RefreshToken: credentials.NewSecret("refresh-" + canary),
		TokenType: "Bearer", Expiry: expiry, Extra: map[string]string{"instance_url": "https://acme.my.salesforce.com"},
	})
	src, err := r.remote.Source("crm", "salesforce", credentials.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	c, err := src.Get(as(t, "t1", "u1"))
	if err != nil {
		t.Fatal(err)
	}
	o, ok := c.(credentials.OAuth2)
	if !ok || o.AccessToken.Reveal() != "access-"+canary || !o.RefreshToken.IsZero() || !o.Expiry.Equal(expiry) ||
		o.Extra["instance_url"] != "https://acme.my.salesforce.com" || o.TokenType != "Bearer" {
		t.Errorf("got %#v", c)
	}
	if w := r.wire.String(); strings.Contains(w, "refresh-"+canary) || !strings.Contains(w, "access-"+canary) {
		t.Errorf("on the wire: %s", w)
	}
}

// The server answers only for its slots, each at its scope: a toolsets
// component can't read a slot the agent API doesn't serve, or a user's
// credential as the tenant's.
func TestOnlyServedSlots(t *testing.T) {
	t.Parallel()
	r := newRig(t, map[string]credentials.Scope{"exa": credentials.ScopeUser, "crm": ""})
	ctx := as(t, "t1", "u1")
	r.put(ctx, t, "u1", "gmail", credentials.APIKey{Key: credentials.NewSecret("gmail-" + canary)})
	r.put(ctx, t, "", "exa", credentials.APIKey{Key: credentials.NewSecret("shared-" + canary)})
	for _, c := range []struct {
		slot  string
		scope credentials.Scope
	}{{"gmail", credentials.ScopeUser}, {"exa", credentials.ScopeTenant}, {"crm", credentials.ScopeUser}} {
		src, err := r.remote.Source("x", c.slot, c.scope)
		if err != nil {
			t.Fatal(err)
		}
		_, err = src.Get(ctx)
		var se *credentials.SourceError
		if !errors.As(err, &se) || !errors.Is(err, credrpc.ErrUnknownSlot) || strings.Contains(err.Error(), canary) {
			t.Errorf("%s (%s): %v, want refused as not served", c.slot, c.scope, err)
		}
	}
	if strings.Contains(r.wire.String(), canary) {
		t.Errorf("a refused slot's secret crossed: %s", r.wire.String())
	}
}

// Over HTTP the server takes only callers with its token.
func TestNeedsTheToken(t *testing.T) {
	t.Parallel()
	srv := rpc.NewServer(rpc.WithToken("rpc-token"))
	local, err := credentials.NewSources(credentials.NewVault(memory.New(), credentials.NewSealer(mustRing(t))))
	if err != nil {
		t.Fatal(err)
	}
	if err := credrpc.Register(srv, local, map[string]credentials.Scope{"exa": credentials.ScopeUser}); err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv)
	t.Cleanup(hs.Close)
	for token, want := range map[string]error{"wrong": rpc.ErrUnauthenticated, "rpc-token": credentials.ErrNotConnected} {
		conn, err := rpc.Dial(hs.URL, rpc.WithBearer(token))
		if err != nil {
			t.Fatal(err)
		}
		_, err = credrpc.NewClient(conn).Fetch(as(t, "t1", "u1"), "search", "exa", credentials.ScopeUser)
		if !errors.Is(err, want) {
			t.Errorf("token %q: %v, want %v", token, err, want)
		}
	}
}

func TestRegisterChecksSlots(t *testing.T) {
	t.Parallel()
	for name, slots := range map[string]map[string]credentials.Scope{
		"bad slot":  {"no spaces": ""},
		"bad scope": {"exa": "everyone"},
	} {
		if err := credrpc.Register(rpc.NewServer(), &credentials.Sources{}, slots); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := credrpc.Register(rpc.NewServer(), nil, nil); err == nil {
		t.Error("nil provider: accepted")
	}
}

func mustRing(t *testing.T) *credentials.KeyRing {
	t.Helper()
	k := make([]byte, credentials.KeySize)
	_, _ = rand.Read(k)
	ring, err := credentials.NewKeyRing("k1", map[string][]byte{"k1": k})
	if err != nil {
		t.Fatal(err)
	}
	return ring
}
