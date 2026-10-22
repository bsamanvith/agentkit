//go:build !noa2a && !noconnectors

package main

import (
	"log/slog"
	"os"

	"github.com/bsamanvith/agentkit/pkg/a2a/client"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// a2aClientFactories is the toolset type "a2a": remote A2A agents as tools
// server out (runner, toolsets) leave the client out too, and behind
// noconnectors with the catalog that lists it (api): a build without the
// catalog would still link the package for its variables.
// server out (runner, toolsets) leave the client out too.
func a2aClientFactories(logger *slog.Logger) []toolset.Factory {
	return []toolset.Factory{client.Factory(client.WithLookupEnv(os.LookupEnv), client.WithLogger(logger))}
}
