---
id: T114
title: "Run API: a run picks its toolsets and the tool search mode"
epic: E22
status: done
depends_on: []
---

# T114: A run picks its toolsets and whether they are searched

## Context

The chat's tool picker and tool search ([E22](E22-connectors-and-tool-picker.md)) need the run API to narrow an agent's tools per run. The harness already defers tools per agent (`WithDeferLoading`, `tool_search`, native or emulated); `tool_policy` already shows how a run narrows the agent and keeps the narrowing on resume (request → runner option → `PromptRecord` → `harness.Input`, and across `runnerrpc`).

## Scope

- **Wire:** `RunRequest.Tools *ToolChoice` with `toolsets []string` (absent: all of the agent's; `[]`: none) and `search` (`""`: the agent's, `"direct"`, `"search"`). Validated by the API: a toolset the agent does not have, or another mode, is 400 `invalid_request`. A resume cannot set one.
- **Harness:** `Input.Tools *ToolChoice`; `WithToolsets(toolsetOf)` tells the harness each tool's toolset (`""` for none). A choice drops the tools of toolsets not chosen; `search` defers every remaining toolset tool behind `tool_search`, `direct` defers none (the agent's `defer_loading` included). Tools of no toolset are never dropped or deferred. A deferred tool loaded earlier in the transcript stays loaded.
- **Runner:** `runner.Tools(choice)` run option, kept in `PromptRecord` (and in compaction records) so a resume and the next runs of an interrupted run keep it; carried over `runnerrpc` to a remote runner (an old runner that cannot honour it refuses rather than ignoring it, as with `tool_policy`).
- **Agent setup:** every agent passes `WithToolsets`, and `WithNativeToolSearch` when its model searches natively, so a run can turn search on for an agent with no `defer_loading`.
- **Client:** `agentclient` and the TS client (`@agentkit/agent-ui/api`) send it.

## Out of scope

- Adding a toolset the agent does not have. Narrowing by single tool (the toolset's `include` and `tool_policy` do that).

## Acceptance criteria

- [x] A choice of toolsets sends the model only those toolsets' tools, plus the tools of no toolset — test: `harness.TestToolChoiceKeepsChosenToolsets`
- [x] `search` sends `tool_search` and defers the chosen toolsets' tools (native: sent as deferred; emulated: held back), and a search loads them — test: `harness.TestToolChoiceSearchDefers`
- [x] `direct` sends every chosen tool even when the agent defers them — test: `harness.TestToolChoiceDirectOverridesAgentDefer`
- [x] A call to a tool that was not chosen is refused like an unknown tool, never run — test: `harness.TestToolChoiceRefusesDroppedTool`
- [x] The API refuses an unknown toolset and an unknown mode with 400, and a resume that sets a choice — tests: `agentapi.TestRunToolChoiceValidation`, `TestRunToolChoice`
- [x] The choice is kept on resume after a crash — test: `runner.TestToolChoiceKeptOnResume` (after compaction it rides in `CompactionRecord.Tools`, read by the same code as `ToolPolicy`, whose `TestToolPolicyKeptAcrossCompaction` covers the path)
- [x] Over `runnerrpc` the choice reaches the remote runner, and an older runner refuses the run — tests: `runnerrpc.TestToolChoiceCrossesRPC`, `TestOldRunnerRefusesARunWithAToolChoice`
- [x] The smoke suite's chat run with `"tools": {"search": "search"}` calls `tool_search` then the tool — test: `test/e2e.TestToolSearch` (`make smoke`; the fake model answers "findtool")

## Notes

- The API checks names against the agent's toolsets (config and runtime ones); the harness drops by `toolsetOf`, which every agent already passes through `WithToolPolicy` (now kept when another option passes nil). `WithToolsets` is the same function for harnesses with no policy.
- Every Claude agent gets `WithNativeToolSearch` at setup now, not only one with `defer_loading`, so a run's Search mode is native for Claude. It has no effect while nothing is deferred.
- `runnerrpc` sends a run with a choice as `RunWithTools`, the newest method; a runner without it fails the run (not retried) rather than offer the toolsets it left out.
- Also added: `harness.TestToolChoiceInvalid`, `runner.TestToolChoiceInvalidMode`.
