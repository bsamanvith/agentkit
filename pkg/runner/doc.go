// Package runner runs agents with persistence. It is the one place that knows
// both the harness and the store: it turns harness events into stored entries,
// and a stored context back into a harness transcript, so a run survives a
// crash, a deploy, a transient model failure and a wait for a human.
//
// # Runs
//
// [Runner.Run] starts a chat run: a new user message on a conversation, run by
// the [Agent] the [AgentFactory] gives for that conversation. [Runner.RunFlow]
// starts a flow run, whose step tree comes from the [FlowFactory]. Both create
// the run, claim its lease and return an [Execution] before anything runs, so
// a caller learns the run ID, or that the conversation already has an
// unfinished run ([ErrRunUnfinished]), at once. Iterating
// [Execution.Events] runs it and streams its events.
//
// A chat run writes its prompt entry first, before the prompt's event reaches
// the consumer. It then keeps its turns, one entry per harness
// [harness.EventTurnDone] at the next positions of the conversation's log,
// and writes them together: every [WithFlushEvery] turns (10 unless the run
// set [FlushEvery]), after a turn that called a tool not declared
// [tool.KindRead], when the agent stops, and when the execution is
// interrupted, [Runner.Shutdown] included, unless its lease was lost.
// [EventSaved] tells the consumer how far the log is stored. A crash loses
// the turns not stored yet: the resumed run redoes them, calling the model
// and the read-only tools again. A turn that may have changed the world is
// stored before the next model call, so, as with one write per turn, a crash
// loses at most the turn in flight of those. Another writer at the same
// position ([store.ErrConflict]) stops the run: the runner never overwrites,
// it reloads and reports.
//
// # Interactive and background runs
//
// A chat run started with [Interactive] has a user on the other end, and
// stops with that user ([T100]). It is stored as interactive
// ([store.Interactive]), so every resume of it is too. [Runner.Interrupt] is
// its stop button; a consumer that stops reading (a closed tab) or a dying
// process stops it the same way. Once nobody holds its lease it is
// interrupted ([store.Run.Interrupted]): it is never resumed in the
// background, and [Runner.Resume] ("continue") sends the model exactly the
// requests that were never answered. A new message on its conversation
// cancels it and starts a new run. A retryable model failure is retried
// inline, while the user waits ([WithInteractiveRetry]).
//
// Every other run (flows, and chat runs without Interactive, such as those
// of jobs and webhooks) is a background run: it is recovered and retried as
// below.
//
// # Resuming
//
// A stopped background run continues in one of four ways ([E05]):
//
//   - after a crash or deploy, a [Recovery] finds it with [store.Admin] and
//     resumes it under its tenant;
//   - after a transient model failure ([llm.IsRetryable]), the run is stored
//     as failed and retryable with a backoff from the [RetryPolicy], and the
//     recovery loop resumes it when it is due, up to the policy's attempts;
//   - after an approval, [Runner.Decide] records the decision and resumes it;
//   - and an operator can resume any failed run with [Runner.Resume].
//
// An interactive run continues only by [Runner.Resume], or is replaced by a
// new message.
//
// Completed, rejected and canceled runs are final and never resumed. Resuming
// a completed flow run returns its final state from its checkpoint, without
// running the flow again, so a changed step tree can never make it fail.
//
// A resumed chat run continues from its stored transcript: if it ends on a
// user message, the harness runs it without a new message and sends the model
// exactly the requests an uninterrupted run would have sent; if it ends on the
// model's reply, the run had already answered and completes without a model
// call.
//
// # Flows and changed step trees
//
// A flow run is resumed with the step tree its factory gives now. If its
// [flow.Fingerprint] differs from the checkpoint's, the run fails with an
// error matching [flow.ErrShapeChanged], which is never retried
// automatically; nothing is written, and resuming later with the original
// tree, or with [AllowShapeChange] on an operator's decision, continues it.
// Agent steps count in the fingerprint by name only: changing an agent's
// model or prompt does not change it. Put a version in the step name to have
// such a change detected.
//
// # Agent steps
//
// [Saver] keeps the transcripts of agent steps ([agentstep]) in the
// conversation store, one conversation per step of a run
// ([StepConversation]). They are the conversation log and are never deleted
// when a run ends. [AgentStep] wraps an agent step so that the flow state, and
// so the checkpoint, holds a [StepOutput] (the answer and a reference into the
// store) instead of the whole transcript.
//
// # Leases and cancellation
//
// Every execution holds its run's lease, renews it from the runner's clock and
// releases it on every exit path. An execution that loses its lease stops
// writing, and drops the turns it kept. [Runner.Shutdown] stops a runner's
// executions, and [Runner.Interrupt] one of them; they store their turns and
// release their leases, and their runs stay running. The lease is how a live
// run is told from an interrupted one: a process that dies leaves its runs
// leased until the lease expires, at most one lease time later.
// [Runner.Cancel] makes a run canceled, and if its conversation ends on a
// user message (the run stopped before the model answered) appends a marker
// reply, so the next run sends a valid conversation. Nothing in the log is
// edited. Interrupt and Cancel stop executions of their own runner only; a
// run another process holds gives [store.ErrLeased].
//
// [E05]: https://github.com/bsamanvith/agentkit/blob/main/tasks/E05-store-runner.md
// [T100]: https://github.com/bsamanvith/agentkit/blob/main/tasks/T100-interactive-chat-runs.md
package runner
