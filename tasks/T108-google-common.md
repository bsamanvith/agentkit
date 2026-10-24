---
id: T108
title: "connectors/google/common: the HTTP client, token sources, settings, paging and text limits the Google connectors share"
epic: E21
status: done
depends_on: [T049]
---

# T108: Google common package

## Context

Gmail, Calendar and Drive ([E21](E21-google-connectors.md)) all call Google's REST APIs with an OAuth access token, read the same settings, page the same way and fail the same ways. This package holds that once. It is a library: it makes no tools and is not run through the connector kit.

## Scope

- **`common.Client`** (`NewClient(ClientConfig)`, `Do`, `DoJSON`, `Path`): bearer token in one header, JSON bodies, per-attempt timeout, 8 MiB body limit, ids escaped as one path segment.
- **`common.TokenSource`** (`Token(ctx)`), `TokenSourceFunc`, optional `Invalidator` (a 401 invalidates and retries once), `EnvTokens`, `ErrNoToken`.
- **`common.Env`**: what a factory's options fill in (`HTTPClient`, `BaseURL`, `LookupEnv`, `Clock`, `Logger`, `Tokens`), `Defaults()` and `TokensFor`.
- **`common.Settings`** (`access_token_env`, `base_url`, `timeout`) with `Validate`, `RequestTimeout`, `CheckBaseURL`, `RefuseLiteralToken`.
- **Raw bodies:** `Request.Raw` sends bytes as they are (multipart uploads); a `Content-Type` or `Accept` in `Header` is kept (Drive exports and uploads; added at the Drive thread's request) — test: `TestRawBodyAndCallerHeadersAreKept`.
- **Errors:** `InputError`/`Invalid` for the model's mistakes, Google's error body parsed (message, reasons), rate and quota reasons recognised, the token redacted from any text taken from a response.
- **Paging and text:** `Paginate`, `ClampLimit`, `Truncate`, `TruncateContent` (rune boundary, marker says how to get more).
- **Tool helpers:** `Func` and `Checked` (limits merged into the input schema, `InputError` reported as `ErrInvalidInput`).
- **archtest:** `connectors/` is checked at any depth; `Rules.ConnectorLibraries` names `connectors/google/common` as not a connector.

## Out of scope

- Any tool. OAuth flows and token storage (T095, T096, T042); this package only consumes a token.
- Google batch endpoints and resumable uploads; a connector that needs them adds them here when it does.

## Acceptance criteria

- [x] **One header:** the token is sent as `Authorization: Bearer` and appears in no URL; an id with `/` or `?` stays one path segment — test: `common_test.TestRequestCarriesTheTokenInOneHeaderOnly`
- [x] **Error classes:** 400/422 are the model's (`InputError`, Google's message kept); 401 says to reconnect; 403 and 404 carry Google's message; 5xx the status only; none but a rate limit is retried — test: `TestFailuresAreClassed`
- [x] **No leak, capped:** the token is replaced in error text and the text is capped — test: `TestErrorTextNeverShowsTheTokenAndIsCapped`
- [x] **Rate limits:** a 429, or a 403 with a rate or quota reason, is retried once after `Retry-After` on the injected clock; not twice, not after a wait over 10 s, and the wait ends with the context — tests: `TestRateLimitIsRetriedOnceAfterRetryAfter`, `TestRateLimitIsNotRetriedTwiceOrAfterALongWait`, `TestRetryWaitStopsWithTheContext`
- [x] **Refresh:** a 401 with a refreshing source invalidates the token and retries once with the new one — test: `TestUnauthorizedRefreshesTheTokenOnce`
- [x] **No token:** a source with no account gives an error that says to connect one and sends nothing; another source failure hides its cause — tests: `TestNoTokenIsASayWhatToDoError`, `TestTokenSourceFailureHidesItsCause`
- [x] **Bounds:** a body over 8 MiB and malformed JSON are tool errors — tests: `TestOversizedBodyIsRefused`, `TestMalformedJSONIsAToolError`
- [x] **Paging:** limit, continuation token, errors, cancellation, and a server with endless empty pages — tests: `TestPaginate`, `TestPaginateStopsOnErrorCancellationAndEndlessPages`, `TestClampLimit`
- [x] **Text:** cut at a rune boundary with a marker that counts what was cut — test: `TestTruncateCutsAtARuneBoundaryAndSaysHowToGetMore`
- [x] **Settings:** defaults; a token pasted as a variable name, plain-http and credentialed base URLs, bad timeouts are refused without echoing the value; literal tokens refused with a pointer to `access_token_env` — tests: `TestSettings`, `TestLiteralTokensAreRefusedWithoutBeingEchoed`, `TestEnvTokens`
- [x] **Connector rules at depth:** a toolset package under a family directory is checked by the kit rules; a listed library is not — test: `archtest.TestConnectorRules`
- [x] **Mutation list** recorded (Notes).

## Notes

### For T109, T110, T111

- Build your factory like Tavily's, with `common.Env` behind your options (`WithHTTPClient`, `WithBaseURL`, `WithLookupEnv`, `WithClock`, `WithLogger`) and one more, `WithTokenSource`. In `Build`: `common.RefuseLiteralToken`, `toolset.DecodeSettings` into a struct that embeds `common.Settings`, `Validate`, `env.TokensFor(type, settings.AccessTokenEnv)`, `common.NewClient`. `Build` makes no request.
- Make tools with `common.Func`, return `common.Invalid(...)` for input the schema accepts but the API would refuse, and use `common.Path` for every id.
- If you need something every Google connector would (a new error reason, a helper for RFC 3339 times), add it here in a small separate PR, not in your package.

### Mutation list (2026-10-16)

Each planted bug made a test fail (a compile error does not count). Run with `go test ./connectors/google/...`.

| Planted bug | Caught by |
|---|---|
| 5xx treated as a rate limit (retried) | `TestConformance` (`errors.no_retry_5xx`) |
| token not replaced in error text | `TestErrorTextNeverShowsTheTokenAndIsCapped` |
| ids not escaped in `Path` | `TestRequestCarriesTheTokenInOneHeaderOnly` |
| `Retry-After` cap ignored (waits 60 s) | `TestRateLimitIsNotRetriedTwiceOrAfterALongWait` (fails in 5 s instead of hanging) |
| 401 not refreshed | `TestUnauthorizedRefreshesTheTokenOnce` |
| truncation by bytes, not runes | `TestTruncateCutsAtARuneBoundaryAndSaysHowToGetMore` |
| `Paginate` ignores the limit | `TestPaginate` |
| literal token accepted | `TestLiteralTokensAreRefusedWithoutBeingEchoed` |
| `http` base URL accepted for any host | `TestSettings`, kit `settings.plain_http_base_url` |
| 400 not the model's error | `TestFailuresAreClassed` |
| body limit off | `TestOversizedBodyIsRefused` |

### For T096 (connect API)

`common.TokenSource` is the seam. The adapter from `pkg/mcp/oauth` (the `google` profile) and the vault is a few lines: `Token(ctx)` asks the connector for the calling owner's access token (refreshing it) and maps "not connected" to `common.ErrNoToken`; `Invalidate(token)` marks that token stale so the next call refreshes. It belongs in T096, which also decides how the owner reaches the tool call (context value or per-owner toolset build). Until then `WithTokenSource` or `access_token_env` is the only way in.

### Decision: the kit and OAuth connectors

`RunConnector` needs no change for OAuth connectors. It checks that the secret named by `KeyEnv` goes in exactly one header and nowhere else, and a bearer access token is such a secret. The one thing it does not cover is the per-user `TokenSource` path (`WithTokenSource`): each connector tests that itself (Gmail: `TestTokenComesFromTheSourcePerCallAndNeverFromTheEnvironmentWhenGiven`). A kit check for it is worth adding when a second family of OAuth connectors exists.
