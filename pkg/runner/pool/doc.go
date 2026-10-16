// Package pool places the agent of each chat run in a pool: a named source
// of [runner.Agent]s with an [Isolation] level ([E20]).
//
// The runner keeps a run's persistence (its log, record, lease, recovery and
// retries) wherever its agent runs. A pool decides where the agent's turn
// loop runs: in the server's process ([Process], today's runner), in a
// sandbox, or in a VM of its own. A sandboxed or VM agent is a
// [runner.Agent] that streams harness events back from its environment, so
// the runner cannot tell it from the harness in process.
//
// A [Router] picks the pool of each run from its [Request]: its rules in
// order, then its default pool, taking the first that matches and whose
// isolation is at least what the agent needs. It never places a run in a
// weaker pool: with none strong enough the run does not start ([ErrNoPool]).
// [Router.Factory] makes it a [runner.AgentFactory].
//
// [E20]: https://github.com/bsamanvith/agentkit/blob/main/tasks/E20-runner-pools.md
package pool
