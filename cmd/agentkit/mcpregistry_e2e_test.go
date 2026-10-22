//go:build !nomcp

package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestE2EToolsetsSearch: the serving binary searches the registry its
// config names (a replay of the official one), marks what mcp_servers.allow
// refuses, refuses to add such a URL, and adds by URL while the registry
// is down.
func TestE2EToolsetsSearch(t *testing.T) {
	t.Parallel()
	page, err := os.ReadFile("../../pkg/mcp/registry/testdata/linear-20.json")
	if err != nil {
		t.Fatal(err)
	}
	var down atomic.Bool
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() || r.URL.Path != "/sub/v0.1/servers" {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write(page)
	}))
	t.Cleanup(reg.Close)
	up := upstreams(t)
	p := startServe(t, `{
		"runtime_toolsets": {
			"allow_hosts": ["127.0.0.1"],
			"registry": {"url": "`+reg.URL+`/sub"},
			"mcp_servers": {"allow": ["https://mcp.linear.app/*", "`+up.URL+`/*"]}
		}
	}`, 10*time.Second, envComponents+"=agent", envAPIToken+"="+e2eToken, "ANTHROPIC_API_KEY=e2e-dummy-key")
	cmd := func(want int, args ...string) string {
		t.Helper()
		var stdout, stderr strings.Builder
		env := map[string]string{envServer: p.url, envAPIToken: e2eToken}
		c := cli{stdout: &stdout, stderr: &stderr, getenv: func(k string) string { return env[k] }}
		if code := c.main(t.Context(), append([]string{"toolsets"}, args...)); code != want {
			t.Fatalf("agentkit toolsets %q = %d, want %d; stderr: %s", args, code, want, stderr.String())
		}
		return stdout.String() + stderr.String()
	}

	out := cmd(0, "search", "linear")
	if !regexp.MustCompile(`linear\s+http\s+https://mcp.linear.app/mcp\s*\n`).MatchString(out) ||
		!regexp.MustCompile(`linear-broker\s+sse\s+https://linear-broker-mcp.vercel.app/api/mcp\s+not allowed here`).MatchString(out) ||
		strings.Contains(out, "adelaidasofia") || !strings.Contains(out, "6 servers") {
		t.Errorf("search printed\n%s", out)
	}
	if out := cmd(exitClient, "add", "-name", "broker", "https://linear-broker-mcp.vercel.app/api/mcp"); !strings.Contains(out, "url_not_allowed") {
		t.Errorf("adding a URL outside mcp_servers.allow printed %s", out)
	}
	down.Store(true) // a new search: "linear" stays cached
	if out := cmd(exitClient, "search", "github"); !strings.Contains(out, "registry_unavailable") {
		t.Errorf("searching a registry that is down printed %s", out)
	}
	if out := cmd(0, "add", "-name", "linear", up.URL+"/mcp/a"); !strings.Contains(out, "review_needed") {
		t.Errorf("adding by URL with the registry down printed %s", out)
	}
}
