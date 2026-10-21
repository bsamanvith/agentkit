package resourcemeta_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/auth/authtest"
	"github.com/bsamanvith/agentkit/pkg/auth/resourcemeta"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// server is a resource server as serve builds one: the metadata, and /mcp/
// behind resourcemeta.Middleware. Its resource is its own URL.
func server(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	acme, _ := tenancy.NewTenant("acme")
	authn := authtest.NewFake(map[string]auth.Principal{"good-token-0123456789": {Tenant: acme, Toolsets: []string{"*"}}})
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	meta, err := resourcemeta.Handler(resourcemeta.Config{
		Resource:             srv.URL,
		AuthorizationServers: []string{"https://id.example.com"},
		ScopesSupported:      []string{"agent:run", "mcp:crm"},
		ResourceName:         "agentkit",
		Paths:                []string{"/mcp/"},
	})
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle(resourcemeta.WellKnown, meta)
	mux.Handle(resourcemeta.WellKnown+"/", meta)
	mux.Handle("/mcp/", resourcemeta.Middleware(authn, srv.URL, []string{"/mcp/"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})))
	return srv, srv.URL
}

func TestMetadata(t *testing.T) {
	t.Parallel()
	srv, base := server(t)
	for _, tc := range []struct{ metadata, resource string }{
		{base + resourcemeta.WellKnown, base},
		{base + resourcemeta.WellKnown + "/mcp/crm", base + "/mcp/crm"},
	} {
		// The SDK's client checks the resource (RFC 9728 §3.3) and the
		// authorization servers' schemes.
		m, err := oauthex.GetProtectedResourceMetadata(t.Context(), tc.metadata, tc.resource, srv.Client())
		if err != nil {
			t.Fatalf("%s: %v", tc.metadata, err)
		}
		if m.Resource != tc.resource || !slices.Equal(m.AuthorizationServers, []string{"https://id.example.com"}) ||
			!slices.Equal(m.ScopesSupported, []string{"agent:run", "mcp:crm"}) || !slices.Equal(m.BearerMethodsSupported, []string{"header"}) || m.ResourceName != "agentkit" {
			t.Errorf("%s: %+v", tc.metadata, m)
		}
	}
	for _, p := range []string{resourcemeta.WellKnown + "/v1/runs", resourcemeta.WellKnown + "x", resourcemeta.WellKnown + "/"} {
		resp, err := srv.Client().Get(base + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, resp.StatusCode)
		}
	}
}

// TestUnauthorizedPointsAtMetadata: a 401 from /mcp/x carries
// resource_metadata, and following it gives the metadata of /mcp/x, as an
// MCP client (the SDK's) finds it.
func TestUnauthorizedPointsAtMetadata(t *testing.T) {
	t.Parallel()
	srv, base := server(t)
	for path, want := range map[string]string{
		"/mcp/x":     base + resourcemeta.WellKnown + "/mcp/x",
		"/mcp/x/sub": base + resourcemeta.WellKnown + "/mcp/x",
		"/mcp/":      base + resourcemeta.WellKnown,
	} {
		for _, token := range []string{"", "wrong-token-0123456789"} {
			req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, base+path, nil)
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("%s: %d, want 401", path, resp.StatusCode)
			}
			cs, err := oauthex.ParseWWWAuthenticate(resp.Header.Values("WWW-Authenticate"))
			if err != nil || len(cs) != 1 || cs[0].Scheme != "bearer" {
				t.Fatalf("%s: challenge %q: %v", path, resp.Header.Values("WWW-Authenticate"), err)
			}
			if got := cs[0].Params["resource_metadata"]; got != want {
				t.Errorf("%s: resource_metadata = %q, want %q", path, got, want)
			}
			if token != "" && cs[0].Params["error"] != "invalid_token" {
				t.Errorf("%s with a token: error = %q", path, cs[0].Params["error"])
			}
		}
	}
	m, err := oauthex.GetProtectedResourceMetadata(t.Context(), base+resourcemeta.WellKnown+"/mcp/x", base+"/mcp/x", srv.Client())
	if err != nil || m.Resource != base+"/mcp/x" {
		t.Errorf("the metadata a 401 points at: %+v, %v", m, err)
	}

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/mcp/x", nil)
	req.Header.Set("Authorization", "Bearer good-token-0123456789")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("a good token: %d", resp.StatusCode)
	}
}

func TestHandlerConfig(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]resourcemeta.Config{
		"no authorization server": {Resource: "https://a.example"},
		"resource with a path":    {Resource: "https://a.example/x", AuthorizationServers: []string{"https://id.example"}},
		"resource not a URL":      {Resource: "a.example", AuthorizationServers: []string{"https://id.example"}},
		"resource with a query":   {Resource: "https://a.example?x=1", AuthorizationServers: []string{"https://id.example"}},
	} {
		if _, err := resourcemeta.Handler(c); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := resourcemeta.Handler(resourcemeta.Config{Resource: "https://a.example/", AuthorizationServers: []string{"https://id.example"}}); err != nil {
		t.Errorf("an origin with a slash: %v", err)
	}
}

func Example() {
	h, _ := resourcemeta.Handler(resourcemeta.Config{
		Resource:             "https://agents.example.com",
		AuthorizationServers: []string{"https://id.example.com"},
		Paths:                []string{"/mcp/"},
	})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, resourcemeta.WellKnown+"/mcp/crm", nil))
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	fmt.Println(m["resource"], m["authorization_servers"])
	fmt.Println(resourcemeta.URL("https://agents.example.com", "/mcp/crm"))
	// Output:
	// https://agents.example.com/mcp/crm [https://id.example.com]
	// https://agents.example.com/.well-known/oauth-protected-resource/mcp/crm
}
