// Package openaicompat is the llm.Model for any server that speaks OpenAI's
// Chat Completions API: OpenAI itself, xAI (Grok), Groq, vLLM, Ollama,
// llama.cpp, and gateways such as the LiteLLM proxy or OpenRouter. It is built
// on the official OpenAI Go SDK (github.com/openai/openai-go/v3); the SDK
// sends the requests, retries them and decodes the event stream.
//
// New needs a model ID ([WithModel]); the server is OpenAI's unless
// [WithBaseURL] (or OPENAI_BASE_URL) names another, for example
// http://localhost:11434/v1 for Ollama. The API key comes from OPENAI_API_KEY
// or WithClientOptions(option.WithAPIKey(...)); without one no Authorization
// header is sent, which local servers accept. The SDK sends a key over plain
// HTTP only to a loopback address, and only with option.WithUnsafeAllowHTTP;
// otherwise the request fails with [llm.ErrInvalidRequest].
//
// # Mapping
//
// The system prompt is a "system" message. A user message's tool results
// become "tool" messages, sent before the rest of the message (the API wants
// them right after the assistant's tool calls); a failed result's content is
// prefixed with "Error: ", since the API has no error flag. An assistant
// message's text blocks become its content and its tool uses its tool_calls.
// Tool definitions are "function" tools with the input schema as parameters.
// The tool choice is left to the server (auto). Every request streams, with
// stream_options.include_usage so the last chunk carries the token counts.
//
// Reasoning that a server streams in a reasoning_content field (DeepSeek,
// vLLM, llama.cpp) or a reasoning field (Ollama, OpenRouter, Groq) comes back
// as an [llm.Opaque] block of provider "openaicompat" whose data is that field
// as a JSON object, {"reasoning_content":"..."}. It is sent back in the same
// field of the assistant message, unless [Options.DropReasoning] is set.
// Opaque blocks of other providers are left out of requests.
//
// # Streaming
//
// Text deltas are passed on as they arrive. Tool calls are held back until the
// reply ends and then streamed one after the other, each as its argument
// fragments in order: the API may interleave the fragments of parallel calls,
// and only the end of the reply says whether a call was cut off by the token
// limit. A call cut off that way has invalid arguments and is dropped; the
// reply reports [llm.StopMaxTokens]. A finished call whose arguments are not
// valid JSON fails the stream with [llm.ErrOverloaded] (the model garbled it;
// a retry samples again).
//
// # Stop reasons and usage
//
// finish_reason "stop" is [llm.StopEndTurn] ([llm.StopToolUse] when the reply
// has tool calls, as some servers say "stop" then), "tool_calls" and
// "function_call" are [llm.StopToolUse], "length" is [llm.StopMaxTokens], and
// "content_filter", or a reply with a refusal, is [llm.StopRefusal]: its
// Explanation is the refusal text and its Category the filtered categories of
// an Azure-style content_filter_results (comma-separated). Anything else is
// [llm.StopOther] with the value in Raw. Usage: prompt_tokens minus
// prompt_tokens_details.cached_tokens is InputTokens, the cached tokens are
// CacheReadTokens, completion_tokens is OutputTokens.
//
// # Errors
//
// 429 maps to [llm.ErrRateLimited], except insufficient_quota, which is
// [llm.ErrAuth] (it needs billing, not a wait); 401 and 403 to [llm.ErrAuth];
// 413, and a 400 whose code is context_length_exceeded or whose message says
// the context length is exceeded, to [llm.ErrContextTooLong]; other 4xx to
// [llm.ErrInvalidRequest]; 408, 409, 5xx, a dropped connection, a garbled
// stream or one that ends without a finish_reason to [llm.ErrOverloaded]. An
// error sent inside the stream is classified by its code and type the same
// way. The SDK's own retries (two by default) run first; its *openai.Error
// stays reachable with errors.As.
package openaicompat
