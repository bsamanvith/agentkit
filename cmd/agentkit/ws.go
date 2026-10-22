//go:build !noagent && !nows

package main

import (
	"log/slog"
	"net/http"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wsapi"
	"github.com/bsamanvith/agentkit/pkg/clock"
)

// newWS is the agent API's WebSocket binding (T091), which the agent
// component serves at /v1/ws over the API's handler. The nows tag leaves it
// out (ws_none.go).
func newWS(api http.Handler, clk clock.Clock, log *slog.Logger) (wsBinding, error) {
	ws, err := wsapi.New(api, wsapi.WithClock(clk), wsapi.WithLogger(log.With("component", wsapi.Name)))
	if err != nil {
		return nil, err
	}
	return ws, nil
}
