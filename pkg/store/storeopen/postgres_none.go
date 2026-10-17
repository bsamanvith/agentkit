//go:build nopostgres

package storeopen

func postgresBackend() Backend {
	return notBuilt("Postgres", "nopostgres", "postgres://", "postgresql://")
}
