---
id: T065
title: "A2UI in the harness: ui_render tool, catalog validation, ui events, ui_action messages"
epic: E13
status: done
depends_on: [T062]
---

# T065: A2UI in the harness

## Context

Agents should be able to show UI (forms, choices, cards), not only text, streamed to the client. A2UI is the declarative protocol for it. Decisions: [E13](E13-agent-protocols.md).

## Scope

- `pkg/a2ui`: the message types of the current stable A2UI spec (read the spec at a2ui.org; record the version), the basic-catalog subset (E13), and `Validate(msg) error` (unknown component types and properties, broken ID references, cycles, size limits).
- The `ui_render` tool (opt-in per agent: `agents[].ui: "a2ui"`): input is an A2UI message; valid ones become a `ui` event on the run's stream (`{"v":1,"type":"ui","surface_id":...,"message":{...}}`), invalid ones a tool error describing the problem so the model can fix it.
- `ui_action`: `POST /v1/runs` accepts a message with a `ui_action` part (surface ID, action, values); it is appended to the conversation and shown to the model as structured text.
- The agent API's OpenAPI spec (T062) and `pkg/agentclient` (T053) gain the `ui` event and `ui_action`.

## References

- [A2UI specification](https://a2ui.org/) (current stable version, message reference, basic catalog, actions, data binding)
- [A2UI A2A extension](https://a2ui.org/) (how surfaces travel over A2A, for T067)
- [Introducing A2UI](https://developers.googleblog.com/introducing-a2ui-an-open-project-for-agent-driven-interfaces/) (design goals: no code, client catalog, streaming)

## Acceptance criteria

- [x] Golden examples from the spec validate; each invalid case (unknown component, unknown property, dangling child ID, cycle, oversize) fails with a specific error.
- [x] Fake model calls `ui_render`: the stream carries the `ui` event in order between turns; an invalid surface gives a tool error and the model's corrected retry succeeds.
- [x] A `ui_action` continues the conversation; the transcript stays valid and append-only.
- [x] An agent without `ui: "a2ui"` has no `ui_render` tool.
- [x] Live (`-tags live`): real Claude, asked to let the user pick between options, produces a valid surface on the first or second try. **Run 2026-10-24** with the project's Anthropic key (`make test-live`, all live tests pass): `cmd/agentkit` `TestLiveUIRender` passed (a valid surface first try).
- [x] `FuzzValidate` never panics; mutation list recorded.

## Notes

### Done (2026-10-22), status review

Every criterion but the live one is met and tested. The live test
(`cmd/agentkit/ui_live_test.go`, `TestLiveUIRender`, `-tags live`) is
written and vets, but was not run: the session had no `ANTHROPIC_API_KEY`.
Run `go test -tags live -run TestLiveUIRender ./cmd/agentkit` with a key, then
tick it and set `status: done`.

- `pkg/a2ui`: the A2UI v0.9 messages (spec release 0.9.1, wire `"version":
  "v0.9"`), the catalog, `Parse`, `ParseInput`, `ReadInput`, `Validate`, `ValidateBatch`,
  `SafeURL`, the `ui_render` tool (`Tool`), and `Action`, `ActionText` and
  `ParseActionText` for actions. `testdata/spec` holds the spec's basic
  catalog and its example messages, the goldens.
- `pkg/agentapi`: `ui` events (`ui.go`), `ui_action` on `POST /v1/runs`,
  `Agent.UI`, `wire.TypeUI`, `wire.UIAction`, `wire.Agent.UI`; the OpenAPI
  spec (`docs/api/openapi.yaml`) and its cassette probes.
- `pkg/agentclient`: `UIAction`, documented under "UI".
- `cmd/agentkit`: `agents[].ui: "a2ui"` in the config; `withUI` adds the
  tool to that agent's harness only (not in `nomodels` builds, which run
  no harness). `docs/QUICKSTART.md` shows it.

Tests, one per criterion: `TestSpecExamplesValidate` and
`TestInvalidMessages` (each invalid case checks its sentinel and its JSON
pointer), `TestUIEvents`, `TestUIAction` and `TestUIActionRefused`,
`TestServeUIAgent` (the plain agent's tools have no `ui_render`),
`TestLiveUIRender`, `FuzzValidate` (90 s, no failure) and the mutation list
below. Also `TestCatalogMatchesSpec` (drift against the spec's catalog),
`TestDeferredComponents`, `TestTool`, `TestActionText`,
`TestUIEventsOnlyForSuccess`, `TestReadInput`, `ExampleParseInput`.

### Decisions (defaults picked here)

- **Spec version**: A2UI v0.9 (release 0.9.1, the current stable one; v1.0
  is a candidate). Catalog ID: the basic catalog's,
  `https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json`.
- **Catalog**: the basic catalog minus AudioPlayer, Modal, Slider, Tabs and
  Video, deferred until the console draws them (T066); they are refused as
  "not in this catalog yet", under the basic catalog's ID. There is no Link
  component in v0.9: links are a Button whose action calls `openUrl`.
- **Tool input**: `{"messages": [...]}`, 1 to 16 A2UI messages in order;
  each becomes one `ui` event. A batch can create a surface, fill it and
  set its data in one call.
- **Validation is stateless per call**: a call that updates a surface's
  components sends the whole tree, root included, and every ID it names is
  defined in that call. The server keeps no surface state, so a replica
  needs nothing to validate, and the model's error is about its own call.
  Incremental updates (patching one component) are left out.
- **Strictness**: unknown properties are errors;
  function return types must match their slot; theme keys are
  `primaryColor` (`#RRGGBB`), `iconUrl` and `agentDisplayName`;
  `sendDataModel` is accepted and ignored. Checks may be written as the
  spec's `{condition, message}` or as its example's shorthand
  `{call, args, message}`.
- **URLs**: a literal URL (Image `url`, theme `iconUrl`, `openUrl`'s `url`)
  must be absolute `http` or `https` with a host (`SafeURL`). A URL bound to
  the data model is only known when drawn: the renderer checks it.
- **Limits**: 64 KiB per message, 128 KiB per call, 200 components per
  surface, 100 children or options, depth 32, IDs of 128 bytes, strings of
  8 KiB, an action of 16 KiB; at most 10 problems are reported.
- **`ui` events** come right after the successful `ui_render` call's
  `tool_result`, before `turn_done`, one per message, and are stored
  (replayed by `GET /v1/runs/{id}/events`). Each carries `turn`, `call`,
  `tool_use_id`, `surface_id` and `message` (the message re-encoded after
  validation). Only agents with `ui: "a2ui"` emit them; a failed call emits
  none. The replay of runs from before events were stored has none.
- **`ui_action`**: `{surface_id, component_id, action, context}` in place of
  `message` (both is 400), on an existing conversation of an agent with UI
  (else 400). `context` rather than "values", A2UI's name. The server stamps
  the time and appends a user text message
  `<ui_action>\n{"version":"v0.9","action":{name, surfaceId,
  sourceComponentId, timestamp, context}}\n</ui_action>`
  (`a2ui.ActionText`; `ParseActionText` reads it back). It does not check
  that the surface was shown in the conversation.
- **Image size**: the tables are text read on first use rather than Go map
  literals (which cost 25 KB of init code in every image); the tool is left
  out of `nomodels` builds; the agent API does not validate a call's input
  again (`ReadInput`). See Size.

### Invariants (checked by `checkStream` in every agent API test)

- A `ui` event comes only right after the `tool_result` of its `ui_render`
  call, all of that call's messages in order, same surface, nothing between.
- An agent without UI streams no `ui` event.

### Mutation list (each caught by a failing test)

- Dangling references not checked: `TestInvalidMessages`, `TestTool`.
- Cycles not detected: `TestInvalidMessages` (stack overflow).
- Missing root accepted: `TestInvalidMessages/no_root`.
- Deferred components treated as unknown: `TestDeferredComponents`.
- Literal URLs not checked: `TestInvalidMessages/unsafe_*`.
- `ActionText` timestamp not in UTC: `TestActionText`.
- The tool's error not `tool.ErrInvalidInput`: `TestTool`.
- `ui` events for agents without UI: `TestUIEvents`.
- `ui` events not stored: `TestUIEvents`.
- A call's last message not sent: `TestUIEvents`, `TestUIAction`.
- `ui` events before the `tool_result`: `TestUIEvents`, `TestUIAction`.
- `ui` events for a failed call: `TestUIEventsOnlyForSuccess`.
- `ui_action` taken by an agent without UI: `TestUIActionRefused`.
- An unknown `ui` mode accepted by `NewAgents`:
  `TestNewAgentsRejectsBadConfig`; by the config: `TestServeRejectsBadUI`.
- `withUI` not adding the tool: `TestServeUIAgent`.
- A surface deleted and created again in one call keeping the deleted
  one's components: `TestInvalidMessages/recreated_without_its_tree`.
- A child template's path not checked as a JSON pointer:
  `TestInvalidMessages/template_path_not_a_pointer`.
- `ActionText` not escaping `<`, so a name or context value closes the
  `<ui_action>` tag: `TestActionText`.

Equivalent, so not listed: dropping `TypeUI` from `kept`'s partial-turn
types (a `ui` event always follows a `tool_result`, which already marks the
turn partial).

### Size

On main at `d2b049f` (with `noagent`): +126,976 bytes on the stripped
binary (local +131,072, dev +126,976, runner +114,688), +36,864 on api and
+8,192 on toolsets, logged in docs/SIZES.md. No new dependency, no budget raised. The validator and the
tool are behind `!nomodels` (`cmd/agentkit/ui_tool.go`, `ui_none.go`), and
the agent API reads a successful call's input back with `a2ui.ReadInput`,
which skips the validator, so the images that run no harness do not link
it. Projected images: local about 28.38 MB of 29, dev about 29.89 MB of 30
(tight), toolsets about 16.42 MB of 18.

### For T066 (console renderer)

- A surface is rebuilt from the run's `ui` events in order (or, for an old
  conversation, from its `ui_render` tool_use inputs whose results are not
  errors, through `a2ui.ParseInput`). `updateComponents` replaces the whole
  tree; `updateDataModel` sets (or, without `value`, removes) `path`.
- Check a bound URL with the rules of `a2ui.SafeURL` before using it.
- Show a surface as "sent" once a later user message parses with the
  `ParseActionText` format naming it.
- `sendDataModel` is not supported: actions carry only their `context`.
- Deferred components (`a2ui.Deferred`) can be added to the catalog once
  drawn: remove them from `deferred` and the tests follow.

### For T067 (A2A) and T069

- A surface over A2A is the same messages; `ui_action` is the way back in.
  The text form of an action is stable (`ActionText`), so a remote agent's
  history reads the same.
