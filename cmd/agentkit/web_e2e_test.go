package main

// End-to-end test of the web component in agentkit serve: the built binary
// serves the embedded console at / next to another component's paths. The
// browser test (web/apps/console/e2e) drives the console itself.

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/web"
)

func TestE2EServeWeb(t *testing.T) {
	t.Parallel()
	p := startServe(t, serveConfigJSON, 5*time.Second, envComponents+"=mcp-server,web")

	get := func(path string) (*http.Response, string) {
		t.Helper()
		resp, err := http.Get(p.url + path) //nolint:noctx // test against a local server
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp, string(b)
	}

	for _, path := range []string{"/", "/c/conv_1", "/agents"} {
		resp, body := get(path)
		if resp.StatusCode != http.StatusOK || !strings.Contains(body, `<div id="root">`) ||
			resp.Header.Get("Content-Security-Policy") != web.CSP {
			t.Errorf("GET %s = %s, CSP %q; want the console with its CSP", path, resp.Status, resp.Header.Get("Content-Security-Policy"))
		}
	}
	// The runtime's endpoints and the other component keep their paths.
	if resp, body := get("/healthz"); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"ok"`) {
		t.Errorf("GET /healthz = %s %q", resp.Status, body)
	}
	if resp, body := get("/mcp/demo-a"); strings.Contains(body, `<div id="root">`) {
		t.Errorf("GET /mcp/demo-a = %s with the console", resp.Status)
	}
	if resp, _ := get("/v1/agents"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /v1/agents without the agent component = %s, want 404", resp.Status)
	}

	var ready struct {
		Status     string
		Components []struct{ Name, Status string }
	}
	if code := p.getJSON(t, "/readyz", &ready); code != http.StatusOK || ready.Status != "ready" ||
		len(ready.Components) != 2 || ready.Components[1].Name != web.Name || ready.Components[1].Status != "ready" {
		t.Errorf("/readyz = %d %+v, want web ready", code, ready)
	}
}
