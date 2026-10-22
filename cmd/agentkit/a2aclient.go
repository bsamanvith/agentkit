//go:build !noa2a

package main

import (
	"log/slog"
	"os"

	"github.com/bsamanvith/agentkit/pkg/a2a/client"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// a2aClientFactories is the toolset type "a2a": remote A2A agents as tools
// (T068). It is behind noa2a with the A2A server: the images that leave the
// server out (runner, toolsets) leave the client out too.
func a2aClientFactories(logger *slog.Logger) []toolset.Factory {
	return []toolset.Factory{client.Factory(client.WithLookupEnv(os.LookupEnv), client.WithLogger(logger))}
}
