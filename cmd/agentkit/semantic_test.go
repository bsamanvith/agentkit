//go:build !noagent

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/memory/memorytest"
)

// embeddingServer is an OpenAI-shaped embeddings endpoint answering with
// memorytest.FakeVector, and how many texts it embedded.
func embeddingServer(t *testing.T) (string, *atomic.Int64) {
	t.Helper()
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input      []string `json:"input"`
			Dimensions int      `json:"dimensions"`
		}
		if r.URL.Path != "/v1/embeddings" || json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, `{"error": {"message": "bad request"}}`, http.StatusBadRequest)
			return
		}
		type datum struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		var data []datum
		for i, text := range req.Input {
			data = append(data, datum{i, memorytest.FakeVector(text, 64)})
		}
		n.Add(int64(len(req.Input)))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v1", &n
}

// TestSemanticRecallThroughTheAPI: an agent whose index has an embedder
// (an OpenAI-compatible server) saves a note and finds it by meaning, with
// no word in common, through recall_search in its default (hybrid) mode;
// after the config names another model, `agentkit memory reembed` embeds
// the note again.
func TestSemanticRecallThroughTheAPI(t *testing.T) {
	t.Parallel()
	base, embedded := embeddingServer(t)
	db := filepath.Join(t.TempDir(), "memories.db")
	config := func(model string) string {
		return `{"agent": {"model": {"id": "m"}, "memory": {"index": {"backend": "sqlite", "path": ` + strconv.Quote(db) + `,
		  "embedder": {"type": "openai", "model": "` + model + `", "dimensions": 64, "base_url": ` + strconv.Quote(base) + `}}}}}`
	}
	fake := llmtest.NewFake(t,
		recallCall("save", memory.RecallSaveTool, `{"text":"I always take an aisle seat on planes."}`),
		llmtest.Text("Noted."),
		recallCall("find", memory.RecallSearchTool, `{"query":"flight preferences"}`),
		llmtest.Text("Aisle."),
	)
	s, err := startAgents(t, config("local-embed"), map[string]*llmtest.Fake{"m": fake})
	if err != nil {
		t.Fatal(err)
	}
	c := s.client(t, "")
	for _, msg := range []string{"Remember my seat.", "Where do I sit?"} {
		if _, err := c.Run(t.Context(), agentclient.RunRequest{Message: msg}); err != nil {
			t.Fatal(err)
		}
	}
	out, ok := toolResult(fake.Requests()[3], "find")
	if !ok || !strings.Contains(out, "aisle seat") {
		t.Errorf("recall_search by meaning: %q, ok %v", out, ok)
	}
	if embedded.Load() != 2 {
		t.Errorf("embedded %d texts, want 2 (the note, the query)", embedded.Load())
	}

	path := filepath.Join(t.TempDir(), "agentkit.json")
	if err := os.WriteFile(path, []byte(config("local-embed-2")), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	cli := cli{stdout: &stdout, stderr: &stderr, getenv: func(string) string { return "" }}
	if code := cli.main(t.Context(), []string{"memory", "reembed", "-config", path}); code != 0 {
		t.Fatalf("memory reembed: exit %d: %s", code, stderr.String())
	}
	if want := "agent.memory.index: 1 items re-embedded with local-embed-2 (64 dimensions)\n"; stdout.String() != want {
		t.Errorf("memory reembed printed %q, want %q", stdout.String(), want)
	}
	stdout.Reset()
	if code := cli.main(t.Context(), []string{"memory", "reembed", "-config", path}); code != 0 || !strings.Contains(stdout.String(), ": 0 items") {
		t.Errorf("a second reembed: exit %d, %q", code, stdout.String())
	}
}

// TestSemanticConfigErrors: a bad embedder or mode fails startup, naming it.
func TestSemanticConfigErrors(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ index, want string }{
		{`"embedder": {"type": "cohere", "model": "x"}`, `agent.memory.index.embedder: type "cohere"`},
		{`"embedder": {"type": "voyage", "model": "voyage-3.5"}`, "needs a key: set $VOYAGE_API_KEY"},
		{`"embedder": {"type": "openai", "model": "mystery"}`, "give its dimensions"},
		{`"embedder": {"type": "openai", "model": "m", "dimensions": 8, "base_url": "http://example.com"}`, "want https"},
		{`"modes": ["semantic"]`, `mode "semantic" needs an embedder`},
		{`"modes": ["fuzzy"]`, `mode "fuzzy"`},
	} {
		config := `{"agent": {"model": {"id": "m"}, "memory": {"index": {"backend": "memory", ` + c.index + `}}}}`
		_, err := startAgents(t, config, map[string]*llmtest.Fake{"m": llmtest.NewFake(t)})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("index %s: %v, want an error with %q", c.index, err, c.want)
		}
	}
}

// TestMemoryReembedUsage: no embedder, or no command, is an error.
func TestMemoryReembedUsage(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "agentkit.json")
	if err := os.WriteFile(path, []byte(`{"agent": {"model": {"id": "m"}, "memory": {"index": {"backend": "memory"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	c := cli{stdout: &stdout, stderr: &stderr, getenv: func(string) string { return "" }}
	if code := c.main(t.Context(), []string{"memory"}); code != 2 {
		t.Errorf("memory with no command: exit %d, want 2", code)
	}
	if code := c.main(t.Context(), []string{"memory", "reembed", "-config", path}); code != 1 || !strings.Contains(stderr.String(), "no agent of the config has memory.index.embedder") {
		t.Errorf("reembed with no embedder: exit %d, %q", code, stderr.String())
	}
}
