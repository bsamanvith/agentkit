# web

agentkit's web apps: React + TypeScript, with shadcn/ui on Radix and Tailwind
(E11 "Web stack", T078). The console is built into `../pkg/web/dist`, which
is committed and embedded in the `agentkit` binary, so `go build` needs no
Node.js.

```
web/
  packages/ui         @agentkit/ui        tokens (docs/UI.md), themes, shadcn/ui primitives, Status
  packages/agent-ui   @agentkit/agent-ui  agent components (tool step, step group, source chip,
                                          message, composer) and the /v1 client (./api)
  apps/console        @agentkit/console   the admin console: shell, routes, token prompt
  scripts/check-build.sh                  the stale-build check
  scripts/check-size.sh                   the first-load budget
```

`apps/*` depend on `packages/*`; `agent-ui` depends on `ui`; `ui` depends on
nothing of ours. Packages are TypeScript sources, not built libraries: an
app's Vite bundles them, and imports by path keep unused components out
(`@agentkit/ui/components/button`).

## Running

Node 24 LTS (`.nvmrc`) and pnpm 12 (`packageManager`; `corepack enable` or
`npm i -g pnpm@12.6.0`). From the repository root:

```bash
make web            # install (frozen lockfile) and build the console into pkg/web/dist
make web-check      # lint, format, typecheck, unit tests, pnpm audit, stale-build check
make web-smoke      # Playwright: the console in agentkit serve, in a real browser
```

During development, run `agentkit serve` with the agent API on :8080 and:

```bash
pnpm --dir web dev
```

Vite serves the console with hot reload and proxies `/v1` to
`AGENTKIT_URL` (default `http://127.0.0.1:8080`). For the real CSP and
caching, use `make web` and `AGENTKIT_COMPONENTS=agent,web agentkit serve`.

**Commit `pkg/web/dist` with every change to `web/`.** CI rebuilds and fails
when the committed build is not what the sources make. Builds are
byte-for-byte reproducible (checked on macOS and Linux). A change that
doesn't reach the console's bundle (a component it doesn't import) doesn't
change the build, so it doesn't fail the check either.

## Rules the code keeps

- **CSP**: `pkg/web` serves `default-src 'self'` with no inline script or
  style. So: fonts are self-hosted, the theme is applied before first paint
  by `apps/console/public/theme.js` (not an inline script), nothing is
  inlined as a data URI, and libraries that inject `<style>` tags are
  blocked. Radix's modal scroll lock is one of these: use `modal={false}`
  where you can. `styles/index.css` has a static rule for its
  `data-scroll-locked`, so the lock still works, but the browser logs the
  blocked style, and the smoke test fails on any CSP violation.
- **Size**: the console has to work on low-end machines and slow links.
  `pkg/web` gzips text files once at start and sends them compressed;
  fonts use `font-display: swap` and `unicode-range`, so text shows at once
  and Latin Extended loads only when used. `make web-check` fails when the
  first load (gzipped `index.html`, scripts and styles, plus the Latin
  fonts) is over 240 KB (`scripts/check-size.sh`; it is 215 KB at T078). Lazy-load
  (`React.lazy`) what the first screen doesn't need instead of raising it.
- **Untrusted text stays text**: ESLint bans `dangerouslySetInnerHTML`,
  `innerHTML`, `outerHTML`, `insertAdjacentHTML`, `createContextualFragment`
  and `document.write`.
- **Tokens**: `packages/ui/src/styles/tokens.css` is generated from the
  `## Tokens` block of `docs/UI.md`, prefixed `--ak-`. After changing UI.md,
  run `pnpm --filter @agentkit/ui tokens`; a test fails until you do.
  shadcn's variables (`--background`, `--primary`, ...) are mapped onto the
  tokens in `styles/index.css`, per UI.md's table.
- **Themes**: light, dark and system. The choice is in localStorage
  (`agentkit.theme`) and shown as `data-theme` on `<html>`; "system" means
  no attribute.

## Adding things

- **A shadcn component**: `pnpm --filter @agentkit/ui add-component dialog`.
  It wraps the shadcn CLI and fixes an import it gets wrong in workspaces.
  Generated files stay as generated (lint relaxes two rules for them).
- **An agent component**: in `packages/agent-ui/src`, with a
  `*.stories.tsx` next to it. Stories are CSF3 objects (Storybook's format,
  without Storybook); `components.test.tsx` renders every story, so a new
  story is tested with no extra work.
- **A consumer app**: `apps/<name>` with its own `package.json` depending on
  `"@agentkit/ui": "workspace:*"` and `"@agentkit/agent-ui": "workspace:*"`,
  a Vite config with `@vitejs/plugin-react` and `@tailwindcss/vite`, and a
  CSS entry like the console's: `@import "tailwindcss"`,
  `@import "@agentkit/ui/styles.css"`, and an `@source` for each package so
  Tailwind sees their classes. T078 built a throwaway `apps/example` this way
  to prove it.

## Dependencies

Versions are exact (`saveExact`) and the lockfile is committed; CI installs
with `--frozen-lockfile`. No package may run install scripts
(`onlyBuiltDependencies: []`). `make web-check` runs `pnpm audit --audit-level
high`, so a new high or critical advisory fails CI until the dependency is
upgraded.

## Browsers

The smoke test never downloads a browser (no `playwright install`). In CI it
uses the runner's Google Chrome (`channel: "chrome"` when `CI` is set);
elsewhere, the Chromium Playwright finds in `PLAYWRIGHT_BROWSERS_PATH` (for
example `/opt/pw-browsers`) or its cache. `@playwright/test` is pinned to a
version whose browsers are installed there.
