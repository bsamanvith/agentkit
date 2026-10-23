---
id: T114
title: Require auth, and a Cloudflare Access gate, for serving through a tunnel
epic: E07
status: review
depends_on: [T040, T041, T061]
---

# T114: Require auth, and a Cloudflare Access gate, for serving through a tunnel

## Context

On loopback with no credentials, `agentkit serve` stays open (E07's
"off-loopback without auth" decision). A tunnel or reverse proxy on the same
machine (cloudflared, ngrok, nginx) connects over loopback, so it would
publish that open server. The owner runs agentkit on one VM behind a
Cloudflare Tunnel with an Access application in front
([deploy/cloudflare](../deploy/cloudflare/README.md)). This task adds a mode
that refuses to start open, a gate that checks Access's assertion at the
origin, and moves the toolchain past a stdlib advisory with a vulnerability
check in CI. PR bsamanvith/agentkit#112.

## Scope

- `AGENTKIT_REQUIRE_AUTH` / `auth.required`: serve refuses to start with no
  authentication, whatever the address.
- `pkg/auth/cfaccess`: verifies `Cf-Access-Jwt-Assertion` against the team's
  keys (through `pkg/auth/jwt`'s key cache) and turns its `email` into a
  principal; `auth.cloudflare_access` and `AGENTKIT_CF_ACCESS_TEAM_DOMAIN`,
  `AGENTKIT_CF_ACCESS_AUD`.
- The gate in front of every request of a process that runs `agent`,
  `mcp-server` or `web`, but an exact list of public requests.
- `pkg/auth/jwt`: stale keys are capped, and a failed first fetch is retried
  sooner.
- Go 1.27.3 (go.mod, Dockerfile, CI), `make vulncheck` (govulncheck v1.9.0)
  in CI's `check` job and the nightly workflow.
- Docs: QUICKSTART, COMPONENTS, deploy/cloudflare, ENGINEERING §3, AGENTS.md,
  SIZES.

## Out of scope

- Access service tokens (no email): refused, not mapped to a principal.
- A `cloudflare_access.required` flag: the file's section is what makes
  Access mandatory.
- Leaving the Access code out of the runner and toolsets images with a
  build tag (the SIZES row names it as what gets cut first).
- Pinning the Go base image by digest.

## Acceptance criteria

Each item is one observable behavior and maps to one test.

- [ ] With require-auth on (`AGENTKIT_REQUIRE_AUTH=1` or `"required": true`)
      and no credentials, serve exits 1 on a loopback address; runner and
      toolsets without `AGENTKIT_RPC_TOKEN` too — test:
      `cmd/agentkit.TestServeRejectsBadSetup` ("require auth: ..." cases)
- [ ] `AGENTKIT_REQUIRE_AUTH` takes 1/true/yes and 0/false/no, refuses
      anything else, and 0 does not turn off the file's `"required": true` —
      test: `cmd/agentkit.TestServeRejectsBadSetup`
- [ ] With Access on, a request without a valid assertion for the team and
      application gets 401 before any component sees it, and a valid one
      reaches it — test: `cmd/agentkit.TestE2EServeCloudflareAccess`,
      `pkg/auth/cfaccess.TestRequire`
- [ ] Exactly these pass without an assertion: GET or HEAD of `/healthz`,
      `/readyz`, `/oauth/callback`, `/oauth/client-metadata.json`, and POST
      `/v1/hooks/{id}` with an id of `[A-Za-z0-9_-]`; path variants (`..`,
      `%2e%2e`, `//healthz`, `/healthz/`, other methods) do not — test:
      `cmd/agentkit.TestAccessPublic`
- [ ] A valid assertion's `email` (lower-cased) is the user, in the
      configured tenant with the configured scopes and toolsets (default
      `*`); a key sent beside it wins — test:
      `pkg/auth/cfaccess.TestValidAssertionGivesTheOwner`,
      `TestConfiguredScopesAndToolsets`
- [ ] Refused assertions (wrong signature, algorithm, issuer, audience,
      expired, no email, another team's key) get 401 and log at debug only —
      test: `pkg/auth/cfaccess.TestRefusedAssertions`,
      `TestRefusalsLogAtDebug`
- [ ] With `emails` set, only those emails are let in — test:
      `pkg/auth/cfaccess.TestEmails`
- [ ] With no `emails`, serve warns at startup; with require-auth on it
      refuses to start unless `scopes` is set in `auth.cloudflare_access` —
      test: `cmd/agentkit.TestServeRejectsBadSetup` ("access, no emails")
- [ ] The file's `auth.cloudflare_access` section makes Access mandatory: no
      `team_domain` or `aud` (from the file or the variables) fails startup —
      test: `cmd/agentkit.TestServeRejectsBadSetup`
- [ ] A process running only `runner` or `toolsets` is never gated; beside
      `agent`, `mcp-server` or `web`, `/rpc/` passes only when `AGENTKIT_RPC_TOKEN` is set; the split
      deployment works with the Access variables set on every process — test:
      `cmd/agentkit.TestAccessGate`, `TestE2ESplitDeployment`
- [ ] `GET /readyz` without a valid assertion answers its status code and
      `{"status":"ready"}` or `{"status":"not ready"}` only — test:
      `cmd/agentkit.TestAccessGate`
- [ ] When the team's keys can't be fetched, stale keys are used for at most
      24 hours past their expiry, then every assertion fails; with no keys
      cached a failed fetch is retried after 5 s — test:
      `pkg/auth/jwt.TestStaleKeysAreCapped`,
      `TestFirstFetchFailedRetriesSooner`
- [ ] go.mod, the Dockerfile and CI use Go 1.27.3, and `make vulncheck`
      (govulncheck v1.9.0) exits 0, in CI's `check` job and nightly — checked
      by running it, see Notes
- [x] Docs: QUICKSTART and COMPONENTS (require-auth values and scope,
      the public list, which components read the Access variables),
      deploy/cloudflare (the file section, emails, MCP clients through
      Access), ENGINEERING §3 and AGENTS.md (`make vulncheck`), SIZES —
      `scripts/lint-skills.sh` checks the links

## Notes

- Review of #112 (2026-10-23) found the gate wrapped `/rpc/runner/` and
  `/rpc/toolsets/` too, which broke the split deployment with the Access
  variables set on every container (callers send the RPC token, never an
  assertion). Decided: Access guards only the processes people reach
  (`agent`, `mcp-server`, `web`).
- Access is mandatory only through the file's section: a misspelled variable
  alone silently leaves it off, and a key still satisfies require-auth. With
  require-auth on and Access off, serve logs "Cloudflare Access is not
  configured".
- require-auth does not apply to a process serving only `web` (static
  files); serve logs that.
- MCP clients reach `/mcp` through Access with `cf-access-token:
  $(cloudflared access token -app=...)`; the token expires with the Access
  session (deploy/cloudflare, "MCP clients").
- `make vulncheck` run 2026-10-23 on the branch: exit 0, no vulnerabilities
  reachable; one in a required module not called (GO-2026-5932,
  `golang.org/x/crypto/openpgp`, no fix).
- Sizes, 2026-10-23 (stripped binaries, main `9899c35` vs `7fde521`): runner
  +102,400, the rest under 100 KB; row in docs/SIZES.md. The all-in-one has
  about 150 KB left of 37 MB, api about 130 KB of 23 MB.
- The test criteria passed once on the branch with the review's fixes in
  progress (one run, not the `-race -count=20` gate); tick them after `make
  check` and CI.
