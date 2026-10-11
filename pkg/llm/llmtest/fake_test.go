package llmtest_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

func TestFakeConformance(t *testing.T) {
	t.Parallel()
	llmtest.RunModel(t, func(t *testing.T, s llmtest.Scenario) llm.Model {
		return llmtest.NewFake(t, s.Replies...)
	})
}

func TestFake(t *testing.T) {
	t.Parallel()
	call := llm.ToolUse{ID: "toolu_1", Name: "echo", Input: []byte(`{"text":"hello there"}`)}
	fake := llmtest.NewFake(t,
		llmtest.ToolCalls(call),
		llmtest.Text("All done."),
	)
	newReq := func() llm.Request {
		return llm.Request{
			System:   "sys",
			Messages: []llm.Message{llm.User(llm.Text{Text: "hi"})},
			Tools: []tool.Definition{{
				Name: "echo", InputSchema: []byte(`{"type":"object"}`),
			}},
			MaxTokens: 100,
		}
	}

	// Replies come in order, and are streamed event by event.
	req1 := newReq()
	var kinds []llm.EventKind
	var input strings.Builder
	var first llm.Response
	for ev, err := range fake.Generate(t.Context(), req1) {
		if err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, ev.Kind)
		if ev.Kind == llm.EventToolInputDelta {
			input.WriteString(ev.Text)
		}
		if ev.Kind == llm.EventMessageDone {
			first = *ev.Response
		}
	}
	if len(kinds) < 5 || kinds[0] != llm.EventToolUseStart || kinds[len(kinds)-1] != llm.EventMessageDone {
		t.Errorf("event kinds = %v, want a tool-use start, several input deltas, a block done and a message done", kinds)
	}
	if input.String() != string(call.Input) {
		t.Errorf("input deltas = %q, want %q", input.String(), call.Input)
	}
	if first.Stop.Kind != llm.StopToolUse || len(first.Message.Content) != 1 {
		t.Errorf("first response = %#v", first)
	}

	req2 := newReq()
	req2.Messages = append(req2.Messages, first.Message, llm.User(llm.ToolResult{ToolUseID: "toolu_1", Content: "hello there"}))
	second, err := llm.Collect(fake.Generate(t.Context(), req2))
	if err != nil || second.Message.Text() != "All done." {
		t.Fatalf("second reply = %#v, %v", second, err)
	}

	// Mutating what was sent afterwards, or what Requests returned, does not
	// change the record.
	req1.Messages[0].Content[0] = llm.Text{Text: "changed"}
	req1.Tools[0].InputSchema[0] = '['
	req1.System = "changed"
	got := fake.Requests()
	got[1].Messages[0] = llm.User()
	got = fake.Requests()
	want1, want2 := newReq(), newReq()
	want2.Messages = req2.Messages
	if len(got) != 2 {
		t.Fatalf("recorded %d requests, want 2", len(got))
	}
	for i, want := range []llm.Request{want1, want2} {
		if fmt.Sprintf("%#v", got[i]) != fmt.Sprintf("%#v", want) {
			t.Errorf("request %d changed after the fact:\n got %#v\nwant %#v", i, got[i], want)
		}
	}

	// Out of replies: the test fails, with a clear sign in the stream too.
	rec := &recorder{}
	empty := llmtest.NewFake(rec, llmtest.Text("only"))
	if _, err := llm.Collect(empty.Generate(t.Context(), newReq())); err != nil {
		t.Errorf("first call: %v", err)
	}
	if rec.failed {
		t.Fatal("the first reply failed the test")
	}
	_, err = llm.Collect(empty.Generate(t.Context(), newReq()))
	if !errors.Is(err, llmtest.ErrNoReply) {
		t.Errorf("err = %v, want ErrNoReply", err)
	}
	if !rec.failed {
		t.Error("running out of replies did not fail the test")
	}
}

// recorder is a testing.TB that records failures instead of reporting them.
type recorder struct {
	testing.TB
	failed bool
}

func (r *recorder) Helper() {}

func (r *recorder) Errorf(string, ...any) { r.failed = true }
