// Package gemini is the llm.Model for Google's Gemini API. It is a small
// net/http client of the one endpoint it needs, POST
// {base URL}/models/{model}:streamGenerateContent?alt=sse, and reads the
// reply as server-sent events.
//
// The model ID defaults to [DefaultModel] ([WithModel] names another, such
// as "gemini-3.1-pro-preview"). The API key comes from GEMINI_API_KEY, else
// GOOGLE_API_KEY, or [WithAPIKey], and is sent as the x-goog-api-key header.
// The server is Google's unless [WithBaseURL] names another, for a gateway
// or a test.
//
// # Mapping
//
// The system prompt is the systemInstruction. Messages are contents of role
// "user" or "model". A tool result is a functionResponse part, named after
// the call it answers (found in the history by its ID), whose response is
// {"output": content}, or {"error": content} for a failed one. A tool use
// is a functionCall part with its ID. Tool definitions are function
// declarations whose parametersJsonSchema is the input schema, as written.
// Every request streams.
//
// # Thought signatures and opaque blocks
//
// Gemini signs its reasoning: a part may carry a thoughtSignature, which
// must come back with that part in the next request (the API refuses a
// function call without one). The adapter keeps everything of this kind in
// [llm.Opaque] blocks of provider "gemini", whose data is a Gemini part, a
// JSON object:
//
//   - {"thoughtSignature":"..."} alone is the signature of the block just
//     before it, a text or a tool use; it is put back on that part;
//   - a thought summary ({"text":...,"thought":true}, with [Options.IncludeThoughts])
//     or any other part the adapter does not map (an empty text part
//     with a signature, code execution, ...) is sent back as it is.
//
// Opaque blocks of other providers are left out of requests.
//
// # Streaming
//
// Text deltas are passed on as they arrive; a text part with a signature
// ends its block. The API sends each function call whole, so a tool use is
// streamed as its start, its input in one delta, and its end. A call without
// an ID gets one ("call_" and random hex). An empty text part is dropped.
//
// # Stop reasons and usage
//
// finishReason STOP is [llm.StopEndTurn] ([llm.StopToolUse] when the reply
// has function calls), MAX_TOKENS is [llm.StopMaxTokens], and SAFETY,
// RECITATION, LANGUAGE, BLOCKLIST, PROHIBITED_CONTENT, SPII and
// IMAGE_SAFETY, or a prompt the API blocked (promptFeedback.blockReason),
// are [llm.StopRefusal]: its Category is the categories of the blocked
// safety ratings (comma-separated), or else the reason, and its Explanation
// the finishMessage. MALFORMED_FUNCTION_CALL and UNEXPECTED_TOOL_CALL fail
// the stream with [llm.ErrOverloaded] (the model garbled a call; a retry
// samples again). Anything else is [llm.StopOther] with the value in Raw.
// Usage: promptTokenCount minus cachedContentTokenCount, plus
// toolUsePromptTokenCount, is InputTokens; the cached tokens are
// CacheReadTokens; candidatesTokenCount plus thoughtsTokenCount is
// OutputTokens. Gemini reports no cache writes.
//
// # Errors
//
// Errors are Google's {"error":{"code","message","status","details"}}.
// RESOURCE_EXHAUSTED and 429 map to [llm.ErrRateLimited]; 401, 403,
// UNAUTHENTICATED, PERMISSION_DENIED and an API_KEY_INVALID reason to
// [llm.ErrAuth]; 413, and a 400 whose message says the input token count
// exceeds the maximum, to [llm.ErrContextTooLong]; other 4xx to
// [llm.ErrInvalidRequest]; 408, 409, 5xx, a dropped connection, a garbled
// stream or one that ends without a finishReason to [llm.ErrOverloaded]. An
// error sent inside the stream is classified by its code and status the
// same way. Connection failures and 408, 409, 429 and 5xx responses are
// retried first, twice by default ([WithMaxRetries]), after the error's
// RetryInfo delay or Retry-After, if any, up to two minutes. The server's
// error is an [*Error], reachable with errors.As.
package gemini
