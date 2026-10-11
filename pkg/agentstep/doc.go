// Package agentstep runs an agent (a [harness.Harness] run) as a [flow.Step],
// so agents can be put in sequences, run in parallel, and paused for approval
// like any other step.
//
// # State
//
// A Step reads its prompt, a string, from the state key [Keys.Input] and
// writes an [Output] (the answer and the whole transcript) under
// [Keys.Output]. The output key is always explicit: two agents in one
// [flow.Parallel] must write different keys, because the same key written by
// two branches is [flow.ErrConflict].
//
// # Saving and resuming
//
// flow checkpoints a step only when it completes, and an agent step can run for
// many turns. So a Step also saves its own transcript after every turn, through
// a [TranscriptSaver] keyed by the flow run id and the step's path (see
// [flow.StepPath]): first the prompt, then the messages each completed turn
// added. When flow runs the step again (after a crash, a failure or a
// cancellation), it continues from what was saved instead of starting over:
//
//   - nothing saved: a new run with the prompt from the state;
//   - the saved transcript ends with a user message (the prompt, or the results
//     of a turn's tool calls): the harness resumes it without a new message,
//     and sends the model exactly the requests an uninterrupted run would;
//   - it ends with an assistant message: the run had already answered (the
//     process stopped before flow saved its checkpoint), so the answer is
//     written to the state and the model is not called.
//
// Only turns that go on (the model called tools) or answer (it ended its turn)
// are saved. A final reply that ends the run with an error, such as one cut off
// by the output limit or a refusal, is not, so running the step again asks the
// model that question again.
//
// # Side effects
//
// A turn is saved only when it is complete: the model's reply and the results
// of all its tool calls. If the process stops while a turn's tools are running,
// that turn is not saved, and on resume the model is asked again and may call
// the same tools again (with new call IDs). Tools with side effects should
// therefore be idempotent, or guard themselves with a key that survives the
// retry (derived from the run, the step path and the input, not the tool-use ID
// alone).
package agentstep
