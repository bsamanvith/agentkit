package anthropic_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/anthropic"
)

// sseBody answers every request with a 200 event stream holding body.
type sseBody []byte

func (b sseBody) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"text/event-stream"}},
		Body:       io.NopCloser(bytes.NewReader(b)),
		Request:    r,
	}, nil
}

// FuzzStream feeds arbitrary event streams to the adapter: whatever the API
// (or a broken connection) sends, the adapter must not panic, must end with
// either a final message consistent with its events or an error that maps to
// an llm sentinel, and must never hand out a tool call with invalid input.
func FuzzStream(f *testing.F) {
	files, _ := filepath.Glob("testdata/*/*.http")
	more, _ := filepath.Glob("testdata/*.http")
	for _, file := range append(files, more...) {
		data, err := os.ReadFile(file)
		if err != nil {
			f.Fatal(err)
		}
		resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(data)), nil)
		if err != nil {
			f.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		f.Add(body)
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		m, err := anthropic.New(
			anthropic.WithBaseURL("http://replay.invalid"),
			anthropic.WithHTTPClient(&http.Client{Transport: sseBody(body)}),
			anthropic.WithClientOptions(option.WithAPIKey("test-key"), option.WithMaxRetries(0)),
		)
		if err != nil {
			t.Fatal(err)
		}
		events, err := drain(m.Generate(t.Context(), userText("Hello.")))
		if err != nil {
			if !isAny(err, llm.ErrOverloaded, llm.ErrRateLimited, llm.ErrInvalidRequest, llm.ErrAuth, llm.ErrContextTooLong) {
				t.Fatalf("error %v matches no llm sentinel", err)
			}
			return
		}
		checkEvents(t, events)
	})
}

func isAny(err error, targets ...error) bool {
	for _, target := range targets {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// checkEvents checks a successful stream: blocks in order, deltas that add
// up to their blocks, and a final message made of the finished blocks.
func checkEvents(t *testing.T, events []llm.Event) {
	t.Helper()
	var (
		blocks []llm.Block
		text   []byte
	)
	for i, ev := range events[:len(events)-1] {
		if ev.Index != len(blocks) {
			t.Fatalf("event %d: index %d, want %d", i, ev.Index, len(blocks))
		}
		switch ev.Kind {
		case llm.EventTextDelta, llm.EventToolInputDelta:
			text = append(text, ev.Text...)
		case llm.EventToolUseStart:
			text = nil
		case llm.EventBlockDone:
			switch b := ev.Block.(type) {
			case llm.Text:
				if b.Text != string(text) {
					t.Fatalf("event %d: text %q, deltas %q", i, b.Text, text)
				}
			case llm.ToolUse:
				if !json.Valid(b.Input) || !bytes.Equal(b.Input, text) && (len(text) != 0 || string(b.Input) != "{}") {
					t.Fatalf("event %d: tool input %q, deltas %q", i, b.Input, text)
				}
			case llm.Opaque:
				if !json.Valid(b.Data) {
					t.Fatalf("event %d: opaque data %q is not JSON", i, b.Data)
				}
			}
			blocks = append(blocks, ev.Block)
			text = nil
		default:
			t.Fatalf("event %d: kind %v", i, ev.Kind)
		}
	}
	last := events[len(events)-1]
	if last.Kind != llm.EventMessageDone || last.Response.Stop.Kind == "" {
		t.Fatalf("last event = %#v", last)
	}
	if len(blocks) != 0 && !reflect.DeepEqual(last.Response.Message.Content, blocks) {
		t.Fatalf("final content %#v, blocks %#v", last.Response.Message.Content, blocks)
	}
}
