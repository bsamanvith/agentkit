package harness_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
)

// svcToolset puts svc_<topic>_* in toolset <topic>; now and echo are in none.
func svcToolset(name string) string {
	rest, ok := strings.CutPrefix(name, "svc_")
	if !ok {
		return ""
	}
	topic, _, _ := strings.Cut(rest, "_")
	return topic
}

func choiceInput(text string, c *harness.ToolChoice) harness.Input {
	in := prompt(text)
	in.Tools = c
	return in
}

// A choice of toolsets sends only those toolsets' tools, and the tools of
// no toolset.
func TestToolChoiceKeepsChosenToolsets(t *testing.T) {
	leakcheck(t)
	fake := llmtest.NewFake(t, llmtest.Text("ok"), llmtest.Text("ok"))
	h := newHarness(t, fake, manyTools(t, &ran{}), harness.WithToolsets(svcToolset))
	if _, _, err := run(t, h, choiceInput("hi", &harness.ToolChoice{Toolsets: []string{"crm", "email"}})); err != nil {
		t.Fatal(err)
	}
	want := []string{"echo", "now", "svc_crm_lookup", "svc_crm_update", "svc_email_lookup", "svc_email_update"}
	got := names(fake.Requests()[0].Tools)
	slices.Sort(got)
	diff(t, "tools", want, got)

	// An empty list keeps only the tools of no toolset.
	if _, _, err := run(t, h, choiceInput("hi", &harness.ToolChoice{Toolsets: []string{}})); err != nil {
		t.Fatal(err)
	}
	got = names(fake.Requests()[1].Tools)
	slices.Sort(got)
	diff(t, "tools with no toolset chosen", []string{"echo", "now"}, got)
}

// search defers the chosen toolsets' tools behind tool_search, emulated
// and native, and a search loads them.
func TestToolChoiceSearchDefers(t *testing.T) {
	leakcheck(t)
	choice := &harness.ToolChoice{Toolsets: []string{"crm", "weather"}, Search: harness.SearchDeferred}
	t.Run("emulated", func(t *testing.T) {
		r := &ran{}
		fake := llmtest.NewFake(t,
			llmtest.ToolCalls(use("c1", harness.SearchTool, `{"query":"crm lookup","max_results":1}`)),
			llmtest.ToolCalls(use("c2", "svc_crm_lookup", `{}`)),
			llmtest.Text("done"),
		)
		h := newHarness(t, fake, manyTools(t, r), harness.WithToolsets(svcToolset))
		if _, _, err := run(t, h, choiceInput("find a customer", choice)); err != nil {
			t.Fatal(err)
		}
		reqs := fake.Requests()
		diff(t, "first request", []string{"echo", "now", harness.SearchTool}, names(reqs[0].Tools))
		diff(t, "after the search", []string{"echo", "now", harness.SearchTool, "svc_crm_lookup"}, names(reqs[1].Tools))
		diff(t, "ran", []string{"svc_crm_lookup"}, r.list())
	})
	t.Run("native", func(t *testing.T) {
		fake := llmtest.NewFake(t, llmtest.Text("ok"))
		h := newHarness(t, fake, manyTools(t, &ran{}), harness.WithToolsets(svcToolset), harness.WithNativeToolSearch())
		if _, _, err := run(t, h, choiceInput("hi", choice)); err != nil {
			t.Fatal(err)
		}
		req := fake.Requests()[0]
		diff(t, "deferred", []string{"svc_crm_lookup", "svc_crm_update", "svc_weather_lookup", "svc_weather_update"}, req.Deferred)
		if len(req.Tools) != 7 {
			t.Errorf("tools = %v, want now, echo, tool_search and the four deferred", names(req.Tools))
		}
	})
}

// direct sends every chosen tool even when the harness defers them.
func TestToolChoiceDirectOverridesAgentDefer(t *testing.T) {
	leakcheck(t)
	fake := llmtest.NewFake(t, llmtest.Text("ok"), llmtest.Text("ok"))
	h := newHarness(t, fake, manyTools(t, &ran{}), harness.WithToolsets(svcToolset), harness.WithDeferLoading(deferSvc))
	if _, _, err := run(t, h, choiceInput("hi", &harness.ToolChoice{Toolsets: []string{"maps"}, Search: harness.SearchDirect})); err != nil {
		t.Fatal(err)
	}
	diff(t, "tools", []string{"echo", "now", "svc_maps_lookup", "svc_maps_update"}, names(fake.Requests()[0].Tools))
	// Without a choice the harness's own deferral holds.
	if _, _, err := run(t, h, prompt("hi")); err != nil {
		t.Fatal(err)
	}
	diff(t, "tools without a choice", []string{"echo", "now", harness.SearchTool}, names(fake.Requests()[1].Tools))
}

// A call to a tool the choice left out is refused, never run.
func TestToolChoiceRefusesDroppedTool(t *testing.T) {
	leakcheck(t)
	r := &ran{}
	fake := llmtest.NewFake(t,
		llmtest.ToolCalls(use("c1", "svc_payroll_update", `{}`)),
		llmtest.Text("sorry"),
	)
	h := newHarness(t, fake, manyTools(t, r), harness.WithToolsets(svcToolset))
	if _, _, err := run(t, h, choiceInput("pay", &harness.ToolChoice{Toolsets: []string{"crm"}})); err != nil {
		t.Fatal(err)
	}
	if got := r.list(); len(got) != 0 {
		t.Errorf("ran %v, want nothing", got)
	}
	res := results(t, fake.Requests()[1].Messages[2])[0]
	if !res.IsError || !strings.Contains(res.Content, "not available in this chat") {
		t.Errorf("result = %+v, want a refusal", res)
	}
}

// A mode that is not one, or a choice of toolsets without WithToolsets,
// is invalid input.
func TestToolChoiceInvalid(t *testing.T) {
	leakcheck(t)
	for name, c := range map[string]struct {
		opts   []harness.Option
		choice *harness.ToolChoice
	}{
		"mode":         {[]harness.Option{harness.WithToolsets(svcToolset)}, &harness.ToolChoice{Search: "lazy"}},
		"no toolsetOf": {nil, &harness.ToolChoice{Toolsets: []string{"crm"}}},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, llmtest.NewFake(t), manyTools(t, &ran{}), c.opts...)
			_, _, err := run(t, h, choiceInput("hi", c.choice))
			if !errors.Is(err, harness.ErrInvalidInput) {
				t.Errorf("err = %v, want ErrInvalidInput", err)
			}
		})
	}
}
