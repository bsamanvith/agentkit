//go:build nosqlite

package storeopen

func sqliteBackend() Backend { return notBuilt("SQLite", "nosqlite", "sqlite:") }
