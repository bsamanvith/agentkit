# Connector auth

A connector never holds a secret in config. Settings *name* where the secret is; the factory's options say *how* to get it. Which of the two below applies is a property of the API: a key the operator owns, or an account a user owns.

## A. Deployment key (search APIs: Tavily, Exa, Parallel)

- Settings: `api_key_env` (default `<PROVIDER>_API_KEY`). Read once in `Build` through `WithLookupEnv`; `cmd/agentkit/catalog.go` passes `os.LookupEnv`, tests pass a map.
- Missing variable: `Build` fails with an error naming the variable, never a value. In config the toolset is `"optional": true`, so the server starts, skips it, and `/readyz` says `degraded` and names the variable.
- Sent in exactly one header (`Authorization: Bearer`, `X-Api-Key`, whatever the provider takes). The kit's `canary` and key-placement checks fail if it reaches a URL, body, output, error or log line.
- A connector that also works without a key (Tavily's `auth: auto|key|keyless`) sets `KeyOptional` and `KeylessMarker` in the kit.
- Tell `/v1/toolsets` about it: one entry in `credentialEnv` in `cmd/agentkit/agent.go` (until factories report it themselves, the TODO there).

## B. Per-user OAuth token (Google, Microsoft, Salesforce, Slack, ...)

The tools act for whoever is calling, so the token is fetched per request, not per toolset. The pattern is `connectors/google/common` (`tasks/T108-google-common.md`; PR #23 until it merges):

```go
// In common (or the single package): what a connector asks for.
type TokenSource interface{ Token(ctx context.Context) (string, error) }
type Invalidator interface{ Invalidate(token string) } // optional: a 401 invalidates and retries once
var ErrNoToken = errors.New("no Google account is connected")  // "connect your account", sends nothing

// In the toolset package: one more option than a keyed connector.
func WithTokenSource(s common.TokenSource) Option { return func(f *factory) { f.env.Tokens = s } }

// In Build:
if err := common.RefuseLiteralToken(Type, raw); err != nil { return nil, err }
var s settings // embeds common.Settings: access_token_env, base_url, timeout
if err := toolset.DecodeSettings(raw, &s); err != nil { return nil, err }
if err := s.Validate(Type); err != nil { return nil, err }
tokens, err := f.env.TokensFor(Type, s.AccessTokenEnv) // injected source, else the env variable
```

- **Without a source** (development, or one operator-owned account), `access_token_env` (default `GOOGLE_ACCESS_TOKEN`) names a variable holding a static access token. It expires in about an hour; after that calls fail with "google rejected the access token" until restart. This is also what the kit uses as `KeyEnv`.
- **With a source**, the source decides whose token from `ctx`. The client asks for a token on every request, so the source caches and refreshes.
- **Scopes:** each package exports the scopes its tools need (narrowest that works: `*.readonly` unless a write setting is on), so the connect flow asks for exactly those. Restricted scopes (Gmail, full Drive) need the provider's app verification for outside users; note it in the task.
- **Errors:** 401 → `ErrToolFailed` saying to reconnect; a source failure other than `ErrNoToken` hides its cause (it may name a store or carry a token).

### Where per-user tokens come from: `pkg/mcp/oauth`

`pkg/mcp/oauth` (T095) is the one OAuth core; read its `doc.go`. What a connector author needs from it:

- **`Profile`** is how a provider differs from the MCP spec, as data: a pre-registered `ClientID`/`ClientSecret`, fixed `AuthURL`/`TokenURL`, `Scopes`, `RefreshScope`, `AuthParams`, `OmitResource`, `DefaultTTL`, `KeepExtras`, and one code hook, `OnToken`. `oauth.Profiles()` has `mcp`, `google`, `microsoft`, `salesforce`. A new provider is a new preset, not new flow code.
- **`Connector.Begin` / `Complete`** run the browser flow in two requests (the console's callback page posts code and state). **`Connector.Token(ctx, owner, srv)`** returns the owner's fresh `Secret`, refreshing once across concurrent callers; errors are `ErrNotConnected`, `ErrReconnectNeeded`, `ErrNeedsMoreAccess`.
- **`Secret`** prints, logs and marshals as `[redacted]`; only `Reveal()` gives the value. Keep it a `Secret` until the line that sets the header.
- **Owner:** `oauth.OwnerFrom(ctx, srv)` reads the principal (tenant + user) from the request context; a tenant-scoped server (`ScopeTenant`) shares one connection per tenant.

The bridge from the core to a REST connector is a `TokenSource` over `Connector.Token`. It is wired by the connect API (T096) and the vault (T042); until they land, connectors take the option and tests and local runs use `access_token_env`. Its shape:

```go
src := common.TokenSourceFunc(func(ctx context.Context) (string, error) {
	owner, err := oauth.OwnerFrom(ctx, srv) // srv: oauth.Server{Name: "gmail", Profile: oauth.Profiles()["google"]}
	if err != nil { return "", err }
	tok, err := conn.Token(ctx, owner, srv)
	if errors.Is(err, oauth.ErrNotConnected) || errors.Is(err, oauth.ErrReconnectNeeded) { return "", common.ErrNoToken }
	if err != nil { return "", err }
	return tok.Reveal(), nil
})
```

### Testing auth

- The kit with `KeyEnv` set to the token variable covers placement, leaks, the 401 class and missing-token errors.
- The family's own tests cover the source: a 401 with an `Invalidator` retries once with the new token; `ErrNoToken` sends nothing; a failing source does not leak its cause (see `common_test.go`).
- The OAuth flow itself is tested in `pkg/mcp/oauth` against `oauthtest`'s strict fake authorization server and protected resource (PKCE S256, resource, single-use codes, CIMD). A new `Profile` preset gets a case in `TestProfiles`; never call a real authorization server from a unit test.
- For a local token: the provider's OAuth playground, or `gcloud auth application-default login --scopes=<scopes>` then `export GOOGLE_ACCESS_TOKEN=$(gcloud auth application-default print-access-token)`. Never commit or log it; recording scrubs it, and `TestNoSecretsInCassettes` fails on one that slipped through.
