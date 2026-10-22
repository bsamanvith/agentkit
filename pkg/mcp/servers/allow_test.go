package servers_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/mcp/registry"
	"github.com/bsamanvith/agentkit/pkg/mcp/servers"
	"github.com/bsamanvith/agentkit/pkg/store/memory"
)

func TestAllowlistPatterns(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		pattern string
		allow   []string
		refuse  []string
	}{
		{
			pattern: "mcp.linear.app",
			allow:   []string{"https://mcp.linear.app/mcp", "https://MCP.Linear.App/sse", "https://mcp.linear.app:443/mcp", "https://mcp.linear.app"},
			refuse:  []string{"http://mcp.linear.app/mcp", "https://mcp.linear.app:8443/mcp", "https://evil.mcp.linear.app/mcp", "https://mcp.linear.app.evil.com/mcp", "https://u:p@mcp.linear.app/mcp"},
		},
		{
			pattern: "https://*.example.com/mcp/*",
			allow:   []string{"https://a.example.com/mcp/x", "https://a.b.example.com/mcp/x/y"},
			refuse:  []string{"https://example.com/mcp/x", "https://a.example.com/other", "https://evil.com/x.example.com/mcp/", "https://evil.com?.example.com/mcp/", "https://aexample.com/mcp/x", "https://evilexample.com/mcp/x"},
		},
		{
			pattern: "https://mcp.example.com/mcp",
			allow:   []string{"https://mcp.example.com/mcp", "https://mcp.example.com/mcp?x=1"},
			refuse:  []string{"https://mcp.example.com/mcp/more", "https://mcp.example.com/", "https://mcp.example.com/MCP"},
		},
		{
			pattern: "http://mcp.internal:8080/*",
			allow:   []string{"http://mcp.internal:8080/", "http://mcp.internal:8080/a/b"},
			refuse:  []string{"https://mcp.internal:8080/", "http://mcp.internal/", "http://mcp.internal:8081/"},
		},
		{
			// Bypasses: userinfo, a trailing dot, an odd port, dot segments
			// (plain, encoded, behind an encoded slash), a backslash, a
			// prefix that is not a segment.
			pattern: "https://mcp.example.com/mcp/*",
			allow:   []string{"https://mcp.example.com/mcp/x", "https://MCP.EXAMPLE.COM/mcp/a/b", "https://mcp.example.com:/mcp/x"},
			refuse: []string{
				"https://mcp.example.com@evil.com/mcp/x", `https://evil.com\@mcp.example.com/mcp/x`, "https://mcp.example.com./mcp/x",
				"https://mcp.example.com:0443/mcp/x", "https://mcp.example.com/mcp/../admin", "https://mcp.example.com/mcp/%2e%2e/admin",
				"https://mcp.example.com/mcp/..%2fadmin", "https://mcp.example.com/mcp/./x", `https://mcp.example.com/mcp/..\admin`,
				"https://mcp.example.com/mcp/..%5cadmin", "https://mcp.example.com/mcpx", "https://mcp.example.com/mcp%2f..%2fadmin",
				"https://mcp.example.com%2eevil.com/mcp/x",
			},
		},
		{
			// Hosts are ASCII: an IDN is compared in its xn-- form, and a
			// host that lowers to ASCII (the Kelvin sign) is refused.
			pattern: "*.kubernetes.io",
			allow:   []string{"https://xn--80ak6aa92e.kubernetes.io/"},
			refuse:  []string{"https://x.\u212Aubernetes.io/", "https://\u0430pple.kubernetes.io/"},
		},
		{
			pattern: "https://*",
			allow:   []string{"https://anything.example.org/x"},
			refuse:  []string{"http://anything.example.org/x"},
		},
	} {
		a, err := servers.NewAllowlist([]string{tc.pattern}, nil)
		if err != nil {
			t.Fatalf("%s: %v", tc.pattern, err)
		}
		ctx := ctxOf(t, "acme")
		for _, u := range tc.allow {
			if !a.Allows(ctx, u) {
				t.Errorf("%s refuses %s", tc.pattern, u)
			}
		}
		for _, u := range tc.refuse {
			if a.Allows(ctx, u) {
				t.Errorf("%s allows %s", tc.pattern, u)
			}
		}
	}
	for _, bad := range []string{"ftp://x", "https://a*b.com", "https://*.", "https://x/?q", "https://u@x", "https://x#f", "", "https://*.a*.com", "https://bücher.example"} {
		if _, err := servers.NewAllowlist([]string{bad}, nil); err == nil {
			t.Errorf("pattern %q accepted", bad)
		}
	}
	if _, err := servers.NewAllowlist(nil, map[string][]string{"no tenant!": nil}); err == nil {
		t.Error("an invalid tenant accepted")
	}
}

// The setting is per tenant: a tenant's own list replaces the one of every
// tenant, and an empty list allows any URL.
func TestAllowlistTenants(t *testing.T) {
	t.Parallel()
	a, err := servers.NewAllowlist([]string{"mcp.linear.app"}, map[string][]string{"open": {}, "strict": {"mcp.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	const linear, example = "https://mcp.linear.app/mcp", "https://mcp.example.com/mcp"
	for tenant, want := range map[string][2]bool{"acme": {true, false}, "open": {true, true}, "strict": {false, true}} {
		ctx := ctxOf(t, tenant)
		if got := [2]bool{a.Allows(ctx, linear), a.Allows(ctx, example)}; got != want {
			t.Errorf("tenant %s allows linear, example: %v, want %v", tenant, got, want)
		}
		if a.Restricted(ctx) == (tenant == "open") {
			t.Errorf("tenant %s: restricted %v", tenant, a.Restricted(ctx))
		}
	}
	var none *servers.Allowlist
	if !none.Allows(ctxOf(t, "acme"), example) || none.Restricted(ctxOf(t, "acme")) {
		t.Error("no allowlist must allow any URL")
	}
}

// Criterion "Allowlist": a URL outside the tenant's mcp_servers.allow is
// refused by Add itself, before anything connects to it.
func TestAddOutsideAllowlist(t *testing.T) {
	t.Parallel()
	var hits atomic.Int64
	r := &rig{t: t, st: memory.New(), env: map[string]string{}, allow: []string{"127.0.0.1"}}
	r.f = newFake(t, func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { hits.Add(1); h.ServeHTTP(w, req) })
	})
	a, err := servers.NewAllowlist([]string{"https://mcp.linear.app/*"}, map[string][]string{"lab": {r.f.ts.URL + "/*"}})
	if err != nil {
		t.Fatal(err)
	}
	r.m = r.manager(servers.WithAllowlist(a))
	_, err = r.m.Add(ctxOf(t, "acme"), "key-admin", wire.AddToolsetRequest{Type: "mcp", Name: "linear", URL: r.f.ts.URL})
	if !errors.Is(err, agentapi.ErrURLNotAllowed) {
		t.Fatalf("adding a URL outside the allowlist: %v, want ErrURLNotAllowed", err)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the refused server got %d requests", n)
	}
	if ts := r.add(ctxOf(t, "lab"), "linear"); ts.Name != "linear" || hits.Load() == 0 {
		t.Errorf("a tenant whose list has the URL: %+v, %d requests", ts, hits.Load())
	}
}

// A redirect is followed only to a URL the tenant's list includes, and,
// for a toolset that sends headers, only on the server's own origin: the
// client adds the headers to every request.
func TestAddRedirects(t *testing.T) {
	t.Parallel()
	var hits, withKey atomic.Int64
	r := &rig{t: t, st: memory.New(), env: map[string]string{"LINEAR_KEY": "s3cret"}, allow: []string{"127.0.0.1"}}
	r.f = newFake(t, func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			hits.Add(1)
			if req.Header.Get("Authorization") != "" {
				withKey.Add(1)
			}
			h.ServeHTTP(w, req)
		})
	})
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, r.f.ts.URL+req.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(front.Close)
	add := func(m *servers.Manager, req wire.AddToolsetRequest) error {
		req.Type, req.URL = "mcp", front.URL+"/mcp"
		_, err := m.Add(ctxOf(t, "acme"), "key-admin", req)
		return err
	}

	only, err := servers.NewAllowlist([]string{front.URL + "/*"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := add(r.manager(servers.WithAllowlist(only)), wire.AddToolsetRequest{Name: "a"}); !errors.Is(err, agentapi.ErrURLNotAllowed) {
		t.Errorf("a redirect off the allowlist: %v, want ErrURLNotAllowed", err)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the server off the allowlist got %d requests", n)
	}

	m := r.manager()
	err = add(m, wire.AddToolsetRequest{Name: "b", Auth: wire.AuthHeaders, Headers: map[string]string{"Authorization": "Bearer ${LINEAR_KEY}"}})
	if !errors.Is(err, agentapi.ErrServerUnreachable) {
		t.Errorf("a redirect to another origin with headers: %v, want ErrServerUnreachable", err)
	}
	if n := withKey.Load(); n != 0 {
		t.Errorf("the other origin got the toolset's header %d times", n)
	}
	if err := add(m, wire.AddToolsetRequest{Name: "c"}); err != nil || hits.Load() == 0 {
		t.Errorf("a redirect with no headers and no allowlist: %v (%d requests)", err, hits.Load())
	}
}

// The registry's servers come out sanitized, named for the add form, and
// marked with whether the tenant may add them; a registry that is down is
// ErrRegistryUnavailable.
func TestDirectorySearch(t *testing.T) {
	t.Parallel()
	var down atomic.Bool
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if down.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"servers":[
			{"server":{"name":"app.linear/Linear MCP","title":"Linear\u200b","description":"Issues.\u202eevil\u0007","version":"1.0.1","remotes":[
				{"type":"streamable-http","url":"https://mcp.linear.app/mcp","headers":[{"name":"Authorization","isRequired":true,"isSecret":true}]},
				{"type":"sse","url":"https://mcp.linear.app/sse"}]}},
			{"server":{"name":"com.example/{tenant}","version":"2","remotes":[{"type":"streamable-http","url":"https://{tenant}.example.com/mcp"}]}},
			{"server":{"name":"com.example/local","version":"1","packages":[{"registryType":"npm","identifier":"x"}]}}
		],"metadata":{}}`))
	}))
	t.Cleanup(reg.Close)
	c, err := registry.New(reg.URL, registry.WithCacheTTL(0))
	if err != nil {
		t.Fatal(err)
	}
	a, err := servers.NewAllowlist([]string{"https://mcp.linear.app/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := newRig(t).manager(servers.WithRegistry(c), servers.WithAllowlist(a))
	if newRig(t).m.Directory() != nil {
		t.Error("a manager with no registry has a directory")
	}
	got, err := m.Directory().Search(ctxOf(t, "acme"), "linear", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got.Registry != reg.URL || !got.Restricted || len(got.Servers) != 2 {
		t.Fatalf("search: %+v", got)
	}
	linear := got.Servers[0]
	if linear.SuggestedName != "linear-mcp" || linear.Title != "Linear" || strings.ContainsAny(linear.Description, "\u202e\u0007") || linear.Status != "active" {
		t.Errorf("linear: %+v", linear)
	}
	want := []wire.RegistryRemote{
		{Transport: "http", URL: "https://mcp.linear.app/mcp", Allowed: true, Headers: []wire.RegistryHeader{{Name: "Authorization", Required: true, Secret: true}}},
		{Transport: "sse", URL: "https://mcp.linear.app/sse", Allowed: false},
	}
	if len(linear.Remotes) != 2 || linear.Remotes[0].Transport != want[0].Transport || !linear.Remotes[0].Allowed ||
		len(linear.Remotes[0].Headers) != 1 || linear.Remotes[0].Headers[0] != want[0].Headers[0] ||
		linear.Remotes[1].Transport != "sse" || linear.Remotes[1].Allowed {
		t.Errorf("remotes %+v, want %+v", linear.Remotes, want)
	}
	if tmpl := got.Servers[1]; tmpl.SuggestedName != "tenant" || len(tmpl.Remotes) != 1 || len(tmpl.Remotes[0].Variables) != 1 || tmpl.Remotes[0].Variables[0] != "tenant" {
		t.Errorf("a templated remote: %+v", tmpl)
	}
	if _, err := m.Directory().Search(ctxOf(t, "acme"), "", 10); !errors.Is(err, agentapi.ErrToolsetInvalid) {
		t.Errorf("an empty query: %v", err)
	}
	down.Store(true)
	if _, err := m.Directory().Search(ctxOf(t, "acme"), "linear", 10); !errors.Is(err, agentapi.ErrRegistryUnavailable) {
		t.Errorf("a registry that is down: %v, want ErrRegistryUnavailable", err)
	}
}
