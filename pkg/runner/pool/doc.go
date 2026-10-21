// Package pool places the agent of each chat run in a pool: a named source
// of [runner.Agent]s with an [Isolation] level ([E20]).
//
// The runner keeps a run's persistence (its log, record, lease, recovery and
// retries) wherever its agent runs. A pool decides where the agent's turn
// loop runs: as the server built it ([NewProcess], today's runner: the
// harness in the server's process, or a runner component it reaches over
// package runnerrpc), in a sandbox, or in a VM of its own. A sandboxed or VM
// agent is a [runner.Agent] that streams harness events back from its
// environment, so the runner cannot tell it from the harness in process.
//
// A [Router] picks the pool of each run from its [Request]: its rules in
// order, then its default pool, taking the first that matches and whose
// isolation is at least what the agent needs. It never places a run in a
// weaker pool: with none strong enough the run does not start ([ErrNoPool]).
// [Router.Factory] makes it a [runner.AgentFactory]. With no configuration
// there is [Default]: one process pool, the runner as it always ran.
//
// # Lifecycle
//
// A pool owns the agents it makes: [Pool.Close] closes those still open and
// frees what the pool holds (containers, VMs, connections); [Router.Close]
// closes every pool. Close them once the runs that use their agents have
// stopped (the agent API closes its router at the end of its Shutdown).
// [Owned] keeps the [io.Closer]s a pool made, for a pool to close in its
// Close. A process pool makes nothing: the agents it returns are the
// server's.
//
// [E20]: https://github.com/bsamanvith/agentkit/blob/main/tasks/E20-runner-pools.md
package pool
