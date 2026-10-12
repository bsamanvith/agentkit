---
id: T078
title: "Web workspace: React + TypeScript + shadcn/ui, shared packages, console app shell, embedding and CI"
epic: E11
status: todo
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

- [ ] `pnpm install && make web` builds; `agentkit serve` with `web` serves the console shell at `/` with light, dark and system themes from the shared tokens.
- [ ] `make web-check` passes (lint, typecheck, unit tests, build, stale-build check) and runs as its own CI job; changing a component without rebuilding fails the stale check.
- [ ] Playwright smoke: the shell loads, the theme switch works, `/v1/agents` (T062) or a stub is called with the token.
- [ ] `go build ./...` from a fresh clone without Node succeeds; the image builds and its size is recorded.
- [ ] `agent-ui` builds as a separate package that a second app (a throwaway `apps/example`) can import (proves the sharing works); remove the example after.
