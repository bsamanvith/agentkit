// Package llm defines agentkit's provider-neutral model interface. The
// harness talks to every model through [Model] and never sees provider types;
// adapters (pkg/llm/anthropic, pkg/llm/openaicompat) translate to and from a
// provider's wire format.
//
// # Messages
//
// A [Message] has a [Role] and a list of [Block]s: [Text], [ToolUse] (the
// model asks for a tool), [ToolResult] (the answer, sent back in a user
// message) and [Opaque] (a provider block we do not interpret, such as
// Claude's thinking blocks). Messages marshal to JSON with a "type"
// discriminator on every block; unknown types are an error, never dropped.
//
// A conversation is append-only. Neither a [Model] nor anything else may edit a
// [Request] it receives: adapters must return [Opaque] blocks to their own
// provider unchanged, so history is never rewritten.
//
// # Streaming
//
// [Model.Generate] returns an [iter.Seq2] of [Event] values. Every stream ends
// with exactly one of: an [EventMessageDone] event carrying the finished
// [Response], or an error. [Collect] runs a stream to the end for callers that
// do not stream.
//
// # Errors
//
// Adapters map provider failures to the sentinels [ErrRateLimited],
// [ErrOverloaded], [ErrInvalidRequest], [ErrAuth] and [ErrContextTooLong],
// wrapped so that both errors.Is and the provider's own error (errors.As)
// work. [IsRetryable] tells a retry loop which failures are worth retrying.
// A cancelled or expired context is reported as the context's error.
//
// Conformance tests for any [Model] implementation, and a scripted fake for
// tests of code that uses one, live in package llmtest.
package llm
