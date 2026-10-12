---
id: T039
title: "pkg/auth: principals, API-key authenticator, edge middleware"
epic: E07
status: done
depends_on: [T027]
---

# T039: `pkg/auth`

## Context

Every request should carry a tenant (E05 scopes all data by it), but today nothing authenticates callers beyond one static token on `/v1`. This task builds the reusable part: the middleware and the API-key authenticator. T040 wires it into `agentkit serve`. Decisions: [E07](E07-auth-credentials.md).

## Scope

- **`auth.Principal`**: `Tenant tenancy.Tenant`, `User string`, `Scopes []string`, `Toolsets []string` (names or `path.Match` globs; empty means none).
  - `p.AllowsToolset(name) bool` and `p.HasScope(s) bool`.
  - `auth.From(ctx) (Principal, bool)`.
- **`auth.Authenticator`** interface: `Authenticate(r *http.Request) (Principal, error)`.
  - Errors: `ErrNoCredentials` (nothing presented) and `ErrInvalidCredentials` (presented and wrong).
  - Any other error is internal: it answers 500 and is logged without the credential.
- **`auth.Middleware(a Authenticator, opts...) func(http.Handler) http.Handler`**:
  - On success it puts the principal in `ctx`, plus `tenancy.With`, `WithUser` and `WithRequest`. The request ID comes from a header if it is valid, otherwise it is generated.
  - On failure it answers 401 with `WWW-Authenticate: Bearer` (realm, plus `error="invalid_token"` when a token was presented).
  - The failure body is an option, so `/v1` keeps its JSON error shape and MCP gets a plain one.
  - An option adds `resource_metadata` to the header (used by T041).
- **`auth.APIKeys`**, an Authenticator built from config entries `{id, hash, tenant, user?, scopes, toolsets}`:
  - `hash` is `sha256:<hex>` of the key; the key itself is never in config.
  - A key is read from `Authorization: Bearer <key>`.
  - Comparison is constant time. Lookup is by hash, and no early exit reveals which entry matched.
  - Entry IDs appear in logs; keys never do.
  - `auth.HashKey(key) string` helper.
  - Keys shorter than 16 bytes are rejected when the entries are built.
- **`auth.Chain(authenticators...)`**: tries each in order. `ErrNoCredentials` moves to the next; `ErrInvalidCredentials` stops. This lets T041 add JWT beside API keys.
- **`authtest`**: a fake authenticator, and `RunAuthenticator`, a conformance suite every authenticator passes (T041 reuses it).

## Out of scope

- Wiring into `cmd/agentkit` and the agent API (T040). JWT (T041). Rate limiting per principal (later).

## References

- [RFC 6750: Bearer Token Usage](https://www.rfc-editor.org/rfc/rfc6750) (header syntax, `WWW-Authenticate` errors)
- [OWASP Authentication Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html)
- Go `crypto/subtle.ConstantTimeCompare`; how `golang.org/x/oauth2` and the MCP Go SDK `auth` package shape their middleware (we reuse the SDK's metadata types later, E07)

## Acceptance criteria

- [x] A valid key gives a principal whose tenant, user, scopes and toolsets are exactly those configured, and the handler sees them through `auth.From`, `tenancy.From` and `tenancy.User`. — `TestValidKeyGivesConfiguredPrincipal` (three keys, two tenants, a user key and service keys, under `Bearer`, `bearer`, `BEARER` and several spaces); the rig checks `tenancy.From`, `tenancy.User` and `tenancy.Request` against the principal on every handled request; `RunAuthenticator`'s `ValidCredentialGivesItsPrincipal` (`TestAPIKeysConformance`, `TestChainConformance`). `Example` shows it in short.
- [x] A missing key, a wrong key, a key differing in one byte, a key prefix, a key with a different scheme (`Basic`, no scheme, lowercase `bearer` is accepted per RFC 6750), several `Authorization` headers, and an oversized header all give 401 with the right `WWW-Authenticate`. The handler is never called. — `TestRejectedRequestsGet401` (11 cases, each checks the exact challenge: `Bearer realm="agentkit"` when nothing was sent, plus `error="invalid_token"` otherwise, and that the handler did not run); lowercase `bearer` in `TestValidKeyGivesConfiguredPrincipal`; `TestBearerToken` for the boundaries (the length limit to the byte, NUL, DEL, tab, non-ASCII); the suite's `WrongSecretIsInvalid`, `AlteredSecretNeverAuthenticates` and `HostileHeadersNeverAuthenticate`.
- [x] Two keys for two tenants never cross: a request with A's key can only produce tenant A, whatever other headers say (a client-sent tenant or user header is ignored). — `TestKeysNeverCrossTenants` (acme's keys with `X-Tenant`, `X-Tenant-Id`, `X-Agentkit-Tenant`, `X-User`, `X-Agentkit-User` naming globex and mallory, and the reverse); the suite's `TenantHeadersAreIgnored` (11 such headers).
- [x] Toolset globs: `AllowsToolset` matches names and globs, and empty allows nothing (table test). — `TestAllowsToolset` (17 rows: nil and empty, exact, case, `*`, `?`, classes, a malformed pattern matching nothing); `TestHasScope` for scopes.
- [x] `Chain`: no credentials falls through; invalid credentials stop; the first success wins. — `TestChain` (8 rows, with how many times each authenticator was called: an internal error stops too; an error matching both sentinels counts as invalid); `TestChainConformance`.
- [x] Logging: with a canary key, no log line, error string or response body contains the key or its hash (handler-side log capture). — `TestCanaryKeyNeverLeaks` (the canary sent 10 ways through two middlewares, `Authenticate`'s errors, five configuration errors about the canary's entry, a duplicate, `KeyEntry` with a short and a spaced canary, and `%v`/`%+v`/`%#v` of the authenticator). The search (`authtest.Leaks`: the key, any 12 bytes of it in a row, or its SHA-256 in hex) also runs at the end of **every** test that uses the rig, over the log at debug level, every response header and body, and every header the handler saw. The handler behind the rig logs and echoes everything it gets.
- [x] Config errors: a duplicate ID, a duplicate hash, a malformed hash, an invalid tenant, or a short key fails construction, with a message naming the entry ID. — `TestConfigErrors` (16 `NewAPIKeys` rows and 3 `KeyEntry` rows: each is a `*auth.ConfigError` whose `ID` is the entry and whose message names it, and none contains a key or hash; a duplicate hash written in upper case is still a duplicate).
- [x] `FuzzAuthorizationHeader`: never panics, never authenticates without a configured key. — `FuzzAuthorizationHeader` judges each input against an independent restatement: authenticated exactly when there is one header within the limit, `bearer` in any case, spaces, then exactly a configured key, and then as that key's tenant. `ErrNoCredentials` only with no header. It also runs every input through the middleware and the rig, so the invariants and the leak search apply. `make fuzz FUZZTIME=30s` was clean.
- [x] Mutation list recorded (below: 42 plants, none survives).

## Notes

### Files

- **`pkg/auth`** (new):
  - `doc.go`;
  - `auth.go`: `Principal`, `Credential`, `From`, `Authenticator`, `AuthenticatorFunc`, `Chain`, `BearerToken`, the errors;
  - `apikeys.go`: `APIKey`, `APIKeys`, `NewAPIKeys`, `KeyEntry`, `HashKey`, `ConfigError`;
  - `middleware.go`: `Middleware` and its options.
  - Tests:
    - `helpers_test.go`: the rig and its invariants;
    - `middleware_test.go`, `auth_test.go`, `apikeys_test.go`, `canary_test.go`, `fuzz_test.go`;
    - `lookup_internal_test.go`: white box, the one reason given in it;
    - `example_test.go`.
- **`pkg/auth/authtest`** (new): `fake.go` (`Fake`, `NewFake`, `Bearer`), `suite.go` (`RunAuthenticator`, `Subject`, `Credential`, `Leaks`, `Equal`), `fake_test.go` (the fake passes the suite).
- **`internal/archtest/rules.go`**: `"pkg/auth": {"pkg/tenancy"}`. The tree, `authtest` included, imports only tenancy from the module and no third-party package.
- **Not changed:** `pkg/tenancy`, `pkg/agentapi`, `cmd/agentkit`. No new dependency.

### Public API

```go
type Principal struct {
    Tenant   tenancy.Tenant
    User     string     // "" for a service credential
    Scopes   []string   // path.Match patterns: "agent:run", "admin:*", "*"
    Toolsets []string   // names or path.Match patterns; empty allows none
    Via      Credential // which credential authenticated: logged, never secret
}
type Credential struct{ Kind, ID string } // Kind: KindAPIKey ("api_key"); ID: the entry id
func (p Principal) AllowsToolset(name string) bool
func (p Principal) HasScope(s string) bool
func (p Principal) Clone() Principal
func From(ctx context.Context) (Principal, bool) // returns a copy

type Authenticator interface{ Authenticate(r *http.Request) (Principal, error) }
type AuthenticatorFunc func(r *http.Request) (Principal, error)
var ErrNoCredentials, ErrInvalidCredentials error
func Chain(as ...Authenticator) Authenticator
func BearerToken(h http.Header) (string, error)
const MaxAuthorizationLength = 8 << 10

func Middleware(a Authenticator, opts ...Option) func(http.Handler) http.Handler
func WithRealm(realm string) Option                    // default "agentkit"
func WithResourceMetadata(url string) Option           // adds resource_metadata="..." (T041)
func WithErrorWriter(f func(w http.ResponseWriter, r *http.Request, status int)) Option
func WithLogger(l *slog.Logger) Option
const RequestIDHeader = "X-Request-Id"

type APIKey struct { ID, Hash, Tenant, User string; Scopes, Toolsets []string } // json: id, hash, tenant, user, scopes, toolsets
func NewAPIKeys(entries []APIKey) (*APIKeys, error)
func (k *APIKeys) Authenticate(r *http.Request) (Principal, error)
func KeyEntry(e APIKey, key string) (APIKey, error) // hashes a raw key; rejects < MinKeyLength
func HashKey(key string) string                      // "sha256:<hex>"
const MinKeyLength = 16
type ConfigError struct{ ID string; Err error }

// package authtest
func NewFake(tokens map[string]auth.Principal) *Fake
func Bearer(r *http.Request, token string)
func RunAuthenticator(t *testing.T, newSubject func(t *testing.T) Subject)
type Subject struct{ Authenticator auth.Authenticator; Credentials []Credential }
type Credential struct{ Name, Secret string; Apply func(*http.Request, string); Want auth.Principal }
func Leaks(s, secret string) bool
func Equal(a, b auth.Principal) bool
```

### Decisions (made here; change them in the task that finds them wrong)

- **The short-key check needs the raw key, so it is `KeyEntry`.** Configuration holds only hashes, so a key's length is unknowable from it. `KeyEntry(e, key)` hashes a key given as a secret, which in practice means `AGENTKIT_API_TOKEN`. It rejects a key under 16 bytes or one outside visible ASCII, which no request could carry. `APIKey` has no raw-key field, so a key never sits in a config file. A key configured only by hash is as strong as whoever made it; `HashKey` is the documented way.
- **Token syntax: visible ASCII (`!`..`~`), not RFC 6750's narrower b64token.** Existing `AGENTKIT_API_TOKEN` values keep working: any `openssl rand -hex`, base64 or `dev-token-…` value does.
  - `Bearer` matches case-insensitively and may be followed by one or more spaces.
  - Refused: an empty token, a byte outside that range, several `Authorization` headers, and a header over 8 KiB. Each is `ErrInvalidCredentials`, so the 401 says `error="invalid_token"`.
  - With no `Authorization` header the answer is `ErrNoCredentials`.
  - A non-Bearer scheme is *invalid*, not "none": a credential was presented in the header we read, and it is not one we accept.
- **Lookup:** SHA-256 the presented token, then compare it with every entry's digest using `subtle.ConstantTimeCompare`, pick the match with `subtle.ConstantTimeSelect`, never break out. Duplicate hashes are refused at construction, so at most one entry matches. The time therefore depends only on the number of entries.
- **The middleware strips `Authorization`** from the request the handler gets, on a clone, so the caller's request is not changed. A careless handler that logs headers, or a proxy toolset that forwards them, cannot leak the credential, and nothing below can forward the caller's token (E07's "no token passthrough"). Only `Authorization` is stripped: `Cookie` stays, because other things need it.
- **Additions to the spec's `Principal`:** `Via Credential{Kind, ID}` for audit logs and for checks that depend on the credential kind (T061's CSRF check); `HasScope` takes patterns, so `admin:*` and `*` work. `From` returns a copy, and `APIKeys` copies the configured slices, so nothing handed out shares memory.
- **Request id:** a client's `X-Request-Id` is kept if it is 1 to 128 of `[A-Za-z0-9._:-]`, which is safe in a log line. Otherwise `crypto/rand.Text()`. Every response, 401s included, carries it.
- **A principal with no tenant is a 500**, not a pass: the middleware never calls a handler without a tenant, whatever an authenticator returns.
- **Failures:** 401 or 500, with the body from `WithErrorWriter`. The default is the status text as `text/plain`, `no-store`. 500 has no challenge.
  - Logged with request id, method, path and a reason that never holds the credential: debug for none, info for invalid, error for internal.
  - Success is logged at debug, with tenant, user and `via`.

### Invariants (checked on every request in the rig, `helpers_test.go`)

1. The handler runs exactly when the status is 200. Otherwise the status is 401 with a `Bearer realm=` challenge, or 500 with none.
2. When it runs, it has a principal with a tenant. `tenancy.From` is that tenant, `tenancy.User` is its user (absent when it has none), and `tenancy.Request` is the response's request id.
3. The handler never sees `Authorization`.
4. The caller's request headers are not changed.
5. Every response carries a request id, and a valid client id is kept.
6. When the test ends, no log line (debug level), response header or body, or header the handler saw contains a configured key, 12 bytes of one in a row, or its hash.
7. `testKeys` scribbles on the entries after building the authenticator, so every test also checks that the configuration was copied.

`RunAuthenticator` checks, for every call, that an error is one of the two classes, that a principal has a tenant, and that no error contains a secret.

### Mutation check

Planted one at a time with `go test -race -count=2 ./pkg/auth/...`, then restored (the tree was diffed against a backup afterwards). Five survived the first round, and each now has a general check:
- the dot and dash superstrings in the suite;
- whole-digest lengths in the white-box test;
- `TestBearerToken` for the length limit and control bytes, which no configured key can reach;
- scribbled config in `testKeys`.

| Planted bug | Caught by |
|---|---|
| **Prefix match accepted** (the fake compares with `HasPrefix`) | `TestFakeConformance` (`AlteredSecretNeverAuthenticates`: last byte dropped, half) |
| **Superstring accepted** (APIKeys cuts the token at the first `.`) | `TestAPIKeysConformance`, `TestChainConformance` (dot suffix), `FuzzAuthorizationHeader` (seed) |
| **Comparison not constant-time: early exit on the first match** | `TestLookupComparesEveryEntry` (counts the comparisons: every entry, whichever matches, and none) |
| **Comparison not constant-time: `bytes.Equal` in place of `subtle.ConstantTimeCompare`** | by inspection: `lookup` is the only comparison, and `Authenticate` and `NewAPIKeys` pass it `subtle.ConstantTimeCompare` |
| Only the first 8 bytes of the digest compared | `TestLookupComparesEveryEntry` (whole 32-byte digests) |
| **A header tenant trusted** (`X-Tenant` overrides the principal) | `TestKeysNeverCrossTenants` |
| A header user trusted (`X-User`) | `TestKeysNeverCrossTenants` |
| **`Chain` continues after invalid credentials** | `TestChain`, `TestChainConformance` |
| `Chain` stops on no credentials | `TestChain`, `TestChainConformance` |
| `Chain`: the last success wins | `TestChain`, `TestChainConformance` |
| Several `Authorization` headers: the first used | `TestRejectedRequestsGet401`, all three conformance runs, `FuzzAuthorizationHeader` |
| No length limit | `TestBearerToken` |
| `Bearer` matched case-sensitively | `TestValidKeyGivesConfiguredPrincipal`, `FuzzAuthorizationHeader` |
| Any scheme accepted | `TestRejectedRequestsGet401`, all three conformance runs, `FuzzAuthorizationHeader` |
| A header with no scheme taken as the token | `TestRejectedRequestsGet401`, all three conformance runs, `FuzzAuthorizationHeader` |
| A missing header is invalid (not "none") | `TestRejectedRequestsGet401`, `TestFailureOptions`, all three conformance runs, `FuzzAuthorizationHeader` |
| A wrong scheme is "none" (not invalid) | `TestRejectedRequestsGet401`, `FuzzAuthorizationHeader` |
| Control bytes allowed in the token | `TestBearerToken` |
| An unknown key is "none" (chain would go on) | `TestRejectedRequestsGet401`, `TestFailureOptions`, conformance, `FuzzAuthorizationHeader`, `Example` |
| The principal handed out without a copy | `TestAPIKeysConformance`, `TestChainConformance` (`PrincipalsDoNotShareMemory`) |
| The configured slices kept without a copy | `TestValidKeyGivesConfiguredPrincipal`, conformance (scribbled config) |
| A config error names the hash | `TestConfigErrors`, `TestCanaryKeyNeverLeaks` |
| Duplicate id allowed / duplicate hash allowed / short key allowed / malformed pattern allowed / error without the entry id | `TestConfigErrors` (each) |
| Empty toolsets allow all / pattern and name swapped / a malformed pattern matches | `TestAllowsToolset` (and `TestHasScope`, `Example` for the swap) |
| `Authorization` not stripped / stripped on the caller's request | `TestValidKeyGivesConfiguredPrincipal`, `TestKeysNeverCrossTenants`, `TestRequestID`, `TestCanaryKeyNeverLeaks`, `FuzzAuthorizationHeader` |
| The middleware logs the `Authorization` header | `TestCanaryKeyNeverLeaks`, `TestRejectedRequestsGet401`, `FuzzAuthorizationHeader` |
| No `error="invalid_token"` / no challenge when nothing was sent / realm not quoted | `TestRejectedRequestsGet401`, `TestFailureOptions`, `Example` |
| An internal error answered 401 / a principal with no tenant let through | `TestInternalErrorsAnswer500` |
| The handler called after a 401 | `TestRejectedRequestsGet401`, `TestCanaryKeyNeverLeaks`, `TestFailureOptions`, `FuzzAuthorizationHeader`, `Example` |
| The user not put in the context / the request id not put in it | `TestValidKeyGivesConfiguredPrincipal`, `TestKeysNeverCrossTenants`, `FuzzAuthorizationHeader` (rig invariants) |
| Any client request id trusted / a valid one ignored | `TestRequestID` (and the rig's check on every request) |

42 plants, none survives. The early-exit and `bytes.Equal` rows are the constant-time pair: one is shown by a test, the other by inspection of the one call site.

### For T040 (`agentkit serve` behind auth)

- Build one authenticator and wrap **both** `/v1/` and `/mcp/` with one `auth.Middleware`:
  - entries from config (`auth.keys: [{id, hash, tenant, user?, scopes, toolsets}]`, the `APIKey` JSON tags) via `NewAPIKeys`;
  - plus `AGENTKIT_API_TOKEN` via `KeyEntry(APIKey{ID: "env", Tenant: "default", Scopes: ["*"], Toolsets: ["*"]}, token)`.
  - Leave `/healthz`, `/readyz` and `/version` open.
- **Remove `agentapi.WithToken` from under the middleware.** The middleware strips `Authorization`, so an inner token check would refuse every request. `agentapi`'s own `auth` goes, or stays only for library users who don't mount `pkg/auth`.
- **`/v1` keeps T036's JSON errors** through `WithErrorWriter`. The writer must set only `Content-Type` and the body, writing `{"v":1,"error":{"code":"unauthorized"|"internal",…}}`. `WWW-Authenticate` and `X-Request-Id` are already set, so don't reset them. MCP can keep the default text body.
- **Per-principal toolsets:** `auth.From(ctx)` then `p.AllowsToolset(name)`.
  - A toolset that is not allowed answers 404 on `/mcp/{name}` and is left out of `GET /mcp/`.
  - The agent API intersects the agent's toolsets with the principal's.
  - Scopes for `/v1`: `p.HasScope("agent:run")` and similar. The env token has `*`.
- `server.WithMiddleware` (T018) carries context values into tool contexts, so tools see `tenancy.From` and `auth.From`.
- **`agentapi`'s in-memory runs and conversations are keyed by id only** (`Server.runs`, `Server.convs`).
  - Once requests carry tenants, `GET /v1/conversations/{id}` and `GET /v1/runs/{id}` must check the caller's tenant, as must continuing a conversation. Otherwise tenant B can read A's conversation by its id. The ids are random, so this is hard to exploit, but it is still a cross-tenant read.
  - Key the maps by (tenant, id), and answer 404 for another tenant's id, until T037 moves the API onto the tenant-scoped store.
- Work that starts without a request (resuming runs after a restart, E14's triggers) has no middleware. It sets `tenancy.With` from the tenant the store saved with the run (`store.TenantRun`).
- Startup fails off loopback with no authenticator (E07).
- The serve e2e test should rerun the canary search over the real binary's log and responses; `authtest.Leaks` is the helper.

### For T041 (JWT)

- **Put the JWT authenticator first:** `Chain(jwt, apiKeys)`.
  - The JWT authenticator answers `ErrNoCredentials` for a bearer token that is not shaped like a JWT (three dot-separated segments).
  - Otherwise `APIKeys` would call a JWT "invalid" and stop the chain, since it treats any bearer token it doesn't know as invalid.
  - API keys must then never look like JWTs; the keys we document (hex) don't.
- Use `auth.BearerToken` for the header, and `WithResourceMetadata` for RFC 9728.
- Run `authtest.RunAuthenticator`:
  - `Secret` is the whole token and `Apply` is `authtest.Bearer`.
  - The suite's one-byte flips move base64url digits by 32, into the high bits, so a lenient base64 decoder cannot ignore the change.
  - Prefix and suffix alterations only have to fail to authenticate; an invalid or no-credentials answer both pass.

### For T061 (local users and login)

- **The session authenticator is one more `Authenticator`:** `Chain(sessions, jwt, apiKeys)` or similar.
  - No session cookie: `ErrNoCredentials`.
  - An unknown or expired session: `ErrInvalidCredentials`.
  - A store failure: an internal error.
- **`Principal.Via`:**
  - Set `Via{Kind: "session", ID: <username>}`. **Never the session id**: `Via` is logged.
  - The CSRF check is middleware after `auth.Middleware`, applying only when `auth.From(ctx).Via.Kind == "session"`, so bearer requests are unaffected. It answers 403 itself; the auth middleware only does 401/500.
- **Roles to scopes:** `user` gets `["agent:run", "agent:read"]`; `admin` also gets `"admin:*"`. Check with `p.HasScope("admin:credentials")` and similar.
- `RunAuthenticator`, with `Apply` setting the cookie (`r.AddCookie`) and `Secret` the session id. The suite also sends hostile `Authorization` headers to it, which a cookie authenticator must not accept.
- The middleware strips only `Authorization`, so the handler still sees `Cookie`, which logout needs. A handler that logs cookies would leak the session: the canary test should cover that path.
- `golang.org/x/crypto` (argon2id) goes in archtest's `ThirdParty` for `pkg/auth` (T061's scope). It is not needed here.
