---
id: T061
title: "Local users and login sessions for the console"
epic: E07
status: todo
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

## Acceptance criteria

- [ ] **Login and logout:** a right password sets the cookie and `/v1/auth/me` returns the user; a wrong password or an unknown user gives the same 401 body, with timing within a small bound (statistical test with a fake hasher clock, not wall-clock flakes); logout invalidates the session server-side.
- [ ] **Cookie:** it has `HttpOnly`, `SameSite=Strict`, and `Secure` under TLS; the session ID is 128 bits or more from `crypto/rand`.
- [ ] **CSRF:** a cookie-authenticated POST without the CSRF header, or with a foreign `Origin`, gives 403; a bearer POST is unaffected.
- [ ] **Throttling:** 10 bad attempts back off the next one (fake clock), and other users are not affected.
- [ ] **Roles:** a `user` gets 403 on admin-only fields and endpoints, and an `admin` gets them.
- [ ] **Canary:** a password never appears in any log, error or response.
- [ ] **`FuzzLogin`** (request bodies) never panics or authenticates.
- [ ] **Mutation list** recorded.
