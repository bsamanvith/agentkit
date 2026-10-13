---
id: T078
title: "Web workspace: React + TypeScript + shadcn/ui, shared packages, console app shell, embedding and CI"
epic: E11
status: done
depends_on: []
---

# T078: web workspace

## Context

Owner decision (2026-10-12): the admin console and future consumer apps use **React + TypeScript** with **shadcn/ui and Radix**, sharing an **agent-ui** component set. This task sets up the workspace so T055 (console chat), T064 (admin pages) and T066 (A2UI renderer) can start building. Decisions: [E11](E11-clients.md) "Web stack", [docs/PRODUCT.md](../docs/PRODUCT.md).

## Scope

- `web/` pnpm workspace (Node LTS pinned in `.nvmrc` / `packageManager`): `packages/ui` (shadcn/ui components, the token set from docs/UI.md and T070 as CSS variables, light/dark/system theme), `packages/agent-ui` (empty components with stories: tool step, step group, source chip, message, composer; real behaviour comes in T055), `apps/console` (Vite app shell: routing, sidebar layout, theme switch, API client wrapper over `/v1` with `fetch` streaming per T053's notes).
- TypeScript strict; ESLint + Prettier; Vitest + Testing Library; one Playwright smoke test against `agentkit serve` (preinstalled Chromium at `/opt/pw-browsers`; never `playwright install`).
- `make web` builds `apps/console` into `pkg/web/dist/`; `pkg/web` (the `web` component) serves it with the CSP and caching rules from T055; `go:embed` of the committed build so `go build` needs no Node.
- `make check` stays Go-only and fast; a new `make web-check` (lint, typecheck, unit tests, build, stale-build check) runs in a **separate CI job** (like fuzz) so web changes don't slow Go feedback.
- Dockerfile unchanged in shape (embeds the committed build); image size reported.
- Dependency policy: pinned versions, lockfile committed, `pnpm audit` in `web-check`; a short `web/README.md` (how to run, how packages relate, how a consumer app is added).

## References

- [shadcn/ui](https://ui.shadcn.com/) (installation with Vite, theming with CSS variables, the neutral/zinc base colors) and [Radix Primitives](https://www.radix-ui.com/primitives)
- [Vite](https://vite.dev/guide/), [pnpm workspaces](https://pnpm.io/workspaces)
- [Vitest](https://vitest.dev/), [Testing Library](https://testing-library.com/), [Playwright](https://playwright.dev/) (use the preinstalled browsers via `PLAYWRIGHT_BROWSERS_PATH`)
- [Go `embed`](https://pkg.go.dev/embed)
- docs/UI.md (tokens, look) and T070's output when available

## Acceptance criteria

- [x] `pnpm install && make web` builds; `agentkit serve` with `web` serves the console shell at `/` with light, dark and system themes from the shared tokens. **`TestE2EServeWeb`** (`cmd/agentkit/web_e2e_test.go`: the real binary with `agent,web`), **`TestServe`** and **`TestEmbeddedBuild`** (`pkg/web`), smoke **"the theme switch pins light and dark, and system follows the OS"**; the tokens are generated from docs/UI.md and a `packages/ui` test fails when they drift.
- [x] `make web-check` passes (lint, typecheck, unit tests, build, stale-build check) and runs as its own CI job; changing a component without rebuilding fails the stale check. CI's **`web`** job (`.github/workflows/check.yml`, beside `check` and `fuzz`); **`web/scripts/check-build.sh`** exited 1 with Composer changed and not rebuilt (mutation table).
- [x] Playwright smoke: the shell loads, the theme switch works, `/v1/agents` (T062) or a stub is called with the token. `make web-smoke` (`apps/console/e2e/smoke.spec.ts`, in CI's `web` job): **"the shell loads, asks for the token, and sends it to /v1/agents"**, **"the theme switch pins light and dark, and system follows the OS"**, **"on a phone, the drawer holds the sections and closes on a pick"**, **"theme.js pins the theme before the app's script runs"**.
- [x] `go build ./...` from a fresh clone without Node succeeds; the image builds and its size is recorded. **Checked by hand**, not by a test: a fresh worktree with no `node` on `PATH`; `make image-size` 18,099,712 bytes (Results). CI's `check` and `image` jobs build without Node.
- [x] `agent-ui` builds as a separate package that a second app (a throwaway `apps/example`) can import (proves the sharing works); remove the example after. **Checked by hand** (Results: it typechecked and built, was removed, and the lockfile came back byte-identical); no test keeps it, by design.

## Notes

### Results

- **`make web`** builds the console into `pkg/web/dist` (committed; about 630 KB: a 482 KB script, a 64 KB stylesheet and four woff2 fonts). With `AGENTKIT_COMPONENTS=agent,web`, `agentkit serve` serves the shell at `/` with System, Light and Dark themes from docs/UI.md's tokens.
- **What goes over the wire**: `pkg/web` gzips text files once, at start, and sends them compressed to clients that accept it (with `Vary: Accept-Encoding` and an ETag per encoding). The first load is 213,042 bytes: 160,514 of gzipped `index.html`, `theme.js`, script (148 KB) and styles (12 KB), out of 539 KB raw, and 52,528 of the two Latin fonts, Geist and Geist Mono (counted whether or not the first screen uses mono; woff2, already compressed, cached for a year). Fonts load after first paint (`font-display: swap`), and Latin Extended loads only when it is needed (`unicode-range`). `web/scripts/check-size.sh` fails `make web-check` above 240 KB (owner's request, 2026-10-12: the console runs on low-end PCs and bad connections). It reads the script and style names from `index.html`, so a new entry file or a missing one can't slip past it, and it fails if it finds no Latin fonts to count.
- **`make web-check`** runs install (frozen lockfile), lint (0 warnings), format check, typecheck, unit tests (16 in `ui`, 98 in `agent-ui`, 42 in the console), `pnpm audit --audit-level high`, a rebuild compared with the committed build, and the size budget. It runs in CI's `web` job, next to `check` and `fuzz`, which are unchanged.
- **The stale check fails** when a component the console uses is changed without a rebuild (tried with Composer's placeholder: exit 1, naming the changed files). It compares builds, so a change to a component the console doesn't import (ToolStep, tried) changes nothing that is embedded and passes. That is intended.
- **Builds are reproducible.** A build in `node:24.14.0` on Linux was byte-identical to the macOS build.
- **`make web-smoke`** (Playwright, four tests) runs against the real binary (`agent,web`, with a token):
  - the shell loads, and `/agents` shows the first agent beside the list;
  - the token prompt appears after a 401, and `/v1/agents` is then called with `Authorization: Bearer`;
  - Dark and Light set `data-theme` and change the computed background, a reload keeps the choice, and System follows `prefers-color-scheme`;
  - `theme.js` pins the theme with the app bundle blocked;
  - at 375 px: no rail and no sideways scroll; Menu opens the drawer, Esc closes it and returns focus to Menu, picking Agents closes it, and Agents is a list page whose items have Back;
  - no console error or CSP violation appears.
- **`go build ./...` works without Node**, from a fresh worktree with no `node` on `PATH`.
- **The image** (`make image`, `make image-size`) is 18,099,712 bytes uncompressed, against a 20 MB budget. Without the console (HEAD `4af15e5`) it was 17,378,816 bytes, so the console adds 720,896 bytes. The Dockerfile is unchanged: `.dockerignore` already lets `pkg/` in.
- **Sharing works.** A throwaway `apps/example` imported `@agentkit/agent-ui` (Message, StepGroup and the `AgentClient`) and `@agentkit/ui` (ThemeProvider, Button, styles). It typechecked and built, and was then removed; the lockfile came back byte-identical.

### Decisions

- **Versions**:
  - Node 24 LTS (`.nvmrc` 24.14.0) and pnpm 12.6.0 (`packageManager`);
  - React 19.3, react-router 8.4, Vite 8.3, Tailwind 4.3;
  - TypeScript 6.0, because typescript-eslint does not support 6.1 or 7 yet;
  - ESLint 10 with the React Compiler rules of eslint-plugin-react-hooks 7;
  - Vitest 5 and Prettier 3.
  
  Every version is exact. `onlyBuiltDependencies: []` means no package runs install scripts.
- **Packages are sources, not built libraries.** Each package's `exports` point at `.ts` and `.tsx` files, and the app's Vite compiles them. There is no build step per package and no stale `dist` per package. Components are imported by path (`@agentkit/ui/components/button`), so the bundle holds only what is used.
- **shadcn**:
  - set up with the CLI (new-york style, neutral base colour); its components are kept as generated, in `packages/ui/src/components`;
  - its CSS variables are mapped onto our `--ak-` tokens in `styles/index.css`, per docs/UI.md's table; the CLI's own stylesheet (hsl colours, a `.dark` class) was replaced;
  - `tokens.css` is generated from docs/UI.md (`pnpm --filter @agentkit/ui tokens`), and a test fails when they drift;
  - shadcn 4.21 rewrites a workspace's utils import to the npm package `cn`, so `add-component` wraps the CLI and repairs the import.
- **Stories without Storybook.** Each agent component has a `*.stories.tsx` in CSF3 format. `components.test.tsx` renders every story through `import.meta.glob`, so stories are tested and Storybook can be added later without rewriting them. Storybook itself would add a heavy dev dependency for five placeholder components.
- **Strict CSP**: `default-src 'self'` with no inline script or style, as T055 specified. To keep to it:
  - fonts are self-hosted (Geist, Latin and Latin Extended, upright);
  - the theme is applied before first paint by `public/theme.js`, an external file;
  - Vite inlines nothing (`assetsInlineLimit: 0`, no modulepreload polyfill);
  - `TestEmbeddedBuild` checks the committed `index.html` for inline scripts, inline styles and handlers.
- **`pkg/web` serving rules**:
  - `index.html` is `no-store`;
  - `/assets/*` (hashed names) is cached for a year;
  - other files are revalidated with an ETag;
  - paths without an extension get `index.html`, so routes survive a reload;
  - a miss under `/assets/`, or a miss with an extension, is a 404, so a stale tab never runs HTML as a script;
  - `/v1` and `/mcp`, and paths under them, are never answered with the app, after cleaning the path (`//v1/agents` and `/x/../v1/agents` are 404s too);
  - methods other than GET and HEAD get 405.
  
  It is mounted at `/` for every method, because a method pattern (`GET /`) would conflict with other components' patterns in ServeMux. It imports nothing from the module (archtest rule `"pkg/web": {}`).
- **Token and theme storage**:
  - the API token is kept in `sessionStorage` (`agentkit.token`). The prompt opens only after a 401, and requests wait for the token, so there is no request loop. The app is `inert` behind the prompt.
  - the theme is kept in `localStorage` (`agentkit.theme`).
- **Playwright** is pinned to 1.63.0, whose browsers are installed here. CI uses the runner's Google Chrome (`channel: "chrome"`); elsewhere, Playwright's own resolution applies (`PLAYWRIGHT_BROWSERS_PATH`). Nothing runs `playwright install`.
- **The one change to an existing file in `cmd/agentkit`** is in `serve.go`, and it can't be avoided: the component switch and `knownComponents` live there, so it gets the import, `web.Name` in `knownComponents`, and `case web.Name: m = newWeb()`. The constructor is in the new `web.go`.

### Files

- `web/`:
  - the workspace: `package.json`, `pnpm-workspace.yaml`, `pnpm-lock.yaml`, `.nvmrc`, `tsconfig.base.json`, `eslint.config.js`, `.prettierrc.json`, `.prettierignore`, `.gitignore`, `README.md`;
  - `scripts/check-build.sh`.
- `web/packages/ui`:
  - `components/`, from shadcn: alert, badge, button, card, collapsible, dropdown-menu, empty, input, input-group, kbd, separator, sheet, sidebar, skeleton, spinner, textarea, tooltip;
  - `hooks/use-mobile.ts`, rewritten with `useSyncExternalStore`;
  - `styles/` (`index.css`, `tokens.css`, `fonts.css`);
  - `theme.tsx`, `status.tsx`, `lib/utils.ts`;
  - `scripts/tokens.mjs`, `scripts/shadcn-add.mjs`;
  - tests.
- `web/packages/agent-ui`:
  - `api/`: `sse.ts`, `types.ts`, `client.ts`, and `client.test.ts` with TestSSEFraming's cases ported;
  - `tool-step`, `step-group`, `source-chip`, `message`, `composer`, each with stories;
  - `components.test.tsx`.
- `web/apps/console`:
  - `src/`: `app.tsx`, `routes.tsx`, `shell.tsx`, `layout.ts`, `lists.ts`, `list-row.tsx`, `sections.ts`, `api.ts`, `api-provider.tsx`, `token-prompt.tsx`, `pages/` (`chat`, `admin`, `page`, `readiness`), `app.test.tsx`;
  - `public/theme.js`;
  - `e2e/smoke.spec.ts`, `e2e/agentkit.json`;
  - `playwright.config.ts`, `vite.config.ts`.
- `pkg/web`: `web.go`, `web_test.go`, and `dist/` (the committed build).
- `cmd/agentkit`: `web.go` and `web_e2e_test.go` (new), and `serve.go` (the wiring).
- Other changes:
  - `internal/archtest/rules.go`;
  - `Makefile` (`web`, `web-check`, `web-smoke`);
  - `.github/workflows/check.yml` (the `web` job).

### Mutation check

Each mutant was planted, the tests named below were run, and the source was restored. All of them were caught:

| Mutant | Caught by |
|---|---|
| No `Content-Security-Policy` header | TestSecurityHeaders |
| A miss under `/assets/` (or with an extension) answered with `index.html` | TestServe |
| `/v1/` and `/mcp/` answered with the app | TestNotTheApp, TestE2EServeWeb |
| The token stored in `localStorage` | app.test "the token > is asked for after a 401, once…" |
| Requests not waiting for a token (a request loop on 401) | the same test (`seen` has one request) |
| `applyThemePreference` not setting `data-theme` | theme.test |
| `theme.js` doing nothing | smoke "theme.js pins the theme before the app's script runs" |
| A modal (scroll-locking) menu, which injects a `<style>` | smoke (found for real: the theme test failed on the CSP violation, so the menu became `modal={false}`) |
| A used component changed without `make web` | `check-build.sh` (exit 1) |
| gzip never sent | TestCompression, TestE2EServeWeb |
| The bundle over its budget (tried as a 100 KB budget) | `check-size.sh` (exit 1) |
| The reserved check on the raw path (`//v1/agents` gets the app) | TestNotTheApp |
| A freshly entered, refused token shown as "rejected or has expired" | app.test "the token > that was just entered and refused says so…" |
| The app not `inert` behind the prompt | app.test "the token > keeps the app inert behind the prompt" |
| A failed load without its reason | app.test "a page > that failed to load says why…", "…can't reach the server…" |
| Reading `localStorage` outside the `try` (site data blocked) | theme.test "survives site data being blocked…" |
| An abort detected with `instanceof Error` (a DOMException isn't one in every realm) | client.test "list responses > keeps an abort…" |
| A list that isn't a list read as empty | client.test "list responses > … is a bad response, not an empty list" |
| A broken body passed through as a `TypeError` | client.test "bytes > turns a broken body into StreamTruncatedError…" |
| The body not cancelled when the caller stops reading | client.test "bytes > cancels the download…" |
| A failed `fetch` passed through as a `TypeError`, not `NetworkError` | client.test "network errors > is a NetworkError… when fetch fails" |
| A body that breaks while reading reported as "not JSON" | client.test "network errors > … when the body breaks…" |
| Any `TypeError` shown as "Couldn't reach the server." | app.test "a page > that failed for a reason other than the network…" |
| A token with a zero-width space sent | app.test "the token > that can't be sent…" |
| A token saved before a request with it succeeded | app.test "the token > that was just entered and refused…" |
| `accepted` doing nothing (a revoked token shown as refused) | app.test "the token > that worked and was revoked later…" |
| `/v1/toolsets` with a non-list shown as none | app.test "a page > with a list that isn't a list…" |
| `object-src 'none'` dropped, or `data:` images allowed | TestSecurityHeaders |
| The Latin fonts not found (renamed) | `check-size.sh` (exit 1) |

One mutant could not be caught: setting a retry after a 401. The token wait stops the request anyway, so the mutant behaves the same as the original.

### For T055, T064 and T066

- **Where components go**:
  - generic primitives go in `packages/ui`: add shadcn ones with `pnpm --filter @agentkit/ui add-component <name>`, and write our own (like `Status`) in `src/`, exported from `index.ts`;
  - agent-specific components (chat, tool steps, A2UI for T066) go in `packages/agent-ui/src`, each with a `*.stories.tsx`, and are exported from `index.ts`;
  - console pages go in `apps/console/src/pages`, with routes in `routes.tsx` (UI.md's routes are all there already) and the rail's sections in `sections.ts`;
  - the list column (Chats list for T055, admin lists for T064) is `ListColumn` in `shell.tsx`; the shell loads the admin section's list once (`lists.ts`) and the column and the page both read it with `useLists()`;
  - a page is `<Page title aside back …>` (`pages/page.tsx`): the head bar (list toggle or Menu, Back on a phone, the h1, status and actions) and a scrolling body; `useLayout()` says whether it is a phone.
- **How to call the API**:
  - in the console, `useApi().client` is an `AgentClient`, and `useResource(load, key)` loads a resource with a skeleton, an error with Retry, and the token prompt on a 401. Render it with `<Loaded res={…} what="agents">` (in `pages/page.tsx`);
  - for a run, call `for await (const ev of client.run(req, { signal }))`; abort the signal to stop. The stream ends after `run_done` or `run_error`, and throws `StreamTruncatedError` if the body ends before either. Unknown event types and blocks are passed through, never thrown;
  - errors are `ApiError` (`status`, `code`, `retryable`, `retryAfter`). Endpoints without a typed helper use `client.getJSON<T>(path)`;
  - add typed helpers to `packages/agent-ui/src/api/client.ts` as T055 needs them: conversations, `POST /v1/conversations` (T092), cancel.
- **Keep the first load small** (under 240 KB with the first screen's fonts; `make web-check` enforces it). T055's Markdown renderer and syntax highlighting, and T064's admin pages, should be `React.lazy` chunks (Vite splits them out), so the chat's first screen doesn't wait for them. Test on a throttled connection (DevTools "Slow 4G") and a 4× CPU slowdown.
- **How to test**:
  - **unit tests** use Vitest and Testing Library (`pnpm --dir web test`). Console tests stub `fetch` with `vi.stubGlobal` and render `<App router={createMemoryRouter(routes, …)}>`, as in `app.test.tsx`;
  - **stories** are tested by being written;
  - **browser tests** go in `apps/console/e2e` and run with `make web-smoke` against the real binary; `e2e/agentkit.json` is its config. T055's chat test needs a model: start the binary with `ANTHROPIC_BASE_URL` pointing at a replay server, as `cmd/agentkit`'s e2e tests do (`cli_e2e_test.go`);
  - after any change under `web/`, run `make web` and commit `pkg/web/dist`, or CI's `web` job fails.

### Review (2026-10-13)

Sub-agents reviewed the two T078 commits (Go serving, the client, the console, the scripts and CI, tests). Fixed on `claude/t078-review-fixes`:

- **`pkg/web`**: the reserved check runs on the cleaned path, and on `/v1` and `/mcp` themselves, before the method check; `/assets` and `/assets/` are 404s, not the app. The CSP names each directive (`script-src`, `style-src`, `img-src`, `font-src`, `connect-src`, `object-src 'none'`, `base-uri 'none'`, `form-action 'self'`), so no fallback is implied.
- **The client**: a body that breaks mid-run throws `StreamTruncatedError` with the cause, unless it was an abort or `ResponseTooLargeError`; the body is cancelled when the caller stops reading; aborts are detected by name and never turned into `BadResponseError`; `/v1/agents`, `/v1/conversations` and `/v1/toolsets` with a non-list where the list goes are bad responses, not empty lists (null is still empty: Go's nil slice).
- **The console**: a token just entered and refused says "That token wasn't accepted.", and only one that worked before says it was rejected or expired; a failed load shows why (the error, status and code, Retry-After, or "Couldn't reach the server."); each list column is a `nav`; `App` makes its router once, not on every render.
- **The theme**: reading `localStorage` is inside the `try`, since with site data blocked the getter itself throws.
- **Scripts and lint**: `check-build.sh` puts the committed build back when the build fails; `check-size.sh` counts what `index.html` loads, fonts included, with a 240 KB budget in place of 200 KB of scripts and styles alone; ESLint also bans `outerHTML`, `insertAdjacentHTML`, `createContextualFragment` and `document.write`.
- **Tests** for each of these (the mutation table), plus a UTF-8 character split at every byte and a byte order mark in `SSEReader`.

A second round of sub-agents (bugs, silent failures, test coverage) reviewed those fixes. Fixed:

- **A pasted token with a zero-width space** made the browser throw before sending, which the console showed as "Couldn't reach the server." on every page, with no way back to the prompt. The card now refuses a token that isn't printable ASCII ("That token wasn't accepted.", no request).
- **Network failures are `NetworkError`** (with the cause): a failed `fetch`, or a body that breaks while reading, which `getJSON` called "not JSON". "Couldn't reach the server." is shown only for these; any other error shows its message.
- **A token is saved to `sessionStorage` once a request with it succeeds**, so a reload after a refusal doesn't call it expired.
- **`/v1/toolsets` uses `listField`**: a body that isn't an object was shown as "No toolsets are configured."
- **`check-size.sh`** counts `theme.js` too, and fails when it finds no Latin fonts instead of counting 0.
- **Tests**: `TestSecurityHeaders` checks every CSP directive's value, not just the header against the constant; the "Forget token sends no request" check waits before counting (it compared a length with itself); a revoked token; conversations' list and cursor.

Left for later:

- **Making `/mcp` configurable** if T-MCP serves it elsewhere; today it is fixed.
- **Pinning actions by SHA** and `persist-credentials: false` in CI: the existing jobs use tags, so all jobs should move together.
- **Brotli** (about 22 KB less): it needs a dependency or prebuilt `.br` files.
- **`theme.js` is revalidated on every load** (a 304); a hashed name would let it be cached.
- **A system monospace font** in place of Geist Mono would save about 23 KB of the first load.
- **Lazy-loading** the phone drawer and the account menu, and react-router's declarative mode, would cut the script further; T055 should measure first.

### The shell, matched to the mockup (2026-10-13)

A manual pass at three widths found the shell short of docs/UI.md "Layouts" and `docs/ui/console-mockup.html`: shadcn's Sidebar gave a rail and a list, but no head bar, no drawer grid, and the admin pages were lists in the content. The shell now follows the mockup:

- **Laptop and desktop**: a 56 px rail (the `ak` mark, sections, a divider, admin sections, the account at the bottom; labels as tooltips), the section's list column (248 px below 1200 px, 288 px above; a row is the name in mono, a muted line and a status dot), and the content. Its head bar (52 px) holds the list toggle (`Mod+Shift+S`, the platform's own modifier; remembered in `localStorage` as `agentkit.list`; below 900 px it starts hidden), the h1, and the page's status and action. `/agents` and `/toolsets` show the first item, as the mockup does; an agent's head has its status and "Chat with {name}" (to `/new?agent=`). Connections has no list.
- **Phone** (< 768 px): no rail. The head bar has Menu; the drawer has the sections as a three-column grid of icon and label, the Chats list, and the account. Picking anything closes it; Esc and the scrim close it and return focus to Menu; the content behind is `inert` while it is open. Admin lists are pages (rows of 44 px, with a chevron); an item's head bar has Back, and its status and action wrap to a row of their own. The chat's head bar has New chat.
- **One deviation from docs/UI.md**: its component table says the drawer is shadcn's `Sheet`. It is our own (a fixed panel with a transform), because the Sheet's scroll lock injects a `<style>` that the CSP blocks and logs. The smoke test now runs at phone width and fails on any CSP violation. The Sidebar, Sheet and Tooltip are no longer imported (they stay in `packages/ui`), so the first load dropped from 215,629 to 213,042 bytes.
- **Found while checking it in a browser**: closing the drawer focused Menu before React had removed `inert` from the content, so focus fell to `<body>`. jsdom doesn't enforce `inert`, so the unit test passed; the smoke test checks it (and fails with the fix reverted). Esc was also handled on the drawer itself, so with focus on `<body>` (a tap on the drawer's padding) it did nothing; the shell's window key listener handles it now.
- **Mutation check**: 19 mutants in the shell and pages (the list toggle not saved or not read, the width ignored below 900 px, Esc in the account menu closing the drawer too, Esc ignored when focus is on `<body>`, the shortcut ignored, a pick or Esc not closing the drawer, the content or the closed drawer not inert, no focus on open, a list in Connections, no current row, no redirect or a redirect on a phone, Back on a desktop, an unknown agent shown, a wrong tool count, `skip` ignored) were each caught by a console unit test.
- **Left for T055**: chat search and the agent filter in the list head, bottom-sheet menus on phones, the help and shortcuts button, `Mod+K`, and the conversation list itself. T064 fills the item pages ("Details are not built yet.").

### Found out of scope (for T055)

- **Modal Radix overlays** (Dialog, AlertDialog, Sheet, and a modal DropdownMenu) lock scrolling through react-remove-scroll, which injects a `<style>` that the CSP blocks. The shell's menus are `modal={false}`, and the phone drawer is our own, not a Sheet. `styles/index.css` has a static `body[data-scroll-locked]` rule, but the browser still logs a CSP violation for the blocked `<style>`, which the smoke test fails on. Before T055 uses a modal overlay (a confirm dialog, a bottom sheet), the options are:
  - a CSS-class scroll lock in place of react-remove-scroll;
  - `modal={false}` with our own focus handling;
  - a hash in the CSP, which won't work, because the injected style is computed at runtime.
- **Route parameters with a dot** (`/agents/my.agent`) get a 404 from `pkg/web`, which treats a final segment with an extension as a file. Conversation IDs (`conv_…`) are fine. If agent or toolset names may contain dots, T055 can make the fallback check only for known file extensions.
- **Turning `web` on by default**: `scripts/try-agent.sh` and docs/QUICKSTART still use `AGENTKIT_COMPONENTS=agent,mcp-server`, and QUICKSTART's environment list doesn't name `web`. Changing them is T055's scope.

### Handoff (2026-10-13)

**Done** (all on `main`): the workspace, packages, embedding and CI (`00b8182`, and gzip with the size budget in `32f8af5`); review round 1 (`fb4e9af`: reserved paths, stream errors, token wording, the size budget) and round 2 (`ac05355`: `NetworkError`, unsendable tokens, the CSP pinned per directive); the shell matched to the mockup (`06bfd08`) and Esc closing the drawer wherever focus is (`3fec178`). Every acceptance criterion is met, so `status: done`; the last two are checked by hand, not by a test. Two flakes found in this task's CI runs were fixed afterwards, on PRs #9 and #10: `TestE2EChat`'s Ctrl-C under load (chat, T054's notes) and `TestExpiredStreamRestarts`'s Close hang (`pkg/bus/redis`, a test on a fake clock).

**Open gaps**: "Left for later" (Review), "Left for T055" (the shell section) and "Found out of scope". None blocks T055.

**What T055 must know** (details in "For T055, T064 and T066"):

- **Layout**: primitives in `web/packages/ui`, agent components (with stories) in `web/packages/agent-ui`, pages in `web/apps/console/src/pages`, routes in `routes.tsx`. Packages are sources, not built libraries; import components by path.
- **The build is committed**: after any change under `web/`, run `make web` and commit `pkg/web/dist`, or CI's stale check fails. `go build` never needs Node.
- **Size budget**: 240 KB for the first load (gzipped script, styles, `index.html`, `theme.js`, and the Latin fonts), enforced by `check-size.sh` in `make web-check`; it is 213,042 bytes now. Load Markdown, highlighting and admin pages as `React.lazy` chunks.
- **CSP**: `default-src 'self'`, no inline script or style. Modal Radix overlays (Dialog, Sheet, a modal DropdownMenu) inject a `<style>` it blocks, and the smoke test fails on the violation; see "Found out of scope" for the options.
- **Gates**: `make web-check` (lint, format, typecheck, unit tests, audit, rebuild and stale check, size) and `make web-smoke` (Playwright against the real binary; never `playwright install`). Both run in CI's `web` job.
- **Reserved paths**: `pkg/web` never answers `/v1`, `/mcp` or paths under them with the app (checked on the cleaned path). A final segment with an extension is a file, so `/agents/my.agent` is a 404.

