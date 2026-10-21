// Package harness runs the agent loop: ask the model, run the tools it asks
// for, give it the results, and repeat until it has answered, a budget runs
// out, or the context is cancelled. It depends only on [llm.Model] and
// [tool.Registry], so it runs the same against a fake, a real provider, local
// tools or remote ones.
//
// # Running
//
// [New] builds a [Harness]; [Harness.Run] streams a run as [Event]s, and
// [Collect] reads a run to its end for callers that only want the [Result]:
//
//	h, _ := harness.New(model, tools, harness.WithSystem("Be brief."))
//	res, err := harness.Collect(h.Run(ctx, harness.Input{
//		Message: harness.UserText("What is 2+2?"),
//	}))
//
// The tool calls of one model turn run in parallel, at most
// [WithMaxParallelTools] at a time. Their results go back to the model in one
// message, in the order of the calls, whichever finishes first. A tool that
// fails, is not found, gets bad input or is refused by a [WithBeforeTool] hook
// becomes an error result the model can react to; the run goes on.
//
// # Memory: the transcript and the record
//
// A run's memory is its [Transcript]: the messages the model sees, exactly as
// the model produced them (including opaque blocks), never edited, only
// appended to. It marshals to JSON, so it can be saved and passed back in
// [Input.Transcript] to continue: with a new message for a follow-up question,
// or without one to resume a run that stopped. A resumed run sends the model
// exactly the requests an uninterrupted run would have.
//
// Around the transcript, a run also produces a [Record]: when each message
// was written, how long the model and each tool took, token usage and stop
// reasons. It is kept apart because it is for people and bills (a UI, a
// store, traces), not for the model. [EventTurnDone] carries the new messages
// and the [TurnRecord] of each finished turn, so a consumer can persist a run
// turn by turn without holding the whole transcript.
//
// The harness itself stores nothing and reads no clock but the one given by
// [WithClock].
//
// # Turns, budgets and stopping
//
// A turn is one model call and the tools it asked for. Turns are atomic: the
// transcript only grows by whole turns, so it always ends where a run can be
// resumed. When a run ends early (an error, a cancelled context, a consumer
// that stops iterating) the transcript holds its completed turns.
//
// Budgets are checked before each model call, per run: [WithMaxTurns] counts
// model calls and [WithMaxTotalTokens] counts InputTokens, CacheReadTokens,
// CacheWriteTokens and OutputTokens of every turn. The tools of the last
// permitted turn still run, so a token budget can be overshot by one turn;
// in return [ErrBudgetExceeded] leaves a transcript that continues with a
// bigger budget.
//
// # Waiting for people
//
// A run's [Interaction] ([WithInteraction], narrowed by
// [Input.Interaction]) says whether its tool calls may wait for people. In
// an interactive or supervised run, a call whose tool asks ([tool.Interrupt],
// such as ask_user) or that the tool policy wants approved does not finish:
// it is an [Interrupt] ([EventInterrupt]). Once the turn's other calls are
// done, the run ends with [EventSuspended], whose result holds the
// [Pending] turn: the reply, the results so far and the interrupts. The
// transcript does not hold the pending turn. A later run takes the same
// transcript, the pending turn and an [Answer] to every interrupt
// ([Input.Pending], [Input.Answers]). It finishes the turn as turn 0 first: a
// question's answer is its result; an approved call runs with exactly the
// input it was approved with; a denied call's result is the denial. Then the
// run goes on as usual. An autonomous run, the default, never waits: tools
// that interrupt ([tool.Metadata.Interrupts]) are not offered, a tool that
// asks gets an error result, and a call the policy wants approved is refused
// ([PolicyError]).
//
// # Errors
//
// Every error from a run is a [*RunError] wrapping its cause, and carrying
// the partial transcript, usage and record. Match causes with [errors.Is]:
// [ErrBudgetExceeded], [ErrMaxTokens], [ErrRefused] (see [RefusalError]),
// [ErrUnexpectedStop], [ErrInvalidInput], the context's own error, and model
// errors, which are returned as they are and never retried here, so
// [llm.IsRetryable] tells a caller whether to try again.
package harness
