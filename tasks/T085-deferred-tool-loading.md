---
id: T085
title: "Deferred tool loading and skills that load tools"
epic: E17
status: review
depends_on: [T082, T057]
---

# T085: deferred tool loading

## Context

Agents with many tools waste context and confuse models if every definition is sent every time. Some tools should load only when needed, by a search or by a skill that uses them. Decisions: [E17](E17-tool-safety-control.md) ("Deferred loading", "Skills load tools").

## Scope

- **Config:** `defer_loading: true` per tool or toolset in the agent config.
- **Harness:**
  - sends only non-deferred tools, plus a **`tool_search`** tool (query → matching deferred tools by name, description and tags);
  - found tools are loaded for the rest of the run;
  - loaded tools are recorded in the transcript record, so a resumed run has them again.
- **Native provider support:**
  - check the Claude API's tool search tool and `defer_loading` (`claude-api` skill);
  - if the Anthropic adapter can send deferred tools natively, do so behind the same config and record the decision;
  - other providers use the emulated `tool_search`.
- **Skills:**
  - `SKILL.md` `tools:` (names or globs) are loaded when `skill_load` runs (T057);
  - a skill can't add tools the agent doesn't have.
- **Policy:** loading never changes a decision (T083 still checks every call).
- **API and console:** the console shows "loaded N tools" as a step.

## References

- Claude API docs: tool search tool and deferred loading (find the current page with the `claude-api` skill)
- [Anthropic: Writing effective tools for agents](https://www.anthropic.com/engineering/writing-tools-for-agents) (context cost of many tools, namespacing)
- Claude Code skills' `allowed-tools` front matter (prior art for skills naming their tools)

## Acceptance criteria

- [x] **Search and load** (fake model): with 30 deferred tools, the first request carries only the non-deferred tools plus `tool_search`; after a search, the found tool is available for the rest of the run and after a resume. `TestDeferredSearchAndLoad` (emulated: first request, after the search, a follow-up run, a resume of the cut transcript), `TestDeferredNative`, `TestDeferredToolKeptOnResume` (runner: crash after the loading turn, resume from the store), `TestServeDeferLoading`/`TestServeDeferLoadingEmulated` (serve, config to wire record); invariant `checkTools` in every harness test and `TestSimulatedRuns`.
- [x] **Skills:** `skill_load` of a skill listing `tools:` makes them available; a listed tool the agent doesn't have is ignored with a logged warning. `TestLoadSkillLoadsItsTools`, `TestDeferredLoader`, `TestDeferredTranscriptLoadsOnlyDeferredTools`, `TestServeDeferLoading` (`nope_tool` ignored), `TestSkillToolWarnings`.
- [x] **Policy:** policy denials still apply to loaded tools. `TestDeferredPolicyStillApplies`.
- [ ] **Live** (`-tags live`): real Claude finds and uses a deferred tool. `TestLiveDeferredTool` (`cmd/agentkit/deferred_live_test.go`, native and emulated) and `TestLiveDeferredSkillLoad` (native `skill_load`) compile; not run: no `ANTHROPIC_API_KEY` in this session. Status stays `review` until it runs.
- [x] **Mutation list** recorded (Notes).

## Notes

Decisions:

- **The harness owns `tool_search`** in both modes, and a result names what it loaded in `llm.ToolResult.Loads` (JSON `loads`). Loaded tools are read back from the transcript, so resume and follow-up runs need no store change; `TurnRecord.Loaded` (harness, runner, wire `loaded`) is for people and drives the console's "Loaded N tools" step (`turn_done`).
- **Native is the default for Claude** (`model.tool_search: native`, or `emulated`; OpenAI is always emulated and `native` with it is a config error). Native sends every tool on every request with `defer_loading: true` on the deferred ones (`llm.Request.Deferred`), and the result of a search or loader carries `tool_reference` blocks (client-side custom tool search: no beta header, no server tool). The tools array never changes, so the prompt cache and preserved thinking stay valid; emulated appends loaded tools at the end, which breaks the cache once per load. The cache breakpoint goes on the last non-deferred tool (a deferred tool can't carry `cache_control`); the adapter refuses a request with every tool deferred or a deferred name not in Tools.
- **Unverified live:** a native `skill_load` result mixes its text with `tool_reference` blocks. Decided in review (below): kept as is.
- **Config:** `defer_loading: true` (every toolset tool), or a list of toolset names and tool names or globs. An entry that names nothing (and the agent has no runtime toolsets) is a config error; skill and memory tools are never deferred. A tool named `tool_search` with anything deferred is a config error.
- **Skills:** `skill_load` is a loader (`harness.WithToolLoader`, `skill.Catalog.ToolsOf`): its `tools:` are matched against the agent's deferred tools only, so a skill never adds a tool the agent lacks; a listed tool that is not a tool of the agent is logged at startup (`skillToolWarnings`). A listed tool that is not deferred is already there.
- **Calls to a deferred tool not loaded yet** get an error result telling the model to search first. The policy (T083) runs as before on every call.
- Search: name part 3, substring of the name 2, description or tag word 1; ties by name; default 5 results, at most 20; only tools not loaded yet.

Mutations (each killed): emulated loaded tools not sent; transcript not read on resume; not-loaded calls run; policy skipped for loaded tools; load accepts names the agent lacks; native Deferred not sent; native search sends content; nothing deferred; name words count no more than description words; loader ignored; record misses loaded; cache breakpoint on a deferred tool; references to non-deferred tools; all-deferred request accepted; `ToolsOf` empty; typo entries accepted; native not wired; skill loader not wired; `Loads` lost by JSON; wire drops `loaded`; runner record drops `loaded`; console drops the `turn_done` step (vitest).

Size: stripped `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 -trimpath -buildvcs=false` binaries, main vs branch: all-in-one +69,632, local +65,536, dev +65,536, api +57,344, runner +61,440, toolsets +57,344. Under 100 KB, so no SIZES row; no new dependency.

### Review fixes

- **Text with references (decision):** the tool search page (custom tool search) says to "return a standard `tool_result` with `tool_reference` blocks in the content array"; its example has references alone, but nothing says they must be alone, and the SDK's tool result content union takes text and references together. So `skill_load` keeps its text and its references in one result; no fallback to emulated. The Notes said the live test checked this, but `TestLiveDeferredTool` has no skill: `TestLiveDeferredSkillLoad` (native, a skill listing `shipments_*`) now does. Not run here (no credentials); if it gets a 400, move the text to a block of its own after the results.
- **OpenAI-compatible adapter took `Deferred` silently:** a library caller with `WithNativeToolSearch` on it got every tool sent and empty search results. It now refuses the request with `llm.ErrInvalidRequest` (`TestInvalidRequests/deferred_tools`); `llm.Request.Deferred` says so.
- **Plants that survived, now caught:** a search that returns tools loaded already (`TestDeferredSearchSkipsLoaded`), a loader that loads them again (same), no cap of 20 on `max_results` (same), an error result's `Loads` read from the transcript (`TestDeferredErrorResultLoadsNothing`), native mode running a deferred tool not loaded yet (`TestDeferredNativeNotLoadedIsRefused`), an approved loader call that loads nothing (`TestDeferredLoaderAfterApproval`), and `defer_loading: true` deferring the skill tools (`TestServeDeferLoadingAll`).
- **Checked, no change:** a key that cannot see one of an agent's toolsets cannot run the agent (`access.agent`), so `tool_search`, which searches only that run's tools, shows it nothing new; clients cannot send transcripts, so they cannot forge `loads`; the policy decides every call that runs, `tool_search` and loaded tools included; a call to a tool not loaded is refused before the policy, and runs nothing.

### For T086 and T087

- A tool that becomes available mid-run (a toolset that connects later) is just another deferred tool: give it a name the `defer_loading` matcher accepts and `tool_search` finds it; nothing else in the harness changes.
- Anything that loads tools should return `Loads` on its result rather than keeping state: that is what resume reads.
- The console shows loading only live (`turn_done`); history does not show it yet (the record is not in the history response).
