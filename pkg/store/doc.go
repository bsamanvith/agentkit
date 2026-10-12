// Package store defines how agentkit keeps conversations and runs: small
// interfaces that every backend (in memory, SQLite, Postgres, a key-value or
// document store) implements the same way. It holds no data itself; backends
// live in subpackages (packages memory, sqlite and postgres), and the
// conformance suites every backend passes live in package storetest.
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
// # Event logs
//
// [EventLog] keeps each run's durable events (a tool call, an interrupt, a
// finished turn, the run's end) for clients that watch a run and reconnect:
// they ask for the events after the last one they saw. Events are numbered
// 1, 2, 3... per run, and the writer (the runner holding the run's lease)
// chooses each [Event.Seq], as with conversation entries: an identical retry is
// a no-op, a different event at a taken position is [ErrConflict], and a
// position past the next free one is [ErrGap]. So the log never has a hole, a
// retry after a crash never stores an event twice, and a writer whose lease was
// taken over finds out at its next append. The store does not know event
// types: which events are durable is the runner's choice.
//
// # Times
//
// Backends keep times as int64 Unix nanoseconds, which hold 1678 to 2262. A
// time the caller asks a store to keep outside that range (a
// [Details.NextAttemptAt], a lease's expiry) is [ErrInvalid] in every backend,
// and changes nothing. A time a read only compares with (the now of
// [RunStore.ListResumable], a [Page.FromTime]) may be anything.
//
// # Text
//
// What a store keeps as text (conversation and run ids, lease owners, a run's
// error and pending paths, event types, and the JSON of records and event
// data) must be UTF-8 with no NUL byte, which is what a Postgres text column
// holds. Anything else is [ErrInvalid] in every backend, and a run looked up
// by such an id is [ErrNotFound]: it could never have been created.
//
// # Tenants
//
// Every method is scoped by the tenant in its context ([tenancy.Require]). A
// context with no tenant fails with an error matching [tenancy.ErrNoTenant],
// and one tenant never sees, changes or conflicts with another's data: the
// same conversation or run id in two tenants names two unrelated things.
//
// The one exception is [Admin]: [Admin.ListResumableAll] lists the resumable
// runs of every tenant, each with its tenant. It is the only method that
// ignores the tenant in its context, and it is only for the runner's recovery
// loop, which starts with no tenant. A backend hands it out as a separate
// value, from a function rather than a method, so a tenant-scoped store cannot
// be turned into it by a type assertion.
//
// The store never imports the harness: package runner turns harness events
// into entries and a stored context back into a transcript.
package store
