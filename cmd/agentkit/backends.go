package main

import "fmt"

// The store and bus backends serve can open. Each is in its own file, which a
// build tag leaves out, so an image links only the backends it runs (see the
// Makefile's image variants):
//
//	nosqlite    no SQLite store (backend_sqlite.go)
//	nopostgres  no Postgres store, and no agentkit migrate (backend_postgres.go)
//	noredis     no Redis bus (backend_redis.go)
//
// A backend left out answers with errNotBuilt, so a config that names it
// fails at startup and says which image has it.

// notBuilt is the error of a backend this build leaves out.
func notBuilt(env, backend, tag string) error { //nolint:unused // only the builds that leave a backend out call it
	return fmt.Errorf("%s: this agentkit is built without %s (-tags %s); use an image that has it", env, backend, tag)
}
