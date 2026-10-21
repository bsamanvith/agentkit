// Package fakellm is a scripted stand-in for the Anthropic Messages API: an
// [http.Handler] that answers POST /v1/messages, streaming (SSE) or not,
// from JSONL scripts, so that local runs and end-to-end tests are
// deterministic and need no key. cmd/fakellm serves it as its own process
// and image.
//
// A script is a file name.jsonl. Each non-blank line that does not start
// with # is one entry:
//
//	{"match": "multiply", "response": {"tool_calls": [{"name": "calculate", "input": {"op": "mul", "a": 6, "b": 7}}]}}
//	{"match": "\"result\":42", "response": {"text": "6 times 7 is 42."}}
//	{"match": "recorded", "fixture": "recorded/01.http"}
//	{"response": {"text": "I only know a few lines."}}
//
// An entry with a match answers a request when the text of its last user
// message contains the match: its text blocks and the content of its tool
// results. The first matching entry wins. The one entry without a match is
// the fallback, used when no other entry matches, wherever it is in the
// file; without one, a request that matches nothing gets a 400 that says so.
//
// A response has text, tool calls or both (text first) and an optional
// stop_reason (default tool_use with tool calls, else end_turn). A tool call
// without an id gets one derived from the request, so answers are the same
// on every run and differ between requests. Usage is a fixed fake count.
//
// A fixture is a recorded response in the format the replay tests use
// (cmd/agentkit/testdata/e2e/*/NN.http, as curl -i prints it: status line,
// headers, blank line, body) and is served as recorded, whatever the request
// asked for. [ServeFixture] serves one; the replay tests use it too, so
// recorded and scripted replies share one format.
//
// The script is the one named by the X-Fakellm-Script header, else the
// server's default. Every request is logged with the script, the entry that
// answered and the status, so a failing end-to-end test can be debugged
// from the log.
//
// What it does not do: count tokens, check the request against the real
// API's rules beyond what it needs to read, or speak any other provider's
// API.
package fakellm
