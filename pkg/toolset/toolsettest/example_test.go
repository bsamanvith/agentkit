package toolsettest_test

// The code of docs/CONNECTORS.md, compiled: a connector's test file, its live
// tests, and the fakes other tests use. The connector is the toy; for a real
// one, replace toy with its package.

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest/internal/toy"
)

// newFactory wires the connector to the suite's Env.
func newFactory(e toolsettest.Env) toolset.Factory {
	return toy.New(
		toy.WithBaseURL(e.BaseURL), // "" means the default
		toy.WithHTTPClient(e.HTTPClient),
		toy.WithLookupEnv(e.LookupEnv),
		toy.WithClock(e.Clock),
		toy.WithLogger(e.Logger),
	)
}

// connector describes the connector to the suite.
func connector() toolsettest.Connector {
	return toolsettest.Connector{
		New:      newFactory,
		Settings: json.RawMessage(`{"api_key_env": "TOY_API_KEY"}`),
		KeyEnv:   "TOY_API_KEY",
		Calls: []toolsettest.Call{
			{Name: "search_go", Tool: "toy_search", Input: json.RawMessage(`{"query": "go", "max_results": 2}`), Cassettes: []string{"search_go"}},
			{Name: "get_go1", Tool: "toy_get", Input: json.RawMessage(`{"id": "go1"}`), Cassettes: []string{"get_go1"}},
		},
		Invalid: []toolsettest.Call{
			// Inputs the JSON schema accepts but the connector must refuse.
			{Name: "max_results_too_big", Tool: "toy_search", Input: json.RawMessage(`{"query": "go", "max_results": 99}`)},
			{Name: "bad_id", Tool: "toy_get", Input: json.RawMessage(`{"id": "../admin"}`)},
		},
		ErrorBody: toy.ErrorBody,
		Skip: map[toolsettest.CheckID]string{
			"errors.422": "the toy API never answers 422", // listed in docs/CONNECTORS.md
		},
	}
}

func ExampleRunConnector() {
	// func TestConformance(t *testing.T) { toolsettest.RunConnector(t, connector()) }
	_ = func(t *testing.T) { toolsettest.RunConnector(t, connector()) }
}

func ExampleFuzzResponses() {
	// func FuzzSearchResponses(f *testing.F) { toolsettest.FuzzResponses(f, connector(), "toy_search") }
	_ = func(f *testing.F) { toolsettest.FuzzResponses(f, connector(), "toy_search") }
}

func ExampleRecord() {
	// //go:build live
	// func TestRecordCassettes(t *testing.T) { ... }
	_ = func(t *testing.T) {
		toolsettest.SkipUnlessRecording(t)
		toolsettest.Record(t, connector(), toolsettest.Recording{
			BaseURL:   toy.DefaultBaseURL,
			LookupEnv: os.LookupEnv,
		})
	}
}

func ExampleDrift() {
	// //go:build live
	// func TestDrift(t *testing.T) { ... }
	_ = func(t *testing.T) {
		toolsettest.Drift(t, connector(), toolsettest.Recording{BaseURL: toy.DefaultBaseURL, LookupEnv: os.LookupEnv})
	}
}

func ExampleNewFake() {
	_ = func(t *testing.T) {
		defs := toolsettest.Definitions(t, newFactory, json.RawMessage(`{"api_key_env": "TOY_API_KEY"}`))
		fake := toolsettest.NewFake(t, defs,
			toolsettest.Golden("toy_search", "testdata/golden/search_go.json"),
			toolsettest.Sequence(
				toolsettest.Output("toy_get", map[string]string{"id": "go1", "title": "Go", "content": "Go."}),
				toolsettest.Error("toy_get", tool.ErrToolFailed),
			),
		)
		_ = fake // a tool.Registry: hand it to the harness, the agent API, ...
		_ = fake.Calls()
	}
}

func ExampleFakeFactory() {
	_ = func(t *testing.T) {
		defs := toolsettest.Definitions(t, newFactory, nil)
		search := toolsettest.FakeFactory(t, "toy", defs,
			toolsettest.OutputFunc("toy_search", func(_ context.Context, input json.RawMessage) (any, error) {
				return map[string]any{"results": []any{}}, nil
			}))
		catalog, err := toolset.NewCatalog(toolset.Demo(), search)
		_, _ = catalog, err // build config against it: cmd/agentkit tests set cli.catalog
	}
}

func ExampleReplay() {
	_ = func(t *testing.T) {
		tools := toolsettest.Replay(t, connector()) // the real connector, answered from its cassettes
		_ = tools
	}
}
