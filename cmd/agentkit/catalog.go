package main

import (
	"log/slog"
	"os"

	"github.com/bsamanvith/agentkit/connectors/exa"
	"github.com/bsamanvith/agentkit/connectors/google/calendar"
	"github.com/bsamanvith/agentkit/connectors/parallel"
	"github.com/bsamanvith/agentkit/connectors/tavily"
	"github.com/bsamanvith/agentkit/pkg/coretools"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// newCatalog lists the connectors compiled into this binary. A connector
// behind a build tag will add its factory here from a small file of its own,
// so config naming a connector that was not compiled in fails at startup
// (toolset.ErrUnknownType lists the types that were).
func newCatalog() (*toolset.Catalog, error) {
	// Connectors log their auth mode at startup. serve's own logger is not
	// passed here yet (newCatalog takes none), hence a JSON logger on stderr.
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	return toolset.NewCatalog(
		toolset.Demo(),
		coretools.Math(),
		coretools.Files(),
		tavily.Factory(tavily.WithLookupEnv(os.LookupEnv), tavily.WithLogger(logger)),
		exa.Factory(exa.WithLookupEnv(os.LookupEnv)),
		parallel.Factory(parallel.WithLookupEnv(os.LookupEnv), parallel.WithLogger(logger)),
		calendar.Factory(calendar.WithLookupEnv(os.LookupEnv), calendar.WithLogger(logger)),
	)
}
