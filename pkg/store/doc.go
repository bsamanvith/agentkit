// Package store defines how agentkit keeps conversations and runs: small
// interfaces that every backend (in memory, SQLite, Postgres, a key-value or
// document store) implements the same way. It holds no data itself; backends
// live in subpackages (package memory, and SQLite next), and the conformance
// suites every backend passes live in package storetest.
//
// # Conversations
//
// A conversation is an append-only log of [Entry] values, numbered 1, 2, 3...
// The writer chooses each entry's [Entry.Seq], and [ConversationStore.Append]
// stores it only if that position is free: an identical retry is a no-op, a
// different entry at a taken position is [ErrConflict], and a position past the
// next free one is [ErrGap]. Concurrent writers therefore conflict instead of
// interleaving, and a retry after a timeout is always safe. Nothing in the log
// is ever edited or deleted.
//
// Messages are kept as the JSON [llm.Message] marshals to, byte for byte,
// so Opaque blocks (thinking signatures) go back to the provider unchanged.
// The Record beside them (times, usage, timings) is opaque to the store and is
// also kept byte for byte.
//
// There are two reads. [ConversationStore.History] is for people: every entry,
// in pages with opaque cursors, in either order, starting at a sequence number
// or a time. [ConversationStore.Context] is for the agent: the latest
// compaction entry and every entry after it, which is what goes back into a
// request. Compacting appends an entry; it never edits the log.
//
// # Runs
//
// [RunStore] keeps one record per run and a lease per run, so that one process
// at a time runs or resumes it: [Leaser.Claim] fails with [ErrLeased] while
// another owner holds an unexpired lease. Lease times come from the backend's
// injected clock.
//
// A run moves through its statuses with [RunStore.Transition], which names the
// status it moves from: of two processes moving the same run, one wins and the
// other gets [ErrConflict]. Completed, rejected and canceled are final
// ([ErrFinal]). A conversation has at most one unfinished run. What should
// continue now (a running run whose lease expired, a retryable failure that is
// due) is [RunStore.ListResumable]; what waits for an approval is
// [RunStore.ListSuspended]. The policy (when to retry, how to recover) belongs
// to the runner.
//
// # Checkpoints
//
// Backends store flow checkpoints by implementing [flow.Checkpointer]
// directly; this package does not redefine it.
//
// # Tenants
//
// Every method is scoped by the tenant in its context ([tenancy.Require]). A
// context with no tenant fails with an error matching [tenancy.ErrNoTenant],
// and one tenant never sees, changes or conflicts with another's data: the
// same conversation or run id in two tenants names two unrelated things.
//
// The store never imports the harness: package runner turns harness events
// into entries and a stored context back into a transcript.
package store
