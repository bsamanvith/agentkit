//go:build live

package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/skill"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// TestLiveDeferredSkillLoad: real Claude, natively, loads a skill that lists
// a deferred tool and calls the tool. The skill_load result carries the
// skill's text and a tool_reference block in one tool_result, a shape the
// tool search docs do not show (their custom search returns references
// alone); this is the test that the API takes it.
func TestLiveDeferredSkillLoad(t *testing.T) {
	t.Parallel()
	requireCredentials(t)
	skills, err := skill.Load(fstest.MapFS{
		"s/logistics/SKILL.md": &fstest.MapFile{Data: []byte("---\nname: logistics\ndescription: Answer questions about shipments.\n" +
			"tools: [shipments_*]\n---\nCall shipments_list and report the shipments that are in transit.\n")},
	}, "s")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := skill.NewCatalog(skills...)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := cat.Toolset()
	if err != nil {
		t.Fatal(err)
	}
	var called atomic.Bool
	for _, name := range []string{"shipments_list", "shipments_archive", "invoices_list", "invoices_archive"} {
		tl, err := tool.Func(name, "Works on the company's "+strings.Split(name, "_")[0]+".", func(context.Context, struct{}) (string, error) {
			if name == "shipments_list" {
				called.Store(true)
				return `[{"id":"SHP-7","status":"in transit"}]`, nil
			}
			return "[]", nil
		}, tool.WithMetadata(tool.Metadata{Kind: tool.KindRead, Risk: tool.RiskLow}))
		if err != nil {
			t.Fatal(err)
		}
		tools = append(tools, tl)
	}
	reg, err := tool.NewRegistry(tools)
	if err != nil {
		t.Fatal(err)
	}
	model, err := newClaude(modelFlags{model: defaultModelID, effort: "low"})
	if err != nil {
		t.Fatal(err)
	}
	h, err := harness.New(model, reg,
		harness.WithMaxTurns(6),
		harness.WithSystem("You are an operations assistant. Before anything else, load the skill \"logistics\" with skill_load and follow it."),
		harness.WithDeferLoading(func(name string) bool { return strings.Contains(name, "_") && !strings.HasPrefix(name, "skill_") }),
		harness.WithNativeToolSearch(),
		harness.WithToolLoader(skill.LoadTool, cat.ToolsOf),
	)
	if err != nil {
		t.Fatal(err)
	}
	res, err := harness.Collect(h.Run(t.Context(), harness.Input{Message: harness.UserText("Which shipments are in transit?")}))
	if err != nil {
		t.Fatal(err) // a 400 here means the mixed tool_result is refused
	}
	var loaded []string
	for _, tr := range res.Record.Turns {
		loaded = append(loaded, tr.Loaded...)
	}
	if !slices.Contains(loaded, "shipments_list") {
		t.Errorf("loaded %v, want skill_load to have loaded shipments_list", loaded)
	}
	if !called.Load() {
		t.Error("Claude did not call shipments_list after loading the skill")
	}
}

// TestLiveDeferredTool: real Claude, given 30 deferred tools and a task only
// one of them does, finds it with tool_search and calls it: natively (the
// API expands the search's tool references) and emulated (the harness adds
// the found tool to the next request).
func TestLiveDeferredTool(t *testing.T) {
	t.Parallel()
	requireCredentials(t)
	for _, native := range []bool{true, false} {
		t.Run(fmt.Sprintf("native=%v", native), func(t *testing.T) {
			t.Parallel()
			var called atomic.Bool
			var tools []tool.Tool
			for _, topic := range []string{
				"invoices", "payroll", "tickets", "calendar", "contacts", "inventory",
				"shipments", "contracts", "expenses", "leads", "campaigns", "assets", "vendors", "timesheets", "budgets",
			} {
				for _, verb := range []string{"list", "archive"} {
					name := fmt.Sprintf("%s_%s", topic, verb)
					tl, err := tool.Func(name, fmt.Sprintf("%s the company's %s.", strings.ToUpper(verb[:1])+verb[1:], topic),
						func(context.Context, struct{}) (string, error) {
							if name == "shipments_list" {
								called.Store(true)
								return `[{"id":"SHP-7","status":"in transit","eta":"2026-10-24"}]`, nil
							}
							return "[]", nil
						}, tool.WithMetadata(tool.Metadata{Kind: tool.KindRead, Risk: tool.RiskLow}))
					if err != nil {
						t.Fatal(err)
					}
					tools = append(tools, tl)
				}
			}
			reg, err := tool.NewRegistry(tools)
			if err != nil {
				t.Fatal(err)
			}
			model, err := newClaude(modelFlags{model: defaultModelID, effort: "low"})
			if err != nil {
				t.Fatal(err)
			}
			opts := []harness.Option{
				harness.WithMaxTurns(6), harness.WithSystem("You are an operations assistant. Keep answers short."),
				harness.WithDeferLoading(func(string) bool { return true }),
			}
			if native {
				opts = append(opts, harness.WithNativeToolSearch())
			}
			h, err := harness.New(model, reg, opts...)
			if err != nil {
				t.Fatal(err)
			}
			res, err := harness.Collect(h.Run(t.Context(), harness.Input{Message: harness.UserText("Which of our shipments are in transit right now? Use the tools.")}))
			if err != nil {
				t.Fatal(err)
			}
			for _, tr := range res.Record.Turns {
				for _, c := range tr.Tools {
					t.Logf("turn %d: %s (error %v)", tr.Turn, c.Name, c.IsError)
				}
				if len(tr.Loaded) > 0 {
					t.Logf("turn %d loaded %v", tr.Turn, tr.Loaded)
				}
			}
			t.Logf("answer: %s", res.Final.Text())
			if !called.Load() {
				t.Error("Claude did not find and call shipments_list")
			}
		})
	}
}
