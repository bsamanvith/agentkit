---
id: T061
title: "Local users and login sessions for the console"
epic: E07
status: done
depends_on: [T039]
---

# T061: local users and login

## Context

The console needs a simple user login (owner, 2026-10-12). API clients keep bearer keys (T039). Login is one more `auth.Authenticator`, so every endpoint sees the same principal (tenant, user, scopes). Decisions: [E11](E11-clients.md) ("Login", "Roles"), [E07](E07-auth-credentials.md).

## Scope

- **Users in config:** `auth.users: [{username, password_hash, tenant, roles: [admin|user]}]`, or a file named by `AGENTKIT_USERS_FILE`.
  - `password_hash` is argon2id in PHC format.
  - `agentkit users hash-password` reads a password from stdin and prints the hash.
  - Add `golang.org/x/crypto` to the archtest allowlist for `pkg/auth` only.
- **Endpoints:**
  - `POST /v1/auth/login {username, password}` returns 204 and sets the session cookie;
  - `POST /v1/auth/logout`;
  - `GET /v1/auth/me` returns username, tenant and roles.
- **Session cookie:** an opaque random ID, `HttpOnly`, `SameSite=Strict`, `Secure` when served over TLS or behind `X-Forwarded-Proto: https` from a trusted proxy setting, path `/`. Idle timeout 12 h, absolute timeout 7 days. The session store is in memory now (a pluggable interface, store-backed later).
- **CSRF:** a request authenticated by cookie with an unsafe method must carry `X-Agentkit-CSRF` (a value from `/v1/auth/me`) and a same-origin `Origin`. Bearer requests are unaffected.
- **Login throttling:** per username and per IP, with exponential backoff. Messages never say whether the username exists, and timing is equal for known and unknown users (a dummy hash).
- **Roles to scopes:** `user` gets `agent:run` and `agent:read`; `admin` also gets `admin:*` (agents' system prompts, the credentials page).

## References

- [OWASP Session Management Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html)
- [OWASP CSRF Prevention Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Cross-Site_Request_Forgery_Prevention_Cheat_Sheet.html)
- [OWASP Password Storage Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html) and [RFC 9106: Argon2](https://www.rfc-editor.org/rfc/rfc9106) (parameters); `golang.org/x/crypto/argon2`
- [MDN: Set-Cookie](https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Set-Cookie) (`SameSite`, `Secure`, `HttpOnly`, `__Host-` prefix)

## Acceptance criteria

- [x] **Login and logout:** a right password sets the cookie and `/v1/auth/me` returns the user; a wrong password or an unknown user gives the same 401 body, with timing within a small bound (statistical test with a fake hasher clock, not wall-clock flakes); logout invalidates the session server-side.
- [x] **Cookie:** it has `HttpOnly`, `SameSite=Strict`, and `Secure` under TLS; the session ID is 128 bits or more from `crypto/rand`.
- [x] **CSRF:** a cookie-authenticated POST without the CSRF header, or with a foreign `Origin`, gives 403; a bearer POST is unaffected.
- [x] **Throttling:** 10 bad attempts back off the next one (fake clock), and other users are not affected.
- [x] **Roles:** a `user` gets 403 on admin-only fields and endpoints, and an `admin` gets them.
- [x] **Canary:** a password never appears in any log, error or response.
- [x] **`FuzzLogin`** (request bodies) never panics or authenticates.
- [x] **Mutation list** recorded.

## Notes

### What was built

- `pkg/auth/login` (new): users, argon2id hashes (`HashPassword`, PHC strings parsed strictly), sessions (`Store` interface, `MemoryStore`, conformance suite `logintest.RunStore`), CSRF, throttling and the `auth.Authenticator` (`Login.Authenticate`, kind `session`). `logintest.Hasher` is a fake hasher with a work clock for the timing test.
- `pkg/agentapi`: `WithLogin`; `GET /v1/auth/config` and `POST /v1/auth/login` (public), `POST /v1/auth/logout` and `GET /v1/auth/me` (any credential, no scope); the CSRF middleware after authentication; codes `csrf_failed` (403) and `too_many_attempts` (429 with `Retry-After`); `wire/auth.go`. The agent's system prompt (`GET /v1/agents/{name}`) needs `admin:agents` (`ScopeAdminAgents`); an open server still shows it. OpenAPI: the four paths, `AuthConfig`, `LoginRequest`, `Me`, the `session` security scheme.
- `cmd/agentkit`: `auth.users`, `auth.trusted_proxies`, `AGENTKIT_USERS_FILE`, `agentkit users hash-password`; serve accepts users as the credential off loopback unless `mcp-server` runs (sessions never reach `/mcp`). `TestE2EServeLogin` drives the real binary.
- Console: the password card (`web/apps/console/src/sign-in.tsx`) per docs/UI.md "Sign in", the token card behind "Use an API token instead", the account menu's name, role and tenant and **Sign out** (asks first while an answer runs), clearing drafts and runs on sign-out or when another user signs in; `AgentClient.authConfig/me/login/logout`, and the CSRF header on requests other than GET (read again from `/me` once on a `csrf_failed` 403).
- Docs: QUICKSTART "Credentials", docs/SIZES.md row.

Tests per criterion: login and logout `TestSignInAndOut`, `TestSignIn` (agentapi), `TestE2EServeLogin`; equal cost `TestFailedSignInsCostTheSame` (fake hasher work clock: an unknown user costs what the costliest known one does, with one set of parameters or several); cookie `TestCookie`; CSRF `TestCheckCSRF`, `TestCSRF`; throttling `TestThrottling`, `TestSignInThrottled`; roles `TestRoles`, `TestScopesOf`; canary `TestCanary` (logs, errors, responses, the store, and the sessions' ids); `FuzzLogin` (agentapi); lifetimes `TestSessionLifetimes`, `TestExpiredSessionsAreSwept`; console `signing in with a password` in app.test.tsx and `sign-in` in client.test.ts.

### Decisions

- **A subpackage**, `pkg/auth/login`, so only it imports `golang.org/x/crypto/argon2` (archtest allowlists it there, not for all of `pkg/auth`).
- **argon2id parameters:** OWASP's minimum (m=19 MiB, t=2, p=1, 16-byte salt, 32-byte key). Hashes with other parameters verify; the dummy hash for an unknown user costs what the costliest configured one does. At most 4 hashes run at once, so a burst of sign-ins cannot take the server's memory.
- **Throttling:** 10 failures per username, 100 per address (an office behind one NAT signs in together), then 1 s doubling to 15 min; forgotten after 1 h without a failure; a success resets the username. A throttled attempt is not checked (429 before hashing). Unknown names are counted like known ones. At most 10,000 keys are kept.
- **Bearer before cookie:** `auth.Chain(bearer, login)`. A request with both acts as the bearer and needs no CSRF token.
- **Sessions only on `/v1`**, never `/mcp`: MCP clients are not browsers. Hence serve off loopback with users alone refuses `mcp-server`.
- **Roles:** `admin` also gets `agent:admin`, so an admin can manage toolsets at run time (T096) from the console; users' `toolsets` default to `*`.
- **Login CSRF:** a sign-in carrying an `Origin` must be same-origin (403 `csrf_failed`); one without (curl) is allowed.
- **Empty username or password is 400**, not 401: the console checks first, and it is not a failed attempt.
- **Store:** in memory (`MemoryStore`, 10,000 sessions, least recently seen evicted), keyed by the SHA-256 of the session id so a store dump cannot be replayed. Sessions do not survive a restart or reach other replicas yet.
- **Plain HTTP off loopback** logs a warning at startup (the cookie cannot be `Secure`) rather than failing: a TLS proxy may terminate in front without being listed.
- **Secure cookie** is `__Host-agentkit_session`; over plain HTTP (loopback dev) it is `agentkit_session`.

### Mutation list

Each was planted and the tests above failed (script in the worker's scratch folder):

1. cookie not `HttpOnly`; 2. `SameSite=Lax`; 3. never `Secure`; 4. an unknown user skips the dummy hash; 5. sign-out leaves the session in the store; 6. the CSRF token not compared; 7. `Origin` not checked; 8. idle timeout ignored; 9. absolute timeout ignored; 10. a success does not reset the username's count (the first run survived; `TestThrottling` now fails 9 times after a success and expects no wait); 11. throttle threshold off by one; 12. throttling per address only; 13. a 4-byte session id; 14. `X-Forwarded-For` trusted from any peer; 15. `user` gets the admin scopes; 16. the system prompt shown to every caller; 17. no CSRF middleware; 18. cookie before bearer in the chain; 19. the password logged on failure; 20. an unknown username logged (it may be a password).
Console: the client sends no CSRF header; another user keeps the last user's draft; a 401 leaves the password filled; the throttled button stays enabled; sign-out keeps the session flag; a password sign-in keeps a stale token (which the server would prefer to the cookie; this survived until a test was added).

### For T064 (console admin pages)

Admin-only content is gated by scope in the API: `admin:agents` for the system prompt today. Add `admin:credentials` and so on under `admin:*` (the `admin` role's), and read the role from `GET /v1/auth/me` only to hide controls, never to protect them.

### For a store-backed session store

`login.Store` is the seam (`WithStore`); `logintest.RunStore` is its conformance suite. Keys are the SHA-256 hex of the session id; `Expire(idle, created)` sweeps both timeouts. A store-backed one lets sessions survive restarts and work across replicas; pick it where `cmd/agentkit` builds the login (`c.login`).

### Follow-ups

- A Playwright test with users configured (the e2e server has only a token today, so the existing tests keep the token card).
- Restoring the route after a session ends works because the app stays mounted; "a message that got the 401 goes back into the composer" is the run store's existing behaviour, not re-tested here.
