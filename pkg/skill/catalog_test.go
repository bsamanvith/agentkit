package skill_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/skill"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// fixtures are the skills of the golden catalog: testdata/skills and
// one with a literal multi-line description.
func fixtures(t *testing.T) []skill.Skill {
	t.Helper()
	repo := load(t, os.DirFS("testdata"), "skills")
	extra := load(t, fstest.MapFS{"s/zeta/SKILL.md": md("name: alpha-notes\ndescription: |\n  Two lines\n  of description.", "BODY-ALPHA")}, "s")
	return slices.Concat(repo, extra)
}

// TestSystemTextStable: the catalog text is byte-identical across loads and
// whatever order the skills come in, holds every name and description and
// no instructions.
func TestSystemTextStable(t *testing.T) {
	t.Parallel()
	skills := fixtures(t)
	first, err := skill.NewCatalog(skills...)
	if err != nil {
		t.Fatal(err)
	}
	text := first.SystemText()
	testkit.Golden(t, filepath.Join("testdata", "catalog.golden.txt"), []byte(text))

	again, err := skill.NewCatalog(fixtures(t)...)
	if err != nil {
		t.Fatal(err)
	}
	if again.SystemText() != text {
		t.Error("a second load gives another text")
	}
	reversed := slices.Clone(skills)
	slices.Reverse(reversed)
	rotated := append(slices.Clone(skills[1:]), skills[0])
	for _, order := range [][]skill.Skill{reversed, rotated} {
		c, err := skill.NewCatalog(order...)
		if err != nil {
			t.Fatal(err)
		}
		if c.SystemText() != text {
			t.Errorf("order %v gives another text", names(order))
		}
	}
	for _, s := range skills {
		if !strings.Contains(text, "- "+s.Name+": ") {
			t.Errorf("text lacks %s", s.Name)
		}
		if body := strings.TrimSpace(s.Body); strings.Contains(text, body[:min(len(body), 60)]) {
			t.Errorf("text holds the instructions of %s", s.Name)
		}
	}
	empty, _ := skill.NewCatalog()
	if empty.SystemText() != "" {
		t.Errorf("empty catalog: %q", empty.SystemText())
	}
}

// TestEndToEnd: a harness run with the catalog in the system prompt and the
// toolset; the model (scripted) sees only the catalog, loads a skill, reads
// one of its files and gets both.
func TestEndToEnd(t *testing.T) {
	t.Parallel()
	skills := fixtures(t)
	cat, err := skill.NewCatalog(skills...)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := cat.Toolset()
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tool.NewRegistry(tools)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := cat.Get("release-notes")
	if err != nil {
		t.Fatal(err)
	}
	ref, err := wt.Read("references/template.md")
	if err != nil {
		t.Fatal(err)
	}
	model := llmtest.NewFake(t,
		llmtest.ToolCalls(llm.ToolUse{ID: "1", Name: skill.LoadTool, Input: json.RawMessage(`{"name":"release-notes"}`)}),
		llmtest.ToolCalls(llm.ToolUse{ID: "2", Name: skill.ReadTool, Input: json.RawMessage(`{"name":"release-notes","path":"references/template.md"}`)}),
		llmtest.Text("done"),
	)
	system := "You write Go.\n\n" + cat.SystemText()
	h, err := harness.New(model, reg, harness.WithSystem(system))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.Collect(h.Run(t.Context(), harness.Input{Message: harness.UserText("Write release notes for v1.2: added skills.")})); err != nil {
		t.Fatal(err)
	}
	reqs := model.Requests()
	if len(reqs) != 3 {
		t.Fatalf("%d model calls, want 3", len(reqs))
	}
	if reqs[0].System != system {
		t.Errorf("system prompt %q, want the catalog", reqs[0].System)
	}
	for _, s := range skills {
		if body := strings.TrimSpace(s.Body); strings.Contains(reqs[0].System, body[:min(len(body), 60)]) {
			t.Errorf("the system prompt holds the instructions of %s", s.Name)
		}
	}
	var defs []string
	for _, d := range reqs[0].Tools {
		defs = append(defs, d.Name)
	}
	if !slices.Equal(defs, []string{skill.LoadTool, skill.ReadTool}) {
		t.Errorf("tools %v", defs)
	}
	var loaded struct {
		Instructions string
		Files        []skill.File
	}
	if err := json.Unmarshal([]byte(lastResult(t, reqs[1])), &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.Instructions != wt.Body || !slices.Contains(loaded.Files, skill.File{Path: "references/template.md", Size: int64(len(ref))}) {
		t.Errorf("skill_load gave %.80q with files %v", loaded.Instructions, loaded.Files)
	}
	var read struct{ Content string }
	if err := json.Unmarshal([]byte(lastResult(t, reqs[2])), &read); err != nil {
		t.Fatal(err)
	}
	if read.Content != string(ref) {
		t.Errorf("skill_read gave %.80q", read.Content)
	}
}

// lastResult is the content of the tool result in a request's last message.
func lastResult(t *testing.T, req llm.Request) string {
	t.Helper()
	last := req.Messages[len(req.Messages)-1]
	for _, b := range last.Content {
		if r, ok := b.(llm.ToolResult); ok {
			if r.IsError {
				t.Fatalf("tool error: %s", r.Content)
			}
			return r.Content
		}
	}
	t.Fatalf("no tool result in %v", last)
	return ""
}
