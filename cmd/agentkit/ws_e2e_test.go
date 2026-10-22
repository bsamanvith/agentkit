//go:build !noagent && !nows

package main

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/agentclient/wsclient"
)

// agentkit serve serves the agent API's WebSocket binding at /v1/ws, as
// part of the agent component, behind the API's token: a wrong token's
// hello is refused, and the right one's commands reach the API.
func TestE2EServeWebSocket(t *testing.T) {
	t.Parallel()
	p := startServe(t, agentConfigJSON, 10*time.Second,
		envComponents+"=agent", envAPIToken+"="+e2eToken, "ANTHROPIC_API_KEY=e2e-dummy-key")
	for _, tc := range []struct {
		token string
		want  error
	}{
		{"wrong-token-0123456789abcdef", agentclient.ErrUnauthorized},
		{e2eToken, agentclient.ErrNotFound},
	} {
		tr, err := wsclient.New(p.url, wsclient.WithToken(tc.token))
		if err != nil {
			t.Fatal(err)
		}
		c, err := agentclient.New(p.url, agentclient.WithHTTPClient(&http.Client{Transport: tr}))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Stop(t.Context(), "run_nope"); !errors.Is(err, tc.want) {
			t.Errorf("stop over /v1/ws with token %q: %v, want %v", tc.token, err, tc.want)
		}
		_ = tr.Close()
	}
}
