//go:build !noconnectors

package main

import (
	"log/slog"
	"os"

	"github.com/bsamanvith/agentkit/connectors/exa"
	"github.com/bsamanvith/agentkit/connectors/google/calendar"
	"github.com/bsamanvith/agentkit/connectors/google/drive"
	"github.com/bsamanvith/agentkit/connectors/google/gmail"
	"github.com/bsamanvith/agentkit/connectors/google/sheets"
	"github.com/bsamanvith/agentkit/connectors/hubspot"
	"github.com/bsamanvith/agentkit/connectors/parallel"
	"github.com/bsamanvith/agentkit/connectors/salesforce"
	"github.com/bsamanvith/agentkit/connectors/tavily"
	"github.com/bsamanvith/agentkit/pkg/coretools"
	"github.com/bsamanvith/agentkit/pkg/knowledge"
	"github.com/bsamanvith/agentkit/pkg/memory/embed"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// newCatalog lists the connectors compiled into this binary. A connector
// behind a build tag will add its factory here from a small file of its own,
// so config naming a connector that was not compiled in fails at startup
// (toolset.ErrUnknownType lists the types that were). Connectors log to
// logger (serve's), their auth mode at startup among other things. A
// knowledge base's relative root is relative to configDir, the config
// file's folder, so a config ships with its documents next to it.
func newCatalog(logger *slog.Logger, configDir string) (*toolset.Catalog, error) {
	return toolset.NewCatalog(append([]toolset.Factory{
		toolset.Demo(),
		coretools.Math(),
		coretools.Files(),
		coretools.User(),
		knowledge.Factory(knowledge.WithBaseDir(configDir), knowledge.WithLookupEnv(os.LookupEnv),
			knowledge.WithEmbedOptions(embed.WithLogger(logger))),
		tavily.Factory(tavily.WithLookupEnv(os.LookupEnv), tavily.WithLogger(logger)),
		exa.Factory(exa.WithLookupEnv(os.LookupEnv)),
		gmail.Factory(gmail.WithLookupEnv(os.LookupEnv), gmail.WithLogger(logger)),
		parallel.Factory(parallel.WithLookupEnv(os.LookupEnv), parallel.WithLogger(logger)),
		drive.Factory(drive.WithLookupEnv(os.LookupEnv), drive.WithLogger(logger)),
		calendar.Factory(calendar.WithLookupEnv(os.LookupEnv), calendar.WithLogger(logger)),
		sheets.Factory(sheets.WithLookupEnv(os.LookupEnv), sheets.WithLogger(logger)),
		hubspot.Factory(hubspot.WithLookupEnv(os.LookupEnv), hubspot.WithLogger(logger)),
		salesforce.Factory(salesforce.WithLookupEnv(os.LookupEnv), salesforce.WithLogger(logger)),
	}, append(mcpClientFactories(logger), a2aClientFactories(logger)...)...)...)
}
