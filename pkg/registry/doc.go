// Package registry keeps agent definitions as versioned data of a tenant
// (T075, E15): an admin saves a definition through the agent API, each save
// is a new immutable version, validated in full before it is stored, and
// one version per agent is published. New runs of an agent use its
// published version; a run records the version it started on and keeps it
// through every resume, so publishing (or rolling back, which is publishing
// an older version) never changes a run that has started. It implements
// agentapi.AgentRegistry on a store.ItemStore.
//
// The registry does not know what a definition holds: a [Builder], given by
// the program (cmd/agentkit builds the harness from the shape of an entry
// of the serve config's "agents"), turns one into an agentapi.Agent, and
// refuses an invalid one with a *agentapi.DefinitionError that names the
// fields. Saving builds the definition once to validate it; running builds
// it again when no harness of that version is cached. Versions are
// immutable, so a cached harness is never stale: publishing another
// version is a new cache key, and the harness of a version is built at most
// once per process while it stays in the cache.
//
// The agents of the config file are read-only ([WithReadOnly]): a save or a
// publish under one of their names is agentapi.ErrAgentReadOnly.
package registry
