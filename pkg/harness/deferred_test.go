package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/policy"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// services are the topics of the deferred tools: svc_<topic>_lookup and
// svc_<topic>_update for each, 30 in all.
var services = []string{
	"weather", "billing", "calendar", "crm", "docs", "email", "github", "jira",
	"maps", "news", "payroll", "search", "slack", "stocks", "tickets",
}

// manyTools is a registry of two plain tools (now, echo) and 30 deferred
// ones, recording which ran in r.
func manyTools(t testing.TB, r *ran) tool.Registry {
	t.Helper()
	tools := []tool.Tool{
		classified(t, r, "now", tool.Metadata{Kind: tool.KindRead, Risk: tool.RiskLow}),
		classified(t, r, "echo", tool.Metadata{Kind: tool.KindRead, Risk: tool.RiskLow}),
	}
	for _, s := range services {
		for _, verb := range []string{"lookup", "update"} {
			name := "svc_" + s + "_" + verb
			m := tool.Metadata{Kind: tool.KindRead, Risk: tool.RiskLow, Tags: []string{"svc-" + s}}
			if verb == "update" {
				m = tool.Metadata{Kind: tool.KindWrite, Risk: tool.RiskMedium}
			}
			tl, err := tool.Func(name, fmt.Sprintf("%s the %s service's records.", verb, s), func(context.Context, struct{}) (string, error) {
				r.add(name)
				return name + " done", nil
			}, tool.WithMetadata(m))
			if err != nil {
				t.Fatal(err)
			}
			tools = append(tools, tl)
		}
	}
	return newRegistry(t, tools...)
}

func deferSvc(name string) bool { return strings.HasPrefix(name, "svc_") }

func names(defs []tool.Definition) []string {
	out := make([]string, len(defs))
	for i, d := range defs {
		out[i] = d.Name
	}
	return out
}

// loadedOf returns the tools each turn of a record loaded, in order.
func loadedOf(rec harness.Record) []string {
	var out []string
	for _, tr := range rec.Turns {
		out = append(out, tr.Loaded...)
	}
	return out
}

// Search and load: with 30 deferred tools, the first request carries only the
// plain tools and tool_search; a search loads the tool it finds, which the
// model then has for the rest of the run, and in the runs that continue the
// transcript: a follow-up, and a resume of a transcript that stopped right
// after the search's results.
func TestDeferredSearchAndLoad(t *testing.T) {
	leakcheck(t)
	r := &ran{}
	reg := manyTools(t, r)
	fake := llmtest.NewFake(t,
		llmtest.ToolCalls(use("c1", harness.SearchTool, `{"query":"weather lookup","max_results":1}`)),
		llmtest.ToolCalls(use("c2", "svc_weather_lookup", `{}`)),
		llmtest.Text("Sunny."),
		// The follow-up run.
		llmtest.ToolCalls(use("c3", "svc_weather_lookup", `{}`)),
		llmtest.Text("Still sunny."),
	)
	h := newHarness(t, fake, reg, harness.WithDeferLoading(deferSvc))
	_, res, err := run(t, h, prompt("What's the weather?"))
	if err != nil {
		t.Fatal(err)
	}
	reqs := fake.Requests()
	base := []string{"echo", "now", harness.SearchTool}
	diff(t, "tools of the first request", base, names(reqs[0].Tools))
	diff(t, "tools after the search", append(slices.Clone(base), "svc_weather_lookup"), names(reqs[1].Tools))
	diff(t, "tools of the last request", names(reqs[1].Tools), names(reqs[2].Tools))
	got := results(t, reqs[1].Messages[2])[0]
	diff(t, "what the search loaded", []string{"svc_weather_lookup"}, got.Loads)
	var out struct {
		Tools []struct{ Name, Description string }
	}
	if err := json.Unmarshal([]byte(got.Content), &out); err != nil || len(out.Tools) != 1 || out.Tools[0].Name != "svc_weather_lookup" {
		t.Errorf("search result = %q (%v), want the found tool's name and description", got.Content, err)
	}
	diff(t, "tools that ran", []string{"svc_weather_lookup"}, r.list())
	diff(t, "the record's loaded tools", []string{"svc_weather_lookup"}, loadedOf(res.Record))

	// A follow-up run of the transcript has the tool from its first request.
	_, next, err := run(t, h, harness.Input{Transcript: res.Transcript, Message: harness.UserText("And now?")})
	if err != nil {
		t.Fatal(err)
	}
	reqs = fake.Requests()
	diff(t, "tools of a follow-up's first request", append(slices.Clone(base), "svc_weather_lookup"), names(reqs[3].Tools))
	if len(loadedOf(next.Record)) != 0 {
		t.Errorf("a follow-up loaded %v again", loadedOf(next.Record))
	}

	// A resume of the transcript as it stood after the search's results.
	stopped := res.Transcript[:3]
	resumeFake := llmtest.NewFake(t, llmtest.Text("Sunny."))
	resumed := newHarness(t, resumeFake, reg, harness.WithDeferLoading(deferSvc))
	if _, _, err := run(t, resumed, harness.Input{Transcript: stopped}); err != nil {
		t.Fatal(err)
	}
	diff(t, "tools of a resume's first request", append(slices.Clone(base), "svc_weather_lookup"), names(resumeFake.Requests()[0].Tools))
}

// Native: every request lists every tool and names the 30 deferred ones; a
// search's result carries only the references.
func TestDeferredNative(t *testing.T) {
	leakcheck(t)
	r := &ran{}
	fake := llmtest.NewFake(t,
		llmtest.ToolCalls(use("c1", harness.SearchTool, `{"query":"svc_crm_lookup"}`)),
		llmtest.ToolCalls(use("c2", "svc_crm_lookup", `{}`)),
		llmtest.Text("Found it."),
	)
	h := newHarness(t, fake, manyTools(t, r), harness.WithDeferLoading(deferSvc), harness.WithNativeToolSearch())
	_, res, err := run(t, h, prompt("Look up the customer."))
	if err != nil {
		t.Fatal(err)
	}
	reqs := fake.Requests()
	for i, req := range reqs {
		if len(req.Tools) != 33 || len(req.Deferred) != 30 {
			t.Errorf("request %d: %d tools, %d deferred; want 33 and 30", i, len(req.Tools), len(req.Deferred))
		}
	}
	got := results(t, reqs[1].Messages[2])[0]
	if got.Content != "" || got.Loads[0] != "svc_crm_lookup" {
		t.Errorf("native search result = %+v, want no content and the tool named by the query first", got)
	}
	diff(t, "tools that ran", []string{"svc_crm_lookup"}, r.list())
	if l := loadedOf(res.Record); len(l) == 0 || l[0] != "svc_crm_lookup" {
		t.Errorf("record loaded %v", l)
	}
}

// Skills: a loader's successful call loads the deferred tools it names, by
// name or glob; names of tools the agent does not have, and of tools that
// are not deferred, load nothing.
func TestDeferredLoader(t *testing.T) {
	leakcheck(t)
	r := &ran{}
	reg := joinTools(t, manyTools(t, r), classified(t, r, "skill_load", tool.Metadata{Kind: tool.KindRead, Risk: tool.RiskLow}))
	listed := func(json.RawMessage) []string { return []string{"svc_github_*", "svc_nope", "gitlab_*", "now"} }
	fake := llmtest.NewFake(t,
		llmtest.ToolCalls(use("c1", "skill_load", `{}`)),
		llmtest.ToolCalls(use("c2", "svc_github_update", `{}`)),
		llmtest.Text("Done."),
	)
	h := newHarness(t, fake, reg, harness.WithDeferLoading(deferSvc), harness.WithToolLoader("skill_load", listed))
	_, res, err := run(t, h, prompt("File the issue."))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"svc_github_lookup", "svc_github_update"}
	diff(t, "what the skill loaded", want, loadedOf(res.Record))
	diff(t, "tools after the skill loaded", append([]string{"echo", "now", "skill_load", harness.SearchTool}, want...), names(fake.Requests()[1].Tools))
	diff(t, "tools that ran", []string{"skill_load", "svc_github_update"}, r.list())
}

// Policy: loading changes no decision; a read-only policy still denies a
// loaded write tool, which does not run.
func TestDeferredPolicyStillApplies(t *testing.T) {
	leakcheck(t)
	r := &ran{}
	fake := llmtest.NewFake(t,
		llmtest.ToolCalls(use("c1", harness.SearchTool, `{"query":"update payroll"}`)),
		llmtest.ToolCalls(use("c2", "svc_payroll_update", `{}`)),
		llmtest.Text("I may not."),
	)
	h := newHarness(t, fake, manyTools(t, r), harness.WithDeferLoading(deferSvc),
		harness.WithToolPolicy(policy.MustPreset(policy.ReadOnly), nil))
	events, res, err := run(t, h, prompt("Raise my pay."))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(loadedOf(res.Record), "svc_payroll_update") {
		t.Fatalf("the search did not load the tool: %v", loadedOf(res.Record))
	}
	if ds := decisions(events)["svc_payroll_update"]; len(ds) != 1 || ds[0].Decision.Action != policy.Deny {
		t.Errorf("policy events %+v, want one deny", ds)
	}
	if got := results(t, fake.Requests()[2].Messages[4])[0]; !got.IsError {
		t.Errorf("result %+v, want the denial", got)
	}
	diff(t, "tools that ran", []string(nil), r.list())
}

// Only the run's deferred tools load from a transcript: a result that says
// it loaded a tool the agent does not have, or one sent anyway, adds
// nothing to the requests.
func TestDeferredTranscriptLoadsOnlyDeferredTools(t *testing.T) {
	leakcheck(t)
	r := &ran{}
	given := harness.Transcript{
		harness.UserText("earlier"),
		llm.Assistant(use("c1", "now", `{}`)),
		llm.User(llm.ToolResult{ToolUseID: "c1", Content: "x", Loads: []string{"svc_ghost", "echo", "svc_news_lookup"}}),
	}
	fake := llmtest.NewFake(t, llmtest.Text("ok"))
	h := newHarness(t, fake, manyTools(t, r), harness.WithDeferLoading(deferSvc))
	if _, _, err := run(t, h, harness.Input{Transcript: given}); err != nil {
		t.Fatal(err)
	}
	diff(t, "tools of the first request", []string{"echo", "now", harness.SearchTool, "svc_news_lookup"}, names(fake.Requests()[0].Tools))
}

// A deferred tool the model calls before loading it does not run: the model
// is told to search for it.
func TestDeferredNotLoadedIsRefused(t *testing.T) {
	leakcheck(t)
	r := &ran{}
	fake := llmtest.NewFake(t, llmtest.ToolCalls(use("c1", "svc_maps_lookup", `{}`)), llmtest.Text("ok"))
	h := newHarness(t, fake, manyTools(t, r), harness.WithDeferLoading(deferSvc))
	if _, _, err := run(t, h, prompt("Where?")); err != nil {
		t.Fatal(err)
	}
	got := results(t, fake.Requests()[1].Messages[2])[0]
	if !got.IsError || !strings.Contains(got.Content, harness.SearchTool) {
		t.Errorf("result %+v, want an error pointing at the search", got)
	}
	diff(t, "tools that ran", []string(nil), r.list())
}

// A tool of the agent's named tool_search cannot share a run with deferred
// tools; a loader named tool_search is refused by New.
func TestDeferredSearchNameClash(t *testing.T) {
	leakcheck(t)
	r := &ran{}
	reg := joinTools(t, manyTools(t, r), classified(t, r, harness.SearchTool, tool.Metadata{}))
	h := newHarness(t, llmtest.NewFake(t), reg, harness.WithDeferLoading(deferSvc))
	if _, _, err := run(t, h, prompt("hi")); !errors.Is(err, harness.ErrInvalidConfig) {
		t.Errorf("err = %v, want ErrInvalidConfig", err)
	}
	_, err := harness.New(llmtest.NewFake(t), reg, harness.WithToolLoader(harness.SearchTool, func(json.RawMessage) []string { return nil }))
	if !errors.Is(err, harness.ErrInvalidConfig) {
		t.Errorf("New with a loader named %s: err = %v, want ErrInvalidConfig", harness.SearchTool, err)
	}
}

// The search ranks a tool the query names first, and name words over
// description words, and loads nothing for a query that matches nothing or
// is empty.
func TestDeferredSearchRanking(t *testing.T) {
	leakcheck(t)
	for _, c := range []struct {
		query string
		want  []string // the first tools loaded, in order
		err   bool
	}{
		{query: "svc_jira_update", want: []string{"svc_jira_update"}},
		{query: "stocks", want: []string{"svc_stocks_lookup", "svc_stocks_update"}},
		{query: "records", want: []string{"svc_billing_lookup"}},                                                // description only: by name
		{query: "weather today", want: []string{"svc_weather_lookup", "svc_weather_update", "svc_daily_brief"}}, // a name word beats two description words
		{query: "teleport"},
		{query: "  ", err: true},
	} {
		t.Run(c.query, func(t *testing.T) {
			r := &ran{}
			fake := llmtest.NewFake(t, llmtest.ToolCalls(use("c1", harness.SearchTool, fmt.Sprintf(`{"query":%q}`, c.query))), llmtest.Text("ok"))
			brief, err := tool.Func("svc_daily_brief", "Today's weather and news.", func(context.Context, struct{}) (string, error) { return "", nil })
			if err != nil {
				t.Fatal(err)
			}
			h := newHarness(t, fake, joinTools(t, manyTools(t, r), brief), harness.WithDeferLoading(deferSvc))
			if _, _, err := run(t, h, prompt("find")); err != nil {
				t.Fatal(err)
			}
			got := results(t, fake.Requests()[1].Messages[2])[0]
			if got.IsError != c.err {
				t.Fatalf("result %+v, want error %v", got, c.err)
			}
			if len(got.Loads) < len(c.want) || !slices.Equal(got.Loads[:len(c.want)], c.want) || (c.want == nil && got.Loads != nil) {
				t.Errorf("loaded %v, want it to start with %v", got.Loads, c.want)
			}
		})
	}
}

// joinTools is a registry of reg's tools and more.
func joinTools(t testing.TB, reg tool.Registry, more ...tool.Tool) tool.Registry {
	t.Helper()
	defs, err := reg.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	tools := slices.Clone(more)
	for _, d := range defs {
		tools = append(tools, proxy{d, reg})
	}
	return newRegistry(t, tools...)
}

// proxy is a tool of another registry.
type proxy struct {
	def tool.Definition
	reg tool.Registry
}

func (p proxy) Definition() tool.Definition { return p.def.Clone() }
func (p proxy) Invoke(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	return p.reg.Invoke(ctx, p.def.Name, in)
}

// Natively too, a deferred tool the model calls before loading it does not
// run: the provider holds its definition back, and the harness refuses it.
func TestDeferredNativeNotLoadedIsRefused(t *testing.T) {
	leakcheck(t)
	r := &ran{}
	fake := llmtest.NewFake(t, llmtest.ToolCalls(use("c1", "svc_maps_lookup", `{}`)), llmtest.Text("ok"))
	h := newHarness(t, fake, manyTools(t, r), harness.WithDeferLoading(deferSvc), harness.WithNativeToolSearch())
	if _, _, err := run(t, h, prompt("Where?")); err != nil {
		t.Fatal(err)
	}
	if got := results(t, fake.Requests()[1].Messages[2])[0]; !got.IsError || len(got.Loads) != 0 {
		t.Errorf("result %+v, want an error that loads nothing", got)
	}
	diff(t, "tools that ran", []string(nil), r.list())
}

// A search finds only tools not loaded yet, at most 20 however many it is
// asked for; a loader loads only what is not loaded yet.
func TestDeferredSearchSkipsLoaded(t *testing.T) {
	leakcheck(t)
	r := &ran{}
	reg := joinTools(t, manyTools(t, r), classified(t, r, "skill_load", tool.Metadata{Kind: tool.KindRead, Risk: tool.RiskLow}))
	listed := func(json.RawMessage) []string { return []string{"svc_weather_*"} }
	fake := llmtest.NewFake(t,
		llmtest.ToolCalls(use("c1", harness.SearchTool, `{"query":"svc_weather_lookup","max_results":1}`)),
		llmtest.ToolCalls(use("c2", harness.SearchTool, `{"query":"weather"}`)),
		llmtest.ToolCalls(use("c3", harness.SearchTool, `{"query":"svc","max_results":100}`)),
		llmtest.ToolCalls(use("c4", "skill_load", `{}`)),
		llmtest.Text("ok"),
	)
	h := newHarness(t, fake, reg, harness.WithDeferLoading(deferSvc), harness.WithToolLoader("skill_load", listed))
	if _, _, err := run(t, h, prompt("find")); err != nil {
		t.Fatal(err)
	}
	reqs := fake.Requests()
	diff(t, "what the second search loaded", []string{"svc_weather_update"}, results(t, reqs[2].Messages[4])[0].Loads)
	if got := results(t, reqs[3].Messages[6])[0].Loads; len(got) != 20 || slices.ContainsFunc(got, func(n string) bool { return strings.HasPrefix(n, "svc_weather") }) {
		t.Errorf("third search loaded %v, want 20 tools, none loaded already", got)
	}
	diff(t, "what skill_load loaded", []string(nil), results(t, reqs[4].Messages[8])[0].Loads)
}

// A result marked as an error loads nothing, from a transcript too.
func TestDeferredErrorResultLoadsNothing(t *testing.T) {
	leakcheck(t)
	r := &ran{}
	given := harness.Transcript{
		harness.UserText("earlier"),
		llm.Assistant(use("c1", "now", `{}`)),
		llm.User(llm.ToolResult{ToolUseID: "c1", Content: "x", IsError: true, Loads: []string{"svc_news_lookup"}}),
	}
	fake := llmtest.NewFake(t, llmtest.Text("ok"))
	h := newHarness(t, fake, manyTools(t, r), harness.WithDeferLoading(deferSvc))
	if _, _, err := run(t, h, harness.Input{Transcript: given}); err != nil {
		t.Fatal(err)
	}
	diff(t, "tools of the first request", []string{"echo", "now", harness.SearchTool}, names(fake.Requests()[0].Tools))
}

// A loader call that waits for approval loads its tools once a person
// approves it, in the resumed run.
func TestDeferredLoaderAfterApproval(t *testing.T) {
	leakcheck(t)
	r := &ran{}
	reg := joinTools(t, manyTools(t, r), classified(t, r, "skill_load", tool.Metadata{Kind: tool.KindWrite, Risk: tool.RiskMedium}))
	listed := func(json.RawMessage) []string { return []string{"svc_github_lookup"} }
	fake := llmtest.NewFake(t,
		llmtest.ToolCalls(use("c1", "skill_load", `{}`)),
		llmtest.ToolCalls(use("c2", "svc_github_lookup", `{}`)),
		llmtest.Text("Done."),
	)
	opts := []harness.Option{
		harness.WithDeferLoading(deferSvc), harness.WithToolLoader("skill_load", listed),
		harness.WithInteraction(harness.Supervised), harness.WithToolPolicy(policy.MustPreset(policy.ApproveWrites), nil),
	}
	h := newHarness(t, fake, reg, opts...)
	_, res, err := run(t, h, prompt("Look it up."))
	if err != nil || res.Pending == nil {
		t.Fatalf("err %v, pending %v", err, res.Pending)
	}
	_, res2, err := run(t, h, harness.Input{Transcript: res.Transcript, Pending: saved(t, res.Pending), Answers: []harness.Answer{{ID: "c1", Approved: true}}})
	if err != nil {
		t.Fatal(err)
	}
	diff(t, "what the approved skill loaded", []string{"svc_github_lookup"}, loadedOf(res2.Record))
	diff(t, "tools that ran", []string{"skill_load", "svc_github_lookup"}, r.list())
}
