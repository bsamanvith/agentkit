package main

const (
	// envStore names the store of agentkit serve's agent API: "memory" (the
	// default, lost on restart), "sqlite:" and a file path, or a
	// postgres:// URL. agentkit migrate reads it too.
	envStore = "AGENTKIT_STORE"
	// envStoreMigrate=off starts serve without migrating the Postgres store:
	// agentkit migrate does it, as a job.
	envStoreMigrate = "AGENTKIT_STORE_MIGRATE"
	// envStoreAdmin is the Postgres URL the recovery loop reads every
	// tenant's runs on, as a role that bypasses row-level security; unset,
	// it uses $AGENTKIT_STORE, whose role must then bypass it.
	envStoreAdmin = "AGENTKIT_STORE_ADMIN"
)

// envBus names the bus that joins replicas of agentkit serve (E18): empty or
// "memory" for one replica, or a redis:// or rediss:// URL. Every replica
// sharing a store must share it.
const envBus = "AGENTKIT_BUS"
