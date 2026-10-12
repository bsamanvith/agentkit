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

- [x] `pnpm install && make web` builds; `agentkit serve` with `web` serves the console shell at `/` with light, dark and system themes from the shared tokens.
- [x] `make web-check` passes (lint, typecheck, unit tests, build, stale-build check) and runs as its own CI job; changing a component without rebuilding fails the stale check.
- [x] Playwright smoke: the shell loads, the theme switch works, `/v1/agents` (T062) or a stub is called with the token.
- [x] `go build ./...` from a fresh clone without Node succeeds; the image builds and its size is recorded.
- [x] `agent-ui` builds as a separate package that a second app (a throwaway `apps/example`) can import (proves the sharing works); remove the example after.

## Notes

### Results

- **`make web`** builds the console into `pkg/web/dist` (committed; about 630 KB: a 482 KB script, a 64 KB stylesheet and four woff2 fonts). With `AGENTKIT_COMPONENTS=agent,web`, `agentkit serve` serves the shell at `/` with System, Light and Dark themes from docs/UI.md's tokens.
- **What goes over the wire**: `pkg/web` gzips text files once, at start, and sends them compressed to clients that accept it (with `Vary: Accept-Encoding` and an ETag per encoding). The first load is 162,288 bytes gzipped: the script is 150,665 bytes and the styles 11,204, out of 546 KB raw. Fonts (84 KB, cached for a year) load after first paint (`font-display: swap`), and Latin Extended loads only when it is needed (`unicode-range`). `web/scripts/check-size.sh` fails `make web-check` above 200 KB gzipped (owner's request, 2026-10-12: the console runs on low-end PCs and bad connections).
- **`make web-check`** runs install (frozen lockfile), lint (0 warnings), format check, typecheck, unit tests (15 in `ui`, 77 in `agent-ui`, 17 in the console), `pnpm audit --audit-level high`, a rebuild compared with the committed build, and the size budget. It runs in CI's `web` job, next to `check` and `fuzz`, which are unchanged.
- **The stale check fails** when a component the console uses is changed without a rebuild (tried with Composer's placeholder: exit 1, naming the changed files). It compares builds, so a change to a component the console doesn't import (ToolStep, tried) changes nothing that is embedded and passes. That is intended.
- **Builds are reproducible.** A build in `node:24.14.0` on Linux was byte-identical to the macOS build.
- **`make web-smoke`** (Playwright, three tests) runs against the real binary (`agent,web`, with a token):
  - the shell loads;
  - the token prompt appears after a 401, and `/v1/agents` is then called with `Authorization: Bearer`;
  - Dark and Light set `data-theme` and change the computed background, a reload keeps the choice, and System follows `prefers-color-scheme`;
  - `theme.js` pins the theme with the app bundle blocked;
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
  - `/v1/` and `/mcp/` are never answered with the app;
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
  - `src/`: `app.tsx`, `routes.tsx`, `shell.tsx`, `sections.ts`, `api.ts`, `api-provider.tsx`, `token-prompt.tsx`, `pages/` (`chat`, `admin`, `page`, `readiness`), `app.test.tsx`;
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

One mutant could not be caught: setting a retry after a 401. The token wait stops the request anyway, so the mutant behaves the same as the original.

### For T055, T064 and T066

- **Where components go**:
  - generic primitives go in `packages/ui`: add shadcn ones with `pnpm --filter @agentkit/ui add-component <name>`, and write our own (like `Status`) in `src/`, exported from `index.ts`;
  - agent-specific components (chat, tool steps, A2UI for T066) go in `packages/agent-ui/src`, each with a `*.stories.tsx`, and are exported from `index.ts`;
  - console pages go in `apps/console/src/pages`, with routes in `routes.tsx` (UI.md's routes are all there already) and the rail's sections in `sections.ts`;
  - the list column (Chats list for T055, admin lists for T064) is `ListColumn` in `shell.tsx`.
- **How to call the API**:
  - in the console, `useApi().client` is an `AgentClient`, and `useResource(load, key)` loads a resource with a skeleton, an error with Retry, and the token prompt on a 401. Render it with `<Loaded res={…} what="agents">` (in `pages/page.tsx`);
  - for a run, call `for await (const ev of client.run(req, { signal }))`; abort the signal to stop. The stream ends after `run_done` or `run_error`, and throws `StreamTruncatedError` if the body ends before either. Unknown event types and blocks are passed through, never thrown;
  - errors are `ApiError` (`status`, `code`, `retryable`, `retryAfter`). Endpoints without a typed helper use `client.getJSON<T>(path)`;
  - add typed helpers to `packages/agent-ui/src/api/client.ts` as T055 needs them: conversations, `POST /v1/conversations` (T092), cancel.
- **Keep the first load small** (under 200 KB gzipped; `make web-check` enforces it). T055's Markdown renderer and syntax highlighting, and T064's admin pages, should be `React.lazy` chunks (Vite splits them out), so the chat's first screen doesn't wait for them. Test on a throttled connection (DevTools "Slow 4G") and a 4× CPU slowdown.
- **How to test**:
  - **unit tests** use Vitest and Testing Library (`pnpm --dir web test`). Console tests stub `fetch` with `vi.stubGlobal` and render `<App router={createMemoryRouter(routes, …)}>`, as in `app.test.tsx`;
  - **stories** are tested by being written;
  - **browser tests** go in `apps/console/e2e` and run with `make web-smoke` against the real binary; `e2e/agentkit.json` is its config. T055's chat test needs a model: start the binary with `ANTHROPIC_BASE_URL` pointing at a replay server, as `cmd/agentkit`'s e2e tests do (`cli_e2e_test.go`);
  - after any change under `web/`, run `make web` and commit `pkg/web/dist`, or CI's `web` job fails.

### Found out of scope (for T055)

- **The Sidebar's shortcut.** shadcn's Sidebar toggles on Mod+B, checking `metaKey || ctrlKey` on every platform. docs/UI.md says Mod+Shift+S with the platform's own modifier. Change `SIDEBAR_KEYBOARD_SHORTCUT` and the check in `sidebar.tsx`, or turn it off.
- **Modal Radix overlays** (Dialog, AlertDialog, Sheet, and a modal DropdownMenu) lock scrolling through react-remove-scroll, which injects a `<style>` that the CSP blocks. The overlay still works, but the page behind can scroll, and the console logs a CSP violation, which the smoke test fails on. The shell's menu is `modal={false}`. The Sidebar's phone drawer is a Sheet, so it hits this too; the smoke test runs at desktop width only. T055 options:
  - a CSS-class scroll lock in place of react-remove-scroll;
  - `modal={false}` with our own focus handling;
  - a hash in the CSP, which won't work, because the injected style is computed at runtime.
- **Route parameters with a dot** (`/agents/my.agent`) get a 404 from `pkg/web`, which treats a final segment with an extension as a file. Conversation IDs (`conv_…`) are fine. If agent or toolset names may contain dots, T055 can make the fallback check only for known file extensions.
- **Turning `web` on by default**: `scripts/try-agent.sh` and docs/QUICKSTART still use `AGENTKIT_COMPONENTS=agent,mcp-server`, and QUICKSTART's environment list doesn't name `web`. Changing them is T055's scope.
