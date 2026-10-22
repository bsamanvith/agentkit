//go:build !noconnectors

package main

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/knowledge"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// TestKnowledgeAgent: an agent with a "knowledge" toolset, whose root is
// relative to the config file's folder, searches it through the agent API
// and gets the passage that answers. The catalog is serve's own.
func TestKnowledgeAgent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	kb := filepath.Join(dir, "docs")
	if err := os.Mkdir(kb, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := "# Returns\n\n## Restocking fee\n\nOpened e-bikes carry a 12% restocking fee.\n\n## Window\n\n45 days.\n"
	if err := os.WriteFile(filepath.Join(kb, "returns.md"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	config := `{"mcp": {"toolsets": [{"name": "handbook", "type": "knowledge", "settings": {"root": "docs"}}]},
	  "agents": [{"name": "support", "model": {"id": "m"}, "toolsets": ["handbook"]}]}`
	fake := llmtest.NewFake(t,
		llmtest.ToolCalls(llm.ToolUse{ID: "s1", Name: knowledge.SearchTool, Input: json.RawMessage(`{"query": "restocking fee"}`)}),
		llmtest.Text("Opened e-bikes carry a 12% restocking fee (returns.md)."),
	)
	s, err := startAgentsIn(t, dir, config, map[string]*llmtest.Fake{"m": fake}, func() (*toolset.Catalog, error) {
		return newCatalog(slog.New(slog.DiscardHandler), dir)
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.client(t, "").Run(t.Context(), agentclient.RunRequest{Agent: "support", Message: "Is there a restocking fee?"})
	if err != nil {
		t.Fatal(err)
	}
	out, ok := toolResult(fake.Requests()[1], "s1")
	var found knowledge.SearchOutput
	if err := json.Unmarshal([]byte(out), &found); err != nil || !ok || len(found.Results) != 1 ||
		found.Results[0].Section != "Returns > Restocking fee" || !strings.Contains(found.Results[0].Text, "12% restocking fee") {
		t.Errorf("search result %q, ok %v; want the restocking fee passage", out, ok)
	}
	if !strings.Contains(res.Answer, "12%") {
		t.Errorf("answer %q", res.Answer)
	}
}

// TestSampleKnowledgeBase: the sample handbook of deploy/local/agent, as
// search.json and deploy/k8s/base name it, builds, and answers the questions
// the smoke suite and the evals ask.
func TestSampleKnowledgeBase(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "deploy", "local", "agent", "search.json")
	cfg, err := loadServeConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	var specs []toolset.Spec
	for _, s := range cfg.MCP.Toolsets {
		if s.Name == "handbook" {
			s.Optional = false // here it must build
			specs = append(specs, s)
		}
	}
	if len(specs) != 1 {
		t.Fatalf("search.json has %d handbook toolsets, want 1", len(specs))
	}
	catalog, err := newCatalog(slog.New(slog.DiscardHandler), configDir(path))
	if err != nil {
		t.Fatal(err)
	}
	regs, _, err := toolset.Build(t.Context(), catalog, specs)
	if err != nil {
		t.Fatal(err)
	}
	for query, source := range map[string]string{
		"restocking fee for an opened e-bike": "returns.md",
		"What does error E07 mean?":           "error-codes.md",
		"battery warranty charge cycles":      "warranty.md",
		"express delivery cost":               "shipping.md",
		"customer mentions an injury":         "escalation.md",
	} {
		b, err := regs["handbook"].Invoke(t.Context(), knowledge.SearchTool, json.RawMessage(`{"query": `+strconvQuote(query)+`, "k": 1}`))
		if err != nil {
			t.Fatalf("%q: %v", query, err)
		}
		var out knowledge.SearchOutput
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
		if len(out.Results) != 1 || out.Results[0].Source != source {
			t.Errorf("%q: %+v, want %s", query, out.Results, source)
		}
	}
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
