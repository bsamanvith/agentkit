package harness_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/tool/tooltest"
)

// A run that stopped after a turn and was saved as JSON continues with exactly
// the requests an uninterrupted run would have sent.
func TestResumeFromSerializedTranscript(t *testing.T) {
	leakcheck(t)
	replies, opaque := threeTurns()
	reg := newRegistry(t, tooltest.Fixtures()...)

	whole := llmtest.NewFake(t, replies...)
	wholeRes, err := harness.Collect(newHarness(t, whole, reg).Run(t.Context(), prompt("start")))
	if err != nil {
		t.Fatal(err)
	}

	// The same run, stopped after its first turn by a budget...
	first := llmtest.NewFake(t, replies[0])
	_, part, err := run(t, newHarness(t, first, reg, harness.WithMaxTurns(1)), prompt("start"))
	if !errors.Is(err, harness.ErrBudgetExceeded) {
		t.Fatalf("err = %v, want ErrBudgetExceeded", err)
	}
	// ...saved, loaded...
	saved, err := json.Marshal(part.Transcript)
	if err != nil {
		t.Fatal(err)
	}
	var loaded harness.Transcript
	if err := json.Unmarshal(saved, &loaded); err != nil {
		t.Fatal(err)
	}
	// ...and resumed without a new message.
	second := llmtest.NewFake(t, replies[1:]...)
	resumed, err := harness.Collect(newHarness(t, second, reg).Run(t.Context(), harness.Input{Transcript: loaded}))
	if err != nil {
		t.Fatal(err)
	}

	diff(t, "requests", whole.Requests(), append(first.Requests(), second.Requests()...))
	diff(t, "final transcript", wholeRes.Transcript, resumed.Transcript)
	got := resumed.Transcript[1].Content[0].(llm.Opaque)
	if string(got.Data) != string(opaque.Data) {
		t.Errorf("opaque block after a save and load = %q, want %q", got.Data, opaque.Data)
	}
	if first.Remaining() != 0 || second.Remaining() != 0 {
		t.Error("scripted replies left over")
	}
}

func TestFollowUpContinuesTheConversation(t *testing.T) {
	leakcheck(t)
	fake := llmtest.NewFake(t, llmtest.Text("Paris."), llmtest.Text("About two million."))
	h := newHarness(t, fake, newRegistry(t))

	_, first, err := run(t, h, prompt("Capital of France?"))
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := run(t, h, harness.Input{Transcript: first.Transcript, Message: harness.UserText("And its population?")})
	if err != nil {
		t.Fatal(err)
	}
	want := append(harness.Transcript{}, first.Transcript...)
	want = append(want, harness.UserText("And its population?"), llm.Assistant(llm.Text{Text: "About two million."}))
	diff(t, "transcript", want, second.Transcript)
	diff(t, "second request", []llm.Message(want[:3]), fake.Requests()[1].Messages)
	if second.Turns != 1 {
		t.Errorf("Turns = %d: a run counts only its own turns", second.Turns)
	}
}

func TestInvalidInputIsRejectedBeforeAnyModelCall(t *testing.T) {
	leakcheck(t)
	user := harness.UserText("hi")
	asst := llm.Assistant(llm.Text{Text: "hello"})
	calls := llm.Assistant(use("c1", "echo", `{}`))
	result := llm.User(llm.ToolResult{ToolUseID: "c1", Content: "x"})
	tests := []struct {
		name string
		in   harness.Input
	}{
		{"nothing to answer", harness.Input{}},
		{"transcript ends with the assistant and no message", harness.Input{Transcript: harness.Transcript{user, asst}}},
		{"two user messages in a row", harness.Input{Transcript: harness.Transcript{user}, Message: user}},
		{"new message is from the assistant", harness.Input{Message: asst}},
		{"new message carries a tool result", harness.Input{Message: result}},
		{"transcript starts with the assistant", harness.Input{Transcript: harness.Transcript{asst}, Message: user}},
		{"tool call without results", harness.Input{Transcript: harness.Transcript{user, calls}, Message: user}},
		{"results for another call", harness.Input{Transcript: harness.Transcript{
			user, calls,
			llm.User(llm.ToolResult{ToolUseID: "other", Content: "x"}),
		}}},
		{"results with no call", harness.Input{Transcript: harness.Transcript{user, asst, result}, Message: user}},
		{"tool call in a user message", harness.Input{Message: llm.User(use("c1", "echo", `{}`))}},
		{"tool result in an assistant message", harness.Input{
			Transcript: harness.Transcript{user, llm.Assistant(llm.ToolResult{ToolUseID: "c1", Content: "x"})}, Message: user,
		}},
		{"tool call without a name", harness.Input{
			Transcript: harness.Transcript{user, llm.Assistant(use("c1", "", `{}`)), result},
		}},
		{"tool call with input that is not JSON", harness.Input{
			Transcript: harness.Transcript{user, llm.Assistant(use("c1", "echo", `{`)), result},
		}},
		{"opaque block without a provider", harness.Input{
			Transcript: harness.Transcript{user, llm.Assistant(llm.Opaque{Data: json.RawMessage(`{}`)})}, Message: user,
		}},
		{"new message with a nil block", harness.Input{Message: llm.User(nil)}},
		{"new message without content", harness.Input{Message: llm.User()}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := llmtest.NewFake(t)
			h := newHarness(t, fake, newRegistry(t))
			_, _, err := run(t, h, tc.in)
			if !errors.Is(err, harness.ErrInvalidInput) {
				t.Errorf("err = %v, want ErrInvalidInput", err)
			}
			if len(fake.Requests()) != 0 {
				t.Error("the model was called")
			}
		})
	}
}

func TestTurnRecordsAndDeltas(t *testing.T) {
	leakcheck(t)
	usage1 := llm.Usage{InputTokens: 11, OutputTokens: 2}
	usage2 := llm.Usage{InputTokens: 20, CacheReadTokens: 5, OutputTokens: 4}
	fake := llmtest.NewFake(t,
		reply(llm.StopToolUse, usage1, llm.Text{Text: "checking"}, use("c1", "echo", `{"text":"x"}`), use("c2", "fail", `{}`)),
		llmtest.Text("done").WithUsage(usage2),
	)
	h := newHarness(t, fake, newRegistry(t, tooltest.Fixtures()...))
	sent := time.Date(2026, 10, 11, 8, 59, 0, 0, time.UTC) // before the run's clock starts
	earlier := harness.Transcript{harness.UserText("old question"), llm.Assistant(llm.Text{Text: "old answer"})}

	events, res, err := run(t, h, harness.Input{Transcript: earlier, Message: harness.UserText("new"), At: sent})
	if err != nil {
		t.Fatal(err)
	}

	rec := res.Record
	if rec.Prompt == nil || !rec.Prompt.At.Equal(sent) {
		t.Errorf("Prompt = %+v, want it stamped with Input.At", rec.Prompt)
	}
	if len(rec.Turns) != 2 {
		t.Fatalf("%d turn records, want 2", len(rec.Turns))
	}
	t1, t2 := rec.Turns[0], rec.Turns[1]
	if t1.Usage != usage1 || t2.Usage != usage2 || t1.Stop.Kind != llm.StopToolUse || t2.Stop.Kind != llm.StopEndTurn {
		t.Errorf("usage or stop reason wrong: %+v %+v", t1, t2)
	}
	if res.Usage != (llm.Usage{InputTokens: 31, CacheReadTokens: 5, OutputTokens: 6}) {
		t.Errorf("total usage = %+v", res.Usage)
	}
	if len(t1.Messages) != 2 || len(t2.Messages) != 1 {
		t.Fatalf("turns added %d and %d messages, want 2 and 1", len(t1.Messages), len(t2.Messages))
	}
	diff(t, "tools", []string{"c1", "c2"}, []string{t1.Tools[0].ToolUseID, t1.Tools[1].ToolUseID})
	if t1.Tools[0].IsError || !t1.Tools[1].IsError || t1.Tools[0].Name != "echo" || t1.Tools[0].Duration <= 0 {
		t.Errorf("tool records = %+v", t1.Tools)
	}

	// Times never go backwards: run start, model call, first event, reply (the
	// assistant message is stamped with it), tool start, results, and so on.
	chain := []time.Time{
		rec.StartedAt, t1.RequestedAt, t1.FirstEventAt, t1.DoneAt, t1.Messages[0].At,
		t1.Tools[0].StartedAt, t1.Messages[1].At, t2.RequestedAt, t2.FirstEventAt, t2.DoneAt, rec.FinishedAt,
	}
	for i := 1; i < len(chain); i++ {
		if chain[i].Before(chain[i-1]) {
			t.Errorf("time %d (%v) is before time %d (%v)", i, chain[i], i-1, chain[i-1])
		}
	}

	// Applying the turn deltas to the input gives the final transcript, from the
	// events and from the record alike.
	want := append(harness.Transcript{}, earlier...)
	want = append(want, harness.UserText("new"))
	for _, tr := range rec.Turns {
		for _, m := range tr.Messages {
			want = append(want, m.Message)
		}
	}
	diff(t, "transcript rebuilt from the record", want, res.Transcript)
	diff(t, "transcript rebuilt from events", want[len(earlier):], announced(events))
	var fromEvents []harness.TurnRecord
	for _, ev := range events {
		if ev.Kind == harness.EventTurnDone {
			fromEvents = append(fromEvents, *ev.TurnRecord)
		}
	}
	diff(t, "turn records in events and in the result", rec.Turns, fromEvents)
}

// referenceValid says whether a conversation is well formed, written down
// again from the rules in the Transcript documentation and sharing no code
// with Validate. The tests compare the two.
func referenceValid(msgs []llm.Message) bool {
	var awaiting []llm.ToolUse // calls the next message must answer
	for i, m := range msgs {
		if i%2 == 0 && m.Role != llm.RoleUser || i%2 == 1 && m.Role != llm.RoleAssistant {
			return false
		}
		if m.Role == llm.RoleUser {
			if len(m.Content) == 0 || (len(awaiting) > 0 && len(m.Content) != len(awaiting)) {
				return false
			}
			for j, b := range m.Content {
				switch b := b.(type) {
				case llm.Text:
					if len(awaiting) > 0 {
						return false
					}
				case llm.Opaque:
					if len(awaiting) > 0 || b.Provider == "" || !json.Valid(b.Data) {
						return false
					}
				case llm.ToolResult:
					if b.ToolUseID == "" || len(awaiting) == 0 || b.ToolUseID != awaiting[j].ID {
						return false
					}
				default: // a tool call, or no block at all
					return false
				}
			}
			awaiting = nil
			continue
		}
		seen := map[string]bool{}
		for _, b := range m.Content {
			switch b := b.(type) {
			case llm.Text:
			case llm.ToolUse:
				if b.ID == "" || b.Name == "" || seen[b.ID] || len(b.Input) > 0 && !json.Valid(b.Input) {
					return false
				}
				seen[b.ID] = true
				awaiting = append(awaiting, b)
			case llm.Opaque:
				if b.Provider == "" || !json.Valid(b.Data) {
					return false
				}
			default: // a tool result, or no block at all
				return false
			}
		}
	}
	return len(awaiting) == 0
}

// Validate agrees with the reference on conversations built from blocks that
// are right and wrong in every way the rules name, in every order.
func TestValidateAgreesWithTheRules(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	pool := []llm.Block{
		llm.Text{Text: "text"},
		use("c1", "echo", `{}`), use("c2", "echo", `{"a":1}`), use("c1", "echo", `{}`),
		use("", "echo", `{}`), use("c3", "", `{}`), use("c4", "echo", `{`),
		llm.ToolResult{ToolUseID: "c1", Content: "r"},
		llm.ToolResult{ToolUseID: "c2", Content: "r"},
		llm.ToolResult{ToolUseID: "", Content: "r"},
		llm.ToolResult{ToolUseID: "zzz", Content: "r"},
		llm.Opaque{Provider: "p", Data: json.RawMessage(`{"a":1}`)},
		llm.Opaque{Data: json.RawMessage(`{}`)},
		llm.Opaque{Provider: "p", Data: json.RawMessage(`{`)},
		nil,
	}
	agree, valid := 0, 0
	for range 20000 {
		var msgs harness.Transcript
		for i := range rng.IntN(6) {
			role := llm.RoleUser
			if i%2 == 1 {
				role = llm.RoleAssistant
			}
			if rng.IntN(25) == 0 {
				role = "robot"
			}
			m := llm.Message{Role: role}
			for range rng.IntN(4) {
				m.Content = append(m.Content, pool[rng.IntN(len(pool))])
			}
			msgs = append(msgs, m)
		}
		got, want := msgs.Validate() == nil, referenceValid(msgs)
		if got != want {
			t.Fatalf("Validate = %v, the rules say %v, for %s", got, want, mustMarshal2(msgs))
		}
		agree++
		if want {
			valid++
		}
	}
	if valid < 200 {
		t.Errorf("only %d of %d random conversations were valid: the generator no longer reaches the valid ones", valid, agree)
	}
}

func mustMarshal2(msgs harness.Transcript) string {
	var parts []string
	for _, m := range msgs {
		parts = append(parts, fmt.Sprintf("%s%v", m.Role, m.Content))
	}
	return strings.Join(parts, " | ")
}

func FuzzTranscript(f *testing.F) {
	valid, _ := json.Marshal(harness.Transcript{
		harness.UserText("hi"),
		llm.Assistant(llm.Text{Text: "hello"}, use("c1", "echo", `{"text":"x"}`)),
		llm.User(llm.ToolResult{ToolUseID: "c1", Content: "x"}),
	})
	for _, seed := range []string{
		string(valid), `[]`, `null`, `[{"role":"assistant","content":[]}]`,
		`[{"role":"user","content":[{"type":"text","text":"a"}]},{"role":"user","content":[{"type":"text","text":"b"}]}]`,
		`[{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":"y"}]}]`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var tr harness.Transcript
		if json.Unmarshal(data, &tr) != nil {
			return
		}
		err := tr.Validate()
		if err != nil && !errors.Is(err, harness.ErrInvalidInput) {
			t.Fatalf("Validate error %v does not match ErrInvalidInput", err)
		}
		if (err == nil) != referenceValid(tr) {
			t.Fatalf("Validate = %v, the rules say %v", err, referenceValid(tr))
		}
		// Saving and loading changes nothing.
		again, merr := json.Marshal(tr)
		if merr != nil {
			t.Fatalf("a decoded transcript does not marshal: %v", merr)
		}
		var back harness.Transcript
		if uerr := json.Unmarshal(again, &back); uerr != nil {
			t.Fatalf("a marshaled transcript does not decode: %v", uerr)
		}
		if (back.Validate() == nil) != (err == nil) {
			t.Fatal("Validate changed its mind after a save and load")
		}
		// A valid transcript that ends with a user message can be run; one that
		// does not is refused, and nothing panics either way.
		fake := llmtest.NewFake(t, llmtest.Text("ok"))
		h, herr := harness.New(fake, newRegistry(t))
		if herr != nil {
			t.Fatal(herr)
		}
		res, rerr := harness.Collect(h.Run(t.Context(), harness.Input{Transcript: tr}))
		endsWithUser := len(tr) > 0 && tr[len(tr)-1].Role == llm.RoleUser
		switch {
		case err == nil && endsWithUser:
			if rerr != nil || len(res.Transcript) != len(tr)+1 {
				t.Fatalf("a valid transcript failed to run: %v", rerr)
			}
		default:
			if !errors.Is(rerr, harness.ErrInvalidInput) || len(fake.Requests()) != 0 {
				t.Fatalf("an unusable transcript was not refused: %v", rerr)
			}
		}
	})
}
