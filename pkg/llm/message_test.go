package llm_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// signature is a long, deterministic base64 string, like the signature of a
// Claude thinking block (which is sent back and must not change).
func signature() string {
	raw := make([]byte, 900)
	x := uint32(1)
	for i := range raw {
		x = x*1664525 + 1013904223
		raw[i] = byte(x >> 24)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// opaqueData has what encoding/json would rewrite: HTML characters, U+2028,
// non-ASCII text, unsorted keys and whitespace.
func opaqueData() []json.RawMessage {
	return []json.RawMessage{
		json.RawMessage(`{"type":"thinking","thinking":"if a < b && c > d then <b>naïve</b> ✓ 日本語 \u2603 ` + "\u2028" + `end","signature":"` + signature() + `"}`),
		json.RawMessage("{\n  \"signature\": \"AbC+/9==\",\n  \"type\": \"redacted_thinking\",\n  \"data\": [1, 2.50, true, null]\n}"),
	}
}

func sampleMessages() []llm.Message {
	data := opaqueData()
	return []llm.Message{
		llm.User(llm.Text{Text: "Search for Go <iterators> & tell me about it. Ünïcode ✓"}),
		llm.Assistant(
			llm.Opaque{Provider: "anthropic", Data: data[0]},
			llm.Opaque{Provider: "anthropic", Data: data[1]},
			llm.Text{Text: "I'll look that up."},
			llm.ToolUse{ID: "toolu_01", Name: "search", Input: json.RawMessage(`{"query":"go iterators","limit":3}`)},
			llm.ToolUse{ID: "toolu_02", Name: "now", Input: json.RawMessage(`{}`)},
		),
		llm.User(
			llm.ToolResult{ToolUseID: "toolu_01", Content: `{"results":["iter.Seq"]}`},
			llm.ToolResult{ToolUseID: "toolu_02", Content: "tool \"now\" failed: no clock", IsError: true},
		),
		llm.Assistant(),
	}
}

func TestMessageJSONRoundTrip(t *testing.T) {
	t.Parallel()
	msgs := sampleMessages()

	// The wire format is pinned by a golden file.
	got, err := json.MarshalIndent(msgs, "", "  ")
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	testkit.Golden(t, "testdata/messages.json", append(got, '\n'))

	golden, err := os.ReadFile("testdata/messages.json")
	if err != nil {
		t.Fatal(err)
	}
	var decoded []llm.Message
	if err := json.Unmarshal(golden, &decoded); err != nil {
		t.Fatalf("Unmarshal golden: %v", err)
	}
	// An empty assistant message comes back with nil content, as it went in.
	if !reflect.DeepEqual(decoded, msgs) {
		t.Errorf("round trip changed the messages:\n got %#v\nwant %#v", decoded, msgs)
	}

	// Opaque data survives byte for byte under every encoder setting.
	encoders := map[string]func(v any) []byte{
		"Marshal": func(v any) []byte {
			b, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			return b
		},
		"MarshalIndent": func(v any) []byte {
			b, err := json.MarshalIndent(v, "", "\t")
			if err != nil {
				t.Fatal(err)
			}
			return b
		},
		"Encoder without HTML escaping": func(v any) []byte {
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			enc.SetEscapeHTML(false)
			if err := enc.Encode(v); err != nil {
				t.Fatal(err)
			}
			return buf.Bytes()
		},
	}
	for name, encode := range encoders {
		var back []llm.Message
		if err := json.Unmarshal(encode(msgs), &back); err != nil {
			t.Fatalf("%s: Unmarshal: %v", name, err)
		}
		for i, want := range opaqueData() {
			o, ok := back[1].Content[i].(llm.Opaque)
			if !ok || !bytes.Equal(o.Data, want) {
				t.Errorf("%s: opaque block %d data changed:\n got %s\nwant %s", name, i, o.Data, want)
			}
		}
	}
}

func TestToolUseInputIsSemanticallyPreserved(t *testing.T) {
	t.Parallel()
	in := llm.Assistant(llm.ToolUse{ID: "t", Name: "n", Input: json.RawMessage("{ \"a\" : \"<&>\",\n \"b\": [1, 2] }")})
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out llm.Message
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	got := out.Content[0].(llm.ToolUse).Input
	var a, b any
	if err := json.Unmarshal(in.Content[0].(llm.ToolUse).Input, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got, &b); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Errorf("input %s means something else than the original", got)
	}
}

// An omitted or null tool input is written as an empty object, the same thing
// a decoded message has, so a message encodes to what it decodes from.
func TestToolUseWithoutInputMarshalsAsEmptyObject(t *testing.T) {
	t.Parallel()
	for name, input := range map[string]json.RawMessage{"nil": nil, "null": json.RawMessage(`null`)} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data, err := json.Marshal(llm.Assistant(llm.ToolUse{ID: "t", Name: "n", Input: input}))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(data, []byte(`"input":{}`)) {
				t.Errorf("encoded as %s, want an empty object as input", data)
			}
			var back llm.Message
			if err := json.Unmarshal(data, &back); err != nil {
				t.Fatal(err)
			}
			if got := string(back.Content[0].(llm.ToolUse).Input); got != "{}" {
				t.Errorf("input = %s, want {}", got)
			}
		})
	}
}

// Clones share no memory with the original, down to the bytes of raw JSON.
func TestRequestCloneIsDeep(t *testing.T) {
	t.Parallel()
	req := llm.Request{
		Messages: []llm.Message{
			llm.Assistant(
				llm.ToolUse{ID: "t", Name: "n", Input: json.RawMessage(`{"a":1}`)},
				llm.Opaque{Provider: "p", Data: json.RawMessage(`{"b":2}`)},
				llm.Unknown{Type: "image", Data: json.RawMessage(`{"type":"image"}`)},
			),
		},
		Tools: []tool.Definition{{Name: "n", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	}
	clone := req.Clone()
	clone.Messages[0].Content[0].(llm.ToolUse).Input[2] = 'X'
	clone.Messages[0].Content[1].(llm.Opaque).Data[2] = 'X'
	clone.Messages[0].Content[2].(llm.Unknown).Data[2] = 'X'
	clone.Messages[0].Content[0] = llm.Text{Text: "replaced"}
	clone.Tools[0].InputSchema[2] = 'X'

	if got := string(req.Messages[0].Content[0].(llm.ToolUse).Input); got != `{"a":1}` {
		t.Errorf("tool input changed to %s", got)
	}
	if got := string(req.Messages[0].Content[1].(llm.Opaque).Data); got != `{"b":2}` {
		t.Errorf("opaque data changed to %s", got)
	}
	if got := string(req.Messages[0].Content[2].(llm.Unknown).Data); got != `{"type":"image"}` {
		t.Errorf("unknown block's data changed to %s", got)
	}
	if got := string(req.Tools[0].InputSchema); got != `{"type":"object"}` {
		t.Errorf("tool schema changed to %s", got)
	}
}

func TestMarshalRejectsInvalidMessages(t *testing.T) {
	t.Parallel()
	for name, msg := range map[string]llm.Message{
		"no role":              {Content: []llm.Block{llm.Text{Text: "x"}}},
		"nil block":            llm.User(nil),
		"opaque without data":  llm.Assistant(llm.Opaque{Provider: "p"}),
		"opaque bad data":      llm.Assistant(llm.Opaque{Provider: "p", Data: json.RawMessage(`{"a":`)}),
		"opaque no provider":   llm.Assistant(llm.Opaque{Data: json.RawMessage(`{}`)}),
		"opaque not utf-8":     llm.Assistant(llm.Opaque{Provider: "p", Data: json.RawMessage("\"\xff\"")}),
		"tool use bad input":   llm.Assistant(llm.ToolUse{ID: "t", Name: "n", Input: json.RawMessage(`{`)}),
		"tool use bad input 2": llm.Assistant(llm.ToolUse{ID: "t", Name: "n", Input: json.RawMessage(`{} {}`)}),
		"tool use no id":       llm.Assistant(llm.ToolUse{Name: "n"}),
		"tool use no name":     llm.Assistant(llm.ToolUse{ID: "t"}),
		"tool result no id":    llm.User(llm.ToolResult{Content: "x"}),
		"pointer block":        llm.Assistant(&llm.Text{Text: "x"}),
		"typed nil block":      llm.Assistant((*llm.Text)(nil)),
		"unknown without type": llm.Assistant(llm.Unknown{Data: json.RawMessage(`{}`)}),
		"unknown, known type":  llm.Assistant(llm.Unknown{Type: "text", Data: json.RawMessage(`{"type":"text","text":"a"}`)}),
		"unknown, other type":  llm.Assistant(llm.Unknown{Type: "image", Data: json.RawMessage(`{"type":"audio"}`)}),
		"unknown, no type":     llm.Assistant(llm.Unknown{Type: "image", Data: json.RawMessage(`{"source":"x"}`)}),
		"unknown not object":   llm.Assistant(llm.Unknown{Type: "image", Data: json.RawMessage(`"image"`)}),
		"unknown bad data":     llm.Assistant(llm.Unknown{Type: "image", Data: json.RawMessage(`{"type":"image"`)}),
		"unknown not utf-8":    llm.Assistant(llm.Unknown{Type: "image", Data: json.RawMessage("{\"type\":\"image\",\"x\":\"\xff\"}")}),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := json.Marshal(msg); !errors.Is(err, llm.ErrInvalidMessage) {
				t.Errorf("err = %v, want ErrInvalidMessage", err)
			}
		})
	}
}

func TestUnknownBlockType(t *testing.T) {
	t.Parallel()
	for name, doc := range map[string]string{
		"unknown type":      `{"role":"user","content":[{"type":"text","text":"a"},{"type":"image","source":"x"}]}`,
		"no type":           `{"role":"user","content":[{"text":"a"}]}`,
		"null block":        `{"role":"user","content":[null]}`,
		"not an object":     `{"role":"user","content":["a"]}`,
		"unknown role":      `{"role":"system","content":[]}`,
		"no role":           `{"content":[]}`,
		"unknown field":     `{"role":"user","content":[{"type":"text","text":"a","cache":true}]}`,
		"unknown message":   `{"role":"user","content":[],"name":"x"}`,
		"tool use no id":    `{"role":"assistant","content":[{"type":"tool_use","name":"n","input":{}}]}`,
		"tool use no name":  `{"role":"assistant","content":[{"type":"tool_use","id":"t","input":{}}]}`,
		"tool result no id": `{"role":"user","content":[{"type":"tool_result","content":"x"}]}`,
		"opaque as value":   `{"role":"assistant","content":[{"type":"opaque","provider":"p","data":{"a":1}}]}`,
		"opaque bad data":   `{"role":"assistant","content":[{"type":"opaque","provider":"p","data":"{\"a\":"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var m llm.Message
			err := json.Unmarshal([]byte(doc), &m)
			if err == nil {
				t.Fatalf("decoded %#v, want an error", m)
			}
			if !errors.Is(err, llm.ErrInvalidMessage) {
				t.Errorf("err = %v, want ErrInvalidMessage", err)
			}
		})
	}
	var m llm.Message // called directly, so encoding/json does not reject the trailing data first
	if err := m.UnmarshalJSON([]byte(`{"role":"user","content":[]} {}`)); !errors.Is(err, llm.ErrInvalidMessage) {
		t.Errorf("trailing data: err = %v, want ErrInvalidMessage", err)
	}
	if _, err := llm.UnmarshalBlock([]byte(`{"type":"thinking"}`)); !errors.Is(err, llm.ErrInvalidMessage) {
		t.Errorf("UnmarshalBlock: err = %v, want ErrInvalidMessage", err)
	}
}

// TestUnmarshalMessageLenient: what a newer version may add (fields on a
// message or a block, block types) is read, unknown fields ignored and an
// unknown block kept as it was; what is malformed is still an error; and
// for what strict decoding accepts, both agree.
func TestUnmarshalMessageLenient(t *testing.T) {
	t.Parallel()
	image := `{"type":"image", "source":{"url":"https://x/<a>.png"},"alt":"a \u003c b"}`
	for _, tt := range []struct {
		name, doc string
		want      llm.Message
	}{
		{"field on the message", `{"role":"user","content":[{"type":"text","text":"a"}],"name":"x","meta":{"a":[1]}}`, llm.User(llm.Text{Text: "a"})},
		{"field on text", `{"role":"user","content":[{"type":"text","text":"a","citations":[{"url":"u"}]}]}`, llm.User(llm.Text{Text: "a"})},
		{"field on tool_use", `{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"n","input":{"q":1},"caller":"x"}]}`, llm.Assistant(llm.ToolUse{ID: "t", Name: "n", Input: json.RawMessage(`{"q":1}`)})},
		{"field on tool_result", `{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"r","is_error":true,"images":[]}]}`, llm.User(llm.ToolResult{ToolUseID: "t", Content: "r", IsError: true})},
		{"field on opaque", `{"role":"assistant","content":[{"type":"opaque","provider":"p","data":"{\"a\":1}","kind":"thinking"}]}`, llm.Assistant(llm.Opaque{Provider: "p", Data: json.RawMessage(`{"a":1}`)})},
		{"block type", `{"role":"assistant","content":[{"type":"text","text":"see"},` + image + `,{"type":"text","text":"done"}]}`, llm.Assistant(
			llm.Text{Text: "see"}, llm.Unknown{Type: "image", Data: json.RawMessage(image)}, llm.Text{Text: "done"},
		)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := llm.UnmarshalMessageLenient([]byte(tt.doc))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("decoded %#v, want %#v", got, tt.want)
			}
			var strict llm.Message
			if err := json.Unmarshal([]byte(tt.doc), &strict); !errors.Is(err, llm.ErrInvalidMessage) {
				t.Errorf("strict decoding: err = %v, want ErrInvalidMessage", err)
			}
			out := mustJSON(t, got)
			again, err := llm.UnmarshalMessageLenient(out)
			if err != nil {
				t.Fatalf("own output %s: %v", out, err)
			}
			if out2 := mustJSON(t, again); !bytes.Equal(out, out2) {
				t.Errorf("encoding is not stable:\n first %s\nsecond %s", out, out2)
			}
			for _, b := range got.Content {
				if u, ok := b.(llm.Unknown); ok && !jsonSame(t, mustJSON(t, u), []byte(image)) {
					t.Errorf("unknown block written as %s, read as %s", mustJSON(t, u), image)
				}
			}
		})
	}
	for name, doc := range map[string]string{
		"invalid JSON":      `{"role":"user","content":[`,
		"no type":           `{"role":"user","content":[{"text":"a"}]}`,
		"empty type":        `{"role":"user","content":[{"type":"","text":"a"}]}`,
		"type not a string": `{"role":"user","content":[{"type":7}]}`,
		"null block":        `{"role":"user","content":[null]}`,
		"not an object":     `{"role":"user","content":["a"]}`,
		"unknown role":      `{"role":"system","content":[]}`,
		"no role":           `{"content":[]}`,
		"null":              `null`,
		"tool use no id":    `{"role":"assistant","content":[{"type":"tool_use","name":"n","input":{}}]}`,
		"tool result no id": `{"role":"user","content":[{"type":"tool_result","content":"x"}]}`,
		"opaque as value":   `{"role":"assistant","content":[{"type":"opaque","provider":"p","data":{"a":1}}]}`,
		"opaque bad data":   `{"role":"assistant","content":[{"type":"opaque","provider":"p","data":"{\"a\":"}]}`,
		"trailing data":     `{"role":"user","content":[]} {}`,
	} {
		t.Run("malformed/"+name, func(t *testing.T) {
			t.Parallel()
			if m, err := llm.UnmarshalMessageLenient([]byte(doc)); !errors.Is(err, llm.ErrInvalidMessage) {
				t.Errorf("decoded %#v, err = %v; want ErrInvalidMessage", m, err)
			}
		})
	}
	t.Run("agrees with strict decoding", func(t *testing.T) {
		t.Parallel()
		for _, m := range sampleMessages() {
			data := mustJSON(t, m)
			var strict llm.Message
			if err := json.Unmarshal(data, &strict); err != nil {
				t.Fatal(err)
			}
			got, err := llm.UnmarshalMessageLenient(data)
			if err != nil || !reflect.DeepEqual(got, strict) {
				t.Errorf("%s: lenient %#v (%v), strict %#v", data, got, err, strict)
			}
		}
	})
	t.Run("strict decoding rejects an unknown block", func(t *testing.T) {
		t.Parallel()
		data := mustJSON(t, llm.Assistant(llm.Unknown{Type: "image", Data: json.RawMessage(image)}))
		var m llm.Message
		if err := json.Unmarshal(data, &m); !errors.Is(err, llm.ErrInvalidMessage) {
			t.Errorf("decoded %#v, err = %v; want ErrInvalidMessage", m, err)
		}
	})
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// jsonSame reports whether a and b are the same JSON value.
func jsonSame(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(x, y)
}

func FuzzMessageUnmarshal(f *testing.F) {
	golden, err := os.ReadFile("testdata/messages.json")
	if err != nil {
		f.Fatal(err)
	}
	var all []json.RawMessage
	if err := json.Unmarshal(golden, &all); err != nil {
		f.Fatal(err)
	}
	for _, m := range all {
		f.Add([]byte(m))
	}
	for _, seed := range []string{
		``, `null`, `{}`, `[]`, `{"role":"user"}`, `{"role":"user","content":null}`,
		`{"role":"user","content":[{"type":"tool_use","id":"a","name":"b"}]}`,
		`{"role":"user","content":[{"type":"tool_use","id":"a","name":"b","input":null}]}`,
		`{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"b","input":{ "x" : "<&>\u2028" }}]}`,
		`{"role":"assistant","content":[{"type":"opaque","provider":"p","data":"[ \"\\u003c\" ]"}]}`,
		`{"role":"assistant","content":[{"type":"opaque","provider":"p","data":"\"\\ud800\""}]}`,
		`{"role":"user","content":[{"type":"text","text":"\ud800"}]}`,
		`{"role":"user","content":[{"type":"text","text":"a","new":1},{"type":"image","src":{"u":"<x>"}}],"name":"x"}`,
		`{"role":"user","content":[{"type":"image","x":"\ud800"}]}`,
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		lenient, lerr := llm.UnmarshalMessageLenient(data)
		if lerr == nil {
			// Whatever lenient decoding accepts, it can write and read back.
			out := mustJSON(t, lenient)
			again, err := llm.UnmarshalMessageLenient(out)
			if err != nil {
				t.Fatalf("lenient: cannot decode own output %q: %v", out, err)
			}
			if out2 := mustJSON(t, again); !bytes.Equal(out, out2) {
				t.Fatalf("lenient: encoding is not stable:\n first %q\nsecond %q", out, out2)
			}
		}
		var m *llm.Message
		if err := json.Unmarshal(data, &m); err != nil || m == nil {
			return // invalid input must be an error, not a panic
		}
		if lerr != nil || !reflect.DeepEqual(lenient, *m) {
			t.Fatalf("strict decoding gives %#v, lenient %#v (%v)", *m, lenient, lerr)
		}
		out, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("decoded %q but cannot encode %#v: %v", data, m, err)
		}
		var again *llm.Message
		if err := json.Unmarshal(out, &again); err != nil {
			t.Fatalf("cannot decode own output %q: %v", out, err)
		}
		if !reflect.DeepEqual(m, again) {
			t.Fatalf("round trip changed the message:\n first %#v\nsecond %#v", m, again)
		}
		if out2, err := json.Marshal(again); err != nil || !bytes.Equal(out, out2) {
			t.Fatalf("encoding is not stable:\n first %q\nsecond %q (%v)", out, out2, err)
		}
	})
}
