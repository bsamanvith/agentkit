package agentapi_test

import (
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/tool/tooltest"
)

// TestCompactedRunKeepsCounting: a run that did not fit is compacted and
// runs again; its turns go on from those it had done, and its usage is that
// of its turns.
func TestCompactedRunKeepsCounting(t *testing.T) {
	t.Parallel()
	tools, err := tool.NewRegistry(tooltest.Fixtures())
	if err != nil {
		t.Fatal(err)
	}
	u := llm.Usage{InputTokens: 10, OutputTokens: 1}
	m := script(t,
		llmtest.Text("hello").WithUsage(u),
		llmtest.ToolCalls(llm.ToolUse{ID: "t1", Name: "echo", Input: []byte(`{"text":"x"}`)}).WithUsage(u),
		llmtest.Fail(llm.ErrContextTooLong),
		llmtest.Text("the summary").WithUsage(llm.Usage{InputTokens: 100}),
		llmtest.Text("done").WithUsage(u),
	)
	r := newRig(t, m, rigConfig{tools: tools})
	conv := r.run("hi", "").conversationID()
	e := r.run("more", conv).last()
	if e.Type != agentapi.TypeRunDone || e.Turns != 2 || e.Answer != "done" || *e.Usage != u.Add(u) {
		t.Errorf("last event %+v (usage %+v), want run_done after 2 turns, the turns' usage", e, e.Usage)
	}
}
