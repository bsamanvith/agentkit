# Exa's OpenAPI spec (slice)

`exa.json` is the part of Exa's public OpenAPI spec this connector uses:
`POST /search` and `POST /contents` (request body schema and 200 response
schema) and every component schema they reference. Nothing is edited by hand.

- Source: https://exa.ai/docs/exa-spec.yaml (OpenAPI 3.1.0, "Exa Public API" 2.0.0)
- Fetched: 2026-10-12

`spec_test.go` checks every cassette's request and response against it, and
that the connector's search types and categories are values the spec accepts.

To refresh it, re-cut and review the diff; then run the tests, which fail on
any schema keyword the checker does not support:

```sh
curl -sSL https://exa.ai/docs/exa-spec.yaml | go run ./connectors/exa/testdata/openapi/cut.go > connectors/exa/testdata/openapi/exa.json
```
