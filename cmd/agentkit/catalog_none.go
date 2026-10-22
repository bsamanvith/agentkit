//go:build noconnectors

package main

import (
	"log/slog"

	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// newCatalog, in a build without connectors, has the demo toolset only: the
// runner and the agent API of a split deployment take their toolsets from
// the toolsets component ($AGENTKIT_TOOLSETS_URL), and a config naming
// another type fails at startup with toolset.ErrUnknownType.
func newCatalog(*slog.Logger) (*toolset.Catalog, error) {
	return toolset.NewCatalog(toolset.Demo())
}
