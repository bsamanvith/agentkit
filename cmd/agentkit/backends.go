package main

import "fmt"

// The store and bus backends serve can open, and the heavier components.
// Each is in its own file, which a build tag leaves out, so an image links
// only what it runs (see the Makefile's image variants). The store and bus
// backends are in packages storeopen and busopen:
//
//	nosqlite      no SQLite store (pkg/store/storeopen/sqlite.go)
//	nopostgres    no Postgres store, and no agentkit migrate (pkg/store/storeopen/postgres.go)
//	noredis       no Redis bus (pkg/bus/busopen/redis.go)
//	nomodels      no model adapter (models_anthropic.go): agents run on a runner
//	noconnectors  no connectors but the demo (catalog.go): toolsets come from
//	              the toolsets component
//	nomcp         no MCP server (mcpserver.go)
//	noweb         no console (web.go)
//
// A part left out answers with notBuilt, so a config that names it fails at
// startup and says which image has it.

// notBuilt is the error of a backend this build leaves out.
func notBuilt(env, backend, tag string) error { //nolint:unused // only the builds that leave a backend out call it
	return fmt.Errorf("%s: this agentkit is built without %s (-tags %s); use an image that has it", env, backend, tag)
}
