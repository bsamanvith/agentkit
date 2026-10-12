// Package toolsettest is the test kit for connectors (toolset factories that
// call a provider's API): recorded upstreams, a conformance suite every
// connector passes with one call, and honest fakes for every other test, so no
// test outside `make test-live` ever pays for an API call.
//
// # Recorded upstreams
//
// A connector's tests talk to an [Upstream], an httptest server that answers
// from cassettes in testdata/cassettes: <name>.req.json holds the request
// (method, path, query, JSON body; never headers) and <name>.res.http the
// response as `curl -i` prints it. Cassettes are recorded from the real API by
// [Record] (`make record-cassettes`) and never edited by hand. The only HTTP
// client the kit hands out reaches the upstream and refuses every other host.
//
// # The conformance suite
//
// [RunConnector] checks what every connector must do: definitions, strict
// settings, key handling, the error matrix, the retry rule, body limits, that
// the key leaks nowhere, no network, MCP and concurrency:
//
//	func TestConformance(t *testing.T) { toolsettest.RunConnector(t, connector()) }
//
// A connector's own tests cover only what is specific to it: how it maps
// requests and responses, and the provider's quirks.
//
// # Fakes
//
// Everything else (the agent, the API, flows, the binary) uses a [Fake]: the
// connector's real definitions ([Definitions]), inputs and scripted outputs
// validated against the real schemas, and a test failure for any call with no
// script. [FakeFactory] puts one in a toolset.Catalog under the real type;
// [Replay] serves the real connector from its cassettes instead. [RunFake]
// runs the applicable parts of the suite against a fake, so it cannot drift.
//
// docs/CONNECTORS.md is the guide.
package toolsettest
