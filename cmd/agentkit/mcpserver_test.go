//go:build !nomcp

package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/tool"
)

// TestMCPServerToken: off loopback the MCP server needs the API token, like
// the agent API; with a token, a request without it is refused before it
// reaches a toolset.
func TestMCPServerToken(t *testing.T) {
	t.Parallel()
	log := slog.New(slog.DiscardHandler)
	regs := map[string]tool.Registry{}
	if _, err := newMCPServer(t.Context(), defaultMCPConfig(), regs, nil, "", ":8080", log); err == nil || !strings.Contains(err.Error(), envAPIToken) {
		t.Errorf("no token on :8080: err = %v, want one naming $%s", err, envAPIToken)
	}
	m, err := newMCPServer(t.Context(), defaultMCPConfig(), regs, nil, "s3cret-0123456789abcdef", ":8080", log)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(m.Component.Handler())
	t.Cleanup(ts.Close)
	for _, tc := range []struct {
		auth string
		want int
	}{
		{"", http.StatusUnauthorized},
		{"Bearer wrong", http.StatusUnauthorized},
		{"Basic s3cret-0123456789abcdef", http.StatusUnauthorized},
		{"Bearer s3cret-0123456789abcdef", http.StatusOK},
		{"bearer s3cret-0123456789abcdef", http.StatusOK},
	} {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/mcp/", nil)
		if tc.auth != "" {
			req.Header.Set("Authorization", tc.auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("GET /mcp/ with %q = %d, want %d", tc.auth, resp.StatusCode, tc.want)
		}
	}
}
