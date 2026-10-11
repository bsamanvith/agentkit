// Package anthropic is the llm.Model for Claude, built directly on the
// official Go SDK (github.com/anthropics/anthropic-sdk-go) with no translation
// layer in between, so Claude-specific features keep working.
//
// New returns a model that streams every request through the Messages API
// (beta endpoint, for the server-side refusal fallback). Credentials come from
// the SDK's usual chain: ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN, an `ant auth
// login` profile, or workload identity federation. ANTHROPIC_BASE_URL is
// honored too; WithBaseURL overrides it.
//
// # Mapping
//
// A request's system prompt, messages and tool definitions map one to one onto
// the Messages API. Failed tool results are sent with is_error. Thinking,
// redacted-thinking and any other Claude block that llm has no type for come
// back as [llm.Opaque] blocks of provider "anthropic" and are sent back in the
// next request byte for byte, spliced into the request body so that neither
// the SDK nor encoding/json re-encodes them. Opaque blocks of other providers
// are left out. History is never edited, with one exception the API asks for:
// thinking blocks that come before a server-side fallback switch in the same
// message are left out (see [Fallback]).
//
// The tool choice is always "auto": current Claude models reject forced tool
// use. Thinking is adaptive (it cannot be turned off on Claude Opus 5.5);
// [Effort] is the control.
//
// # Streaming
//
// Text deltas are passed on as they arrive. A tool call's input is held back
// until the call is complete and then streamed as its fragments, in order:
// a call cut off by the token limit has invalid input and is dropped, and the
// reply reports [llm.StopMaxTokens]. Opaque blocks have no deltas.
//
// # Options
//
// [Options], set as a request's Provider (as a value or a pointer) or as the
// model's defaults with [WithOptions], choose the effort, the thinking display,
// prompt-cache breakpoints, extra beta headers and the refusal fallback.
//
// # Errors
//
// API errors map to the llm sentinels: 429 to [llm.ErrRateLimited]; 529,
// 5xx, a dropped connection or a stream cut short to [llm.ErrOverloaded];
// 400 and 404 to [llm.ErrInvalidRequest], or [llm.ErrContextTooLong] when the
// prompt does not fit (and 413, a request body over the size limit); 401, 402
// and 403 to [llm.ErrAuth]. The SDK's own retries (two by default) run
// before an error is reported. The SDK's *anthropic.Error stays reachable
// with errors.As.
package anthropic
