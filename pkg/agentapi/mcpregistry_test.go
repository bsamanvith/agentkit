package agentapi_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	mcpregistry "github.com/bsamanvith/agentkit/pkg/mcp/registry"
	"github.com/bsamanvith/agentkit/pkg/mcp/servers"
)

// registryFake replays the official registry's answer to a search for
// "linear" (recorded in package mcp/registry's testdata), or fails while
// down is set.
func registryFake(t *testing.T) (*httptest.Server, *atomic.Bool) {
	t.Helper()
	page, err := os.ReadFile("../mcp/registry/testdata/linear-20.json")
	if err != nil {
		t.Fatal(err)
	}
	down := new(atomic.Bool)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path != "/v0.1/servers" || r.URL.Query().Get("search") != "linear" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(page)
	}))
	t.Cleanup(ts.Close)
	return ts, down
}

// mcpRegistryRig is adminRig with the registry fake searched, uncached, and
// the allowlist allow for every tenant.
func mcpRegistryRig(t *testing.T, allow func(mcpURL string) []string) (*rig, string, *atomic.Bool) {
	t.Helper()
	reg, down := registryFake(t)
	c, err := mcpregistry.New(reg.URL, mcpregistry.WithCacheTTL(0))
	if err != nil {
		t.Fatal(err)
	}
	r, _, mcpURL := adminRig(t,
		func(string) servers.Option { return servers.WithRegistry(c) },
		func(u string) servers.Option {
			a, err := servers.NewAllowlist(allow(u), nil)
			if err != nil {
				t.Fatal(err)
			}
			return servers.WithAllowlist(a)
		},
	)
	return r, mcpURL, down
}

// Criterion "Search" through the API: only servers with a remote, marked
// with what the tenant may add; admins only.
func TestRegistrySearchAPI(t *testing.T) {
	t.Parallel()
	r, _, _ := mcpRegistryRig(t, func(string) []string { return []string{"https://mcp.linear.app/*"} })
	resp, body := r.do("GET", "/v1/mcp-registry/search?q=linear&limit=20", "", as(keyA)...)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("search = %d %s", resp.StatusCode, body)
	}
	got := decodeAs[wire.RegistrySearch](t, body)
	var names []string
	for _, s := range got.Servers {
		names = append(names, s.Name)
		if len(s.Remotes) == 0 {
			t.Errorf("%s has no remote", s.Name)
		}
	}
	want := []string{
		"app.linear/linear", "io.github.Evozim/linear-broker", "io.github.pipeworx-io/linear",
		"io.github.sadri-dridi/linear-id-ok", "io.github.sadri-dridi/linear-id-shape", "io.usefulapi/linear",
	}
	if got.V != agentapi.WireVersion || got.Query != "linear" || !got.Restricted || !slices.Equal(names, want) {
		t.Fatalf("search: v %d, query %q, restricted %v, servers %q; want %q", got.V, got.Query, got.Restricted, names, want)
	}
	linear := got.Servers[0]
	if linear.SuggestedName != "linear" || linear.Remotes[0].URL != "https://mcp.linear.app/mcp" || linear.Remotes[0].Transport != "http" || !linear.Remotes[0].Allowed {
		t.Errorf("linear: %+v", linear)
	}
	if broker := got.Servers[1]; broker.Remotes[0].Transport != "sse" || broker.Remotes[0].Allowed {
		t.Errorf("an SSE remote outside the allowlist: %+v", broker.Remotes)
	}
	if resp, body := r.do("GET", "/v1/mcp-registry/search?q=linear", "", as(keyRun)...); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a key that may not administer: %d %s", resp.StatusCode, body)
	}
	for _, q := range []string{"", "?q=", "?q=linear&limit=0", "?q=linear&limit=101", "?q=linear&limit=x"} {
		if resp, body := r.do("GET", "/v1/mcp-registry/search"+q, "", as(keyA)...); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("search%s = %d %s, want 400", q, resp.StatusCode, body)
		}
	}
	// Without a registry, there is no search.
	plain, _, _ := adminRig(t)
	if resp, body := plain.do("GET", "/v1/mcp-registry/search?q=linear", "", as(keyA)...); resp.StatusCode != http.StatusNotFound {
		t.Errorf("no registry: %d %s, want 404", resp.StatusCode, body)
	}
}

// Criterion "Allowlist": the API refuses a URL outside mcp_servers.allow,
// whether or not the registry lists it, and adds one inside it.
func TestRegistryAllowlistAPI(t *testing.T) {
	t.Parallel()
	r, mcpURL, _ := mcpRegistryRig(t, func(u string) []string { return []string{"https://mcp.linear.app/*", u + "/*"} })
	resp, body := r.do("POST", "/v1/toolsets", `{"type":"mcp","name":"broker","url":"https://linear-broker-mcp.vercel.app/api/mcp","transport":"sse"}`, as(keyA)...)
	if e := decodeAs[agentapi.ErrorResponse](t, body); resp.StatusCode != http.StatusForbidden || e.Error == nil || e.Error.Code != agentapi.CodeURLNotAllowed {
		t.Errorf("a URL outside the allowlist: %d %s, want 403 %s", resp.StatusCode, body, agentapi.CodeURLNotAllowed)
	}
	if resp, body := r.do("GET", "/v1/toolsets/broker/review", "", as(keyA)...); resp.StatusCode != http.StatusNotFound {
		t.Errorf("the refused toolset exists: %d %s", resp.StatusCode, body)
	}
	if resp, body := r.do("POST", "/v1/toolsets", `{"type":"mcp","name":"linear","url":"`+mcpURL+`"}`, as(keyA)...); resp.StatusCode != http.StatusCreated {
		t.Errorf("a URL the allowlist has: %d %s", resp.StatusCode, body)
	}
}

// Criterion "Registry down": the search answers 502 registry_unavailable,
// and adding a server by its URL still works.
func TestRegistryDownAPI(t *testing.T) {
	t.Parallel()
	r, mcpURL, down := mcpRegistryRig(t, func(string) []string { return nil })
	down.Store(true)
	resp, body := r.do("GET", "/v1/mcp-registry/search?q=linear", "", as(keyA)...)
	if e := decodeAs[agentapi.ErrorResponse](t, body); resp.StatusCode != http.StatusBadGateway || e.Error == nil || e.Error.Code != agentapi.CodeRegistryUnavailable {
		t.Errorf("a registry that is down: %d %s, want 502 %s", resp.StatusCode, body, agentapi.CodeRegistryUnavailable)
	}
	if resp, body := r.do("POST", "/v1/toolsets", `{"type":"mcp","name":"linear","url":"`+mcpURL+`"}`, as(keyA)...); resp.StatusCode != http.StatusCreated {
		t.Errorf("adding by URL with the registry down: %d %s", resp.StatusCode, body)
	}
	down.Store(false)
	if resp, body := r.do("GET", "/v1/mcp-registry/search?q=linear", "", as(keyA)...); resp.StatusCode != http.StatusOK {
		t.Errorf("once it is back: %d %s", resp.StatusCode, body)
	}
}
