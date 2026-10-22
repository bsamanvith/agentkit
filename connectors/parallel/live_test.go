//go:build live

package parallel_test

// Live tests against the real Parallel API. They need PARALLEL_API_KEY and
// spend credits (one search, one extract); they run in sessions with
// `make test-live`, never in CI.

import (
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/connectors/parallel"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

func TestLive(t *testing.T) {
	if os.Getenv("PARALLEL_API_KEY") == "" {
		t.Skip("PARALLEL_API_KEY is not set")
	}
	tools, err := parallel.Factory(
		parallel.WithLookupEnv(os.LookupEnv),
		parallel.WithLogger(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))),
	).Build(

		t.Context(), toolset.BuildInput{Settings: nil})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]func(string) (json.RawMessage, error){}
	for _, tl := range tools {
		byName[tl.Definition().Name] = func(in string) (json.RawMessage, error) { return tl.Invoke(t.Context(), json.RawMessage(in)) }
	}

	t.Run("search", func(t *testing.T) {
		out, err := byName["parallel_search"](`{"query": "Go programming language release history", "objective": "Find the latest stable Go release and its date.", "max_results": 3}`)
		if err != nil {
			t.Fatal(err)
		}
		checkOutput(t, "parallel_search", out, 4000)
		var s struct {
			Results []struct{ URL, Content string }
		}
		if err := json.Unmarshal(out, &s); err != nil || len(s.Results) == 0 {
			t.Fatalf("output %s: want at least one result (%v)", out, err)
		}
		for _, r := range s.Results {
			if !strings.HasPrefix(r.URL, "http") || r.Content == "" {
				t.Errorf("result %+v: want a URL and excerpts", r)
			}
		}
		t.Logf("search: %s", out)
	})
	t.Run("extract", func(t *testing.T) {
		out, err := byName["parallel_extract"](`{"urls": ["https://go.dev/doc/devel/release"], "objective": "Which Go releases are listed?"}`)
		if err != nil {
			t.Fatal(err)
		}
		checkOutput(t, "parallel_extract", out, 20000)
		var e struct {
			Results []struct{ URL, Content string }
		}
		if err := json.Unmarshal(out, &e); err != nil || len(e.Results) != 1 || !strings.Contains(e.Results[0].Content, "go1") {
			t.Fatalf("output %s: want the page's content mentioning Go releases (%v)", out, err)
		}
	})
}
