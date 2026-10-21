package agentapi_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
)

// stepClock is a fake clock whose every reading is one millisecond after the
// last, so times and durations in golden files are distinct and stable.
type stepClock struct{ *clocktest.Fake }

func (c stepClock) Now() time.Time {
	c.Advance(time.Millisecond)
	return c.Fake.Now()
}

// goldenRun runs one scripted run with stable IDs and times and returns the
// raw event stream, the run and the conversation as the API wrote them.
func goldenRun(t *testing.T, replies ...llmtest.Reply) (stream, run, conv []byte) {
	t.Helper()
	fake := clocktest.NewFake(time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC))
	clk := stepClock{fake}
	r := newRig(t, script(t, replies...), rigConfig{tools: demoTools(t)["a"], clock: fake, now: clk})
	resp := r.post(t.Context(), runBody("Echo hi, then tell me what it said.", ""), "")
	defer resp.Body.Close()
	stream, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return stream, rawGet(t, r, "/v1/runs/run_002"), rawGet(t, r, "/v1/conversations/conv_001")
}

func rawGet(t *testing.T, r *rig, path string) []byte {
	t.Helper()
	resp, err := r.ts.Client().Get(r.ts.URL + path) //nolint:noctx // test against a local server
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d %v", path, resp.StatusCode, err)
	}
	var out bytes.Buffer
	if err := json.Indent(&out, body, "", "  "); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

var thinking = llm.Opaque{Provider: "anthropic", Data: json.RawMessage(`{"type":"thinking","thinking":"","signature":"c2lnbmF0dXJl"}`)}

// TestEventJSONGolden: the wire form of every event type, of a run and of a
// conversation is what the golden files say. The tool run has run_start,
// text_delta, tool_call, tool_result, turn_done and run_done; the refusal
// has run_error. Update with `make golden` and review the diff: within
// version 1 changes must be additive.
func TestEventJSONGolden(t *testing.T) {
	t.Parallel()
	first := textAndCalls("Let me echo that.", call("toolu_1", "echo", `{"text":"hi"}`)).
		WithUsage(llm.Usage{InputTokens: 120, OutputTokens: 30, CacheWriteTokens: 640})
	first.Response.Message.Content = append([]llm.Block{thinking}, first.Response.Message.Content...)
	second := llmtest.Text("It said: [a] hi").WithUsage(llm.Usage{InputTokens: 15, OutputTokens: 9, CacheReadTokens: 640})
	stream, run, conv := goldenRun(t, first, second)
	testkit.Golden(t, filepath.Join("testdata", "tool_run.sse"), stream)
	testkit.Golden(t, filepath.Join("testdata", "tool_run.run.json"), run)
	testkit.Golden(t, filepath.Join("testdata", "tool_run.conversation.json"), conv)

	refusal := llmtest.Text("I can't help with that.").WithUsage(llm.Usage{InputTokens: 50, OutputTokens: 6})
	refusal.Response.Stop = llm.StopReason{Kind: llm.StopRefusal, Category: "cyber", Explanation: "This looks like an attack."}
	stream, _, _ = goldenRun(t, refusal)
	testkit.Golden(t, filepath.Join("testdata", "refusal.sse"), stream)
}

// TestEventJSONIsAdditive: a client built for these events still parses them,
// unchanged, when fields it does not know are added at any level of an
// event's own objects and of its messages and blocks; a block of a type it
// does not know is kept, as an llm.Unknown block.
func TestEventJSONIsAdditive(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"tool_run.sse", "refusal.sse"} {
		raw := readFile(t, filepath.Join("testdata", name))
		seen := map[string]bool{}
		for line := range strings.SplitSeq(raw, "\n") {
			data, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue
			}
			var want agentapi.Event
			if err := json.Unmarshal([]byte(data), &want); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			seen[want.Type] = true
			var obj map[string]any
			if err := json.Unmarshal([]byte(data), &obj); err != nil {
				t.Fatal(err)
			}
			addUnknown(obj)
			for _, key := range []string{"record", "error", "usage"} {
				if sub, ok := obj[key].(map[string]any); ok {
					addUnknown(sub)
				}
			}
			growMessages(obj)
			for _, m := range eventMessages(&want) {
				withNewBlock(m)
			}
			grown, err := json.Marshal(obj)
			if err != nil {
				t.Fatal(err)
			}
			var got agentapi.Event
			if err := json.Unmarshal(grown, &got); err != nil {
				t.Errorf("%s: event with new fields does not parse: %v", want.Type, err)
			}
			if !jsonEqual(got, want) {
				t.Errorf("%s: with new fields parses as\n%s\nwant\n%s", want.Type, jsonOf(got), jsonOf(want))
			}
		}
		if name == "tool_run.sse" && len(seen) != 6 || name == "refusal.sse" && !seen[agentapi.TypeRunError] {
			t.Errorf("%s has event types %v", name, seen)
		}
	}
}

// TestResourceJSONIsAdditive: the run and the conversation, with unknown
// fields added at the top, to their messages and blocks, and a block of a new
// type in every message, parse to the same resources plus that block.
func TestResourceJSONIsAdditive(t *testing.T) {
	t.Parallel()
	check := func(t *testing.T, file string, want any, messages []*llm.Message, got any) {
		t.Helper()
		var obj map[string]any
		if err := json.Unmarshal([]byte(readFile(t, filepath.Join("testdata", file))), &obj); err != nil {
			t.Fatal(err)
		}
		addUnknown(obj)
		growMessages(obj)
		if len(messages) == 0 {
			t.Fatalf("%s has no messages", file)
		}
		for _, m := range messages {
			withNewBlock(m)
		}
		grown, err := json.Marshal(obj)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(grown, got); err != nil {
			t.Fatalf("%s with new fields and blocks does not parse: %v", file, err)
		}
		if !jsonEqual(got, want) {
			t.Errorf("%s with new fields and blocks parses as\n%s\nwant\n%s", file, jsonOf(got), jsonOf(want))
		}
	}
	t.Run("run", func(t *testing.T) {
		t.Parallel()
		var want agentapi.Run
		if err := json.Unmarshal([]byte(readFile(t, filepath.Join("testdata", "tool_run.run.json"))), &want); err != nil {
			t.Fatal(err)
		}
		var messages []*llm.Message
		if p := want.Record.Prompt; p != nil {
			messages = append(messages, &p.Message)
		}
		for i := range want.Record.Turns {
			for j := range want.Record.Turns[i].Messages {
				messages = append(messages, &want.Record.Turns[i].Messages[j].Message)
			}
		}
		check(t, "tool_run.run.json", &want, messages, &agentapi.Run{})
	})
	t.Run("conversation", func(t *testing.T) {
		t.Parallel()
		var want agentapi.Conversation
		if err := json.Unmarshal([]byte(readFile(t, filepath.Join("testdata", "tool_run.conversation.json"))), &want); err != nil {
			t.Fatal(err)
		}
		var messages []*llm.Message
		for i := range want.Messages {
			messages = append(messages, &want.Messages[i].Message)
		}
		check(t, "tool_run.conversation.json", &want, messages, &agentapi.Conversation{})
	})
}

// newBlock is a content block of a type a later release may add, and
// newBlockJSON its JSON as json.Marshal writes it inside a grown document.
var (
	newBlock = map[string]any{
		"type": "citation_card", "url": "https://example.com/<a>",
		"added_in_a_later_release": map[string]any{"nested": []any{1.0, "two"}},
	}
	newBlockJSON, _ = json.Marshal(newBlock)
)

// growMessages adds to every message under v (an object with "role" and
// "content") unknown fields, on the message and on each of its blocks, and
// newBlock as its first block; and unknown fields to every timed message (an
// object with "at" and "message").
func growMessages(v any) {
	switch v := v.(type) {
	case map[string]any:
		content, isMessage := v["content"].([]any)
		if _, ok := v["role"]; ok && isMessage {
			for _, b := range content {
				if b, ok := b.(map[string]any); ok {
					addUnknown(b)
				}
			}
			v["content"] = append([]any{newBlock}, content...)
			addUnknown(v)
			return
		}
		for _, x := range v {
			growMessages(x)
		}
		_, hasAt := v["at"]
		if _, ok := v["message"]; ok && hasAt {
			addUnknown(v)
		}
	case []any:
		for _, x := range v {
			growMessages(x)
		}
	}
}

// withNewBlock adds to m what reading a grown message gives: newBlock, kept
// as an llm.Unknown block.
func withNewBlock(m *llm.Message) {
	m.Content = append([]llm.Block{llm.Unknown{Type: "citation_card", Data: newBlockJSON}}, m.Content...)
}

// eventMessages are the messages in e.
func eventMessages(e *agentapi.Event) []*llm.Message {
	var ms []*llm.Message
	if e.Prompt != nil {
		ms = append(ms, &e.Prompt.Message)
	}
	if e.Record != nil {
		for i := range e.Record.Messages {
			ms = append(ms, &e.Record.Messages[i].Message)
		}
	}
	if e.Final != nil {
		ms = append(ms, e.Final)
	}
	return ms
}

func addUnknown(obj map[string]any) {
	obj["added_in_a_later_release"] = map[string]any{"nested": []any{1, "two"}}
	obj["another_new_flag"] = true
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
