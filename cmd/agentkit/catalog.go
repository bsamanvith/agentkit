package main

import (
	"log/slog"
	"os"

	"github.com/bsamanvith/agentkit/connectors/tavily"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// newCatalog lists the connectors compiled into this binary. A connector
// behind a build tag will add its factory here from a small file of its own,
// so config naming a connector that was not compiled in fails at startup
// (toolset.ErrUnknownType lists the types that were).
func newCatalog() (*toolset.Catalog, error) {
	return toolset.NewCatalog(
		toolset.Demo(),
		// Logs the auth mode at startup. serve's own logger is not passed here
		// yet (newCatalog takes none), hence a JSON logger on stderr.
		tavily.Factory(tavily.WithLookupEnv(os.LookupEnv), tavily.WithLogger(slog.New(slog.NewJSONHandler(os.Stderr, nil)))),
	)
}
