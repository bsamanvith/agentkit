---
id: T066
title: "Console: render A2UI surfaces and send actions back"
epic: E13
status: done
depends_on: [T055, T065]
---

# T066: A2UI renderer in the console

## Context

The console renders `ui` events (T065) inline in the chat with its own components, so agent-made UI looks like the rest of the console and can't run code. Design: [docs/UI.md](../docs/UI.md) and the mockup's "Narrow the news search" surface. Decisions: [E13](E13-agent-protocols.md).

## Scope

- A renderer in `web/packages/agent-ui` (shared with consumer apps) for the basic-catalog subset, data binding and updates to an existing surface (streamed, progressive), and surface deletion.
- Actions: buttons and submits send `ui_action` (T065) with the bound values; the surface shows "Sent" and is disabled until the agent replies.
- Safety: no HTML or style from the agent; image and link URLs through the allowlist; unknown components shown as a small "unsupported component" note, never as raw data.

## References

- [A2UI renderer development guide and component gallery](https://a2ui.org/)
- [CopilotKit: A2UI v0.9](https://www.copilotkit.ai/blog/a2ui-whats-new-in-google-generative-ui-spec) (catalog negotiation, rendering)
- T070's component inventory (A2UI components drawn with console components)

## Acceptance criteria

- [x] Browser test (chromedp, replayed model): a streamed surface renders progressively; a later update changes it in place; submit sends `ui_action` with the right values and the agent's reply follows.
- [x] XSS: component text containing markup renders as text; a `javascript:` link or a non-allowlisted image URL is dropped (test hook never fires).
- [x] Renders correctly in light and dark themes at phone width.
- [x] Mutation list recorded.

## Notes

### Done (2026-10-22)

Every criterion is met and tested here.

- `web/packages/agent-ui/src/a2ui/model.ts` (main entry): surfaces as data.
  `foldSurfaces` folds a thread's ui messages and actions in order
  (createSurface, updateComponents merging by ID, updateDataModel set or
  remove, deleteSurface, a surface created again starting afresh);
  `parseActionText` reads the server's `<ui_action>` text
  (`a2ui.ActionText`); `safeUrl` is `a2ui.SafeURL`'s rule; `SurfaceEdits`
  holds what the person typed, by conversation and surface revision.
- `a2ui/resolve.ts`: data bindings (absolute, or relative to a template
  item), every function of the catalog (formatString's `${path}`,
  formatNumber/Currency/Date, pluralize, the checks, and/or/not), checks
  and action contexts.
- `a2ui/surface.tsx`, its own entry `@agentkit/agent-ui/a2ui`, loaded
  lazily by the console: the renderer, every component of pkg/a2ui's
  catalog drawn with our components and tokens.
- `thread.ts`: `ui` events (and, in history, successful `ui_render` calls)
  become a surface part where the surface was created; a user message that
  is a `ui_action` becomes a user turn with `action`.
- Console: `RunStore.act` sends `ui_action` (`POST /v1/runs`), the thread
  draws surfaces (live, busy, sent) and hides `ui_render` steps, the action
  shows as "Sent “Search”". `RunRequest.message` is optional, with
  `ui_action`; `Agent.ui` and the `ui` event fields are typed.
- docs/UI.md's A2UI table says how each component is drawn and what is
  dropped; web/README.md names the entry.

Tests, one per criterion:

- Browser, progressive, in place, `ui_action`, reply:
  `e2e/ui.spec.ts` "a surface streams in, updates in place, sends its
  values and shows the agent's reply". The fake model (e2e/fake-model.ts,
  "news") shows a loading card, holds its second `ui_render` until the test
  releases it (GET /release), so the test sees the card while the run goes
  on, then the form in the same element. Submit sends the exact
  `ui_action` (checked on the request), the surface is "Sent" and
  disabled, the held reply changes the surface in place and its text
  carries the values; after a reload the history draws the same.
- XSS: `e2e/ui.spec.ts` "a surface's markup is text, and unsafe or outside
  URLs are dropped" ("unsafe"): markup in Text is text, a bound
  `javascript:` openUrl is a disabled button, an image bound to another
  origin is "Image not shown" and the fake model's `/hook.png` is never
  requested; live and from history; `window.__xss` stays unset, no CSP
  error. Unit: `surface.test.tsx` (literal, bound and computed URLs,
  allowImage, unsupported component, no Markdown).
- Themes at phone width: `e2e/ui.spec.ts` "a surface fits a phone in the
  light/dark theme": 360 px, no horizontal overflow, the surface inside the
  screen, contrast over 7:1 for the title, input and primary button label
  against their backgrounds, dark is dark, targets at least 24 px.
- Also: `model.test.ts` (fold, history, actions, safeUrl's table, pointers,
  edits, functions), `surface.test.tsx` (every component, templates, cycles,
  checks, states, action contexts), `runs.test.ts` (`act`), stories
  (`a2ui-surface.stories.tsx`).

### Decisions (defaults picked here)

- **Browser test with Playwright, not chromedp**: the console's browser
  tests are Playwright against the real binary and a fake model (E11,
  T078, `make web-smoke`, run in CI); a second Go browser harness would
  only duplicate it. The "replayed model" is e2e/fake-model.ts.
- **Images**: allowed only from the page's own origin by default
  (`allowImage`), because the console's CSP is `img-src 'self'`; an outside
  image would only be blocked (and logged as a CSP violation). Others are
  "Image not shown: description". Widening needs a configured list of
  image origins in both the CSP (pkg/web) and `allowImage`: an owner
  question below.
- **Links**: v0.9 has no Link; a Button whose action is `openUrl` is drawn
  as a link (new tab, `noopener noreferrer`) when its URL passes `safeUrl`,
  else disabled with "Link removed". Other local function calls do nothing.
- **Text** is plain text: the spec allows simple Markdown, but the agent's
  text stays text (a heading drops a leading `#`). The **theme** is
  ignored (no colour or icon from the agent).
- **Sent** lasts until the agent changes the surface (any message about it
  after the action), not only until the run ends: a reply in text alone
  leaves the surface sent, so a second press needs the agent to ask again.
  "Not used" (UI.md's fourth state) is not drawn: a surface stays usable
  after the person writes instead.
- **Busy**: while any run of the conversation goes on (or a message waits),
  every surface in it is disabled; also when the agent was removed or the
  browser is offline (no `onAction`).
- **Edits** live in `RunStore.edits` (memory, per conversation and surface
  revision), so they survive the thread being rebuilt from history after a
  run; a full page reload shows the agent's data model (the sent values
  are in the action's message, which the agent has).
- `ui_render` steps are hidden, failed ones too (the model retries them).
- ChoicePicker: chips for `displayStyle: chips`, else checkboxes or radios,
  a select for one of more than 6 options.
- The e2e agent has `"ui": "a2ui"`; the other tests don't notice the tool.

### Mutation list (each caught by a failing test)

Unit (`pnpm --dir web test`):
- `safeUrl` takes `https:host` or user info: `model.test.ts` safeUrl table.
- Images not checked by `allowImage`: `surface.test.tsx` (drops unsafe and
  outside URLs; asks allowImage); also `e2e/ui.spec.ts` unsafe.
- `openUrl`'s URL not checked: `surface.test.tsx`.
- The unsupported note showing the component's data: `surface.test.tsx`.
- An action's context not resolved: `surface.test.tsx`.
- updateComponents replacing instead of merging: `model.test.ts` fold.
- Sent kept after the agent changes the surface: `model.test.ts`.
- A failed `ui_render` drawn from history: `model.test.ts` history.
- Edits not tied to the revision: `model.test.ts` edits.
- A sent surface left enabled: `surface.test.tsx` states.
- A template's item path ignored: `surface.test.tsx` template.
- Cycles not cut: `surface.test.tsx` (stack overflow).
- Checks ignored on a button: `surface.test.tsx`.
- `ui_action` text not parsed in history: `model.test.ts`.
- `act` sending a message as well: `runs.test.ts`.
- Edits kept after sign-out: `runs.test.ts`.

Browser (`make web-smoke`):
- `ui_render` steps shown: `e2e/ui.spec.ts` first test.
- Body text with its own colour (an invisible label in a primary button,
  a real bug found by the screenshot): the theme tests' contrast check.
- Images not checked by `allowImage`: the unsafe test.
- Surfaces usable while the run goes on: the first test's `busy` check.

### Size

No dependency. The renderer is a lazy chunk (`surface-*.js`, 30.7 KB,
about 9 KB gzipped) loaded only for a thread with a surface. The
console's first load went from 218,235 to about 221,400 bytes (budget
240,000): the fold and action parsing in the main chunk, plus Rollup
splitting `button` and `jsx-runtime` into shared chunks. `pkg/web/dist`
grew by about 39 KB, so the image by about the same (under the 100 KB
that needs a docs/SIZES.md row).

### For later

- Owner: which image origins surfaces may show (a list in the server
  config, added to the CSP's `img-src` and passed as `allowImage`).
- T067/T069: the renderer takes any surface; another app passes its own
  `onAction` (`ui_action` on its run) and `allowImage`.
- Deferred components (Slider, Tabs, Modal, Video, AudioPlayer): draw them
  in `surface.tsx`, add them to `drawn`, and take them out of pkg/a2ui's
  `deferred`.
