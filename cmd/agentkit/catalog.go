package main

import "github.com/bsamanvith/agentkit/pkg/toolset"

// newCatalog lists the connectors compiled into this binary. A connector
// behind a build tag will add its factory here from a small file of its own,
// so config naming a connector that was not compiled in fails at startup
// (toolset.ErrUnknownType lists the types that were).
func newCatalog() (*toolset.Catalog, error) {
	return toolset.NewCatalog(toolset.Demo())
}
