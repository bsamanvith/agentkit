//go:build !noagent && nows

package main

import (
	"log/slog"
	"net/http"

	"github.com/bsamanvith/agentkit/pkg/clock"
)

// newWS, in a build without the WebSocket binding (-tags nows), is none:
// /v1/ws is the agent API's 404.
func newWS(http.Handler, clock.Clock, *slog.Logger) (wsBinding, error) { return nil, nil }
