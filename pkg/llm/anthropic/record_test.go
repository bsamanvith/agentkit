package anthropic_test

// Fixtures recorded from the real API. Each recording below sends its
// requests through the adapter to a replay server and checks the replies;
// the server serves testdata/<dir>/<name>.http (then <name>.2.http, ...).
//
// To record them again against the real API (after a change to the adapter,
// the SDK, or to pick up a new model), run
//
//	make record-anthropic
//
// which is AGENTKIT_ANTHROPIC_RECORD=1 with ANTHROPIC_API_KEY (or
// ANTHROPIC_KEY) set, running TestRecorded. The server then forwards each
// request to the API with that key, writes the responses as fixtures, and
// the same checks run on the live replies. The adapter itself only ever gets
// a dummy key. The fixtures keep only the status, Content-Type and the body,
// with request IDs replaced; TestFixturesHoldNoAccountData checks every
// fixture for account data. Review the diff, then run the package's tests.
//
// The fixtures the API cannot produce on demand stay hand-written; the
// README next to them says which and why.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/anthropic"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

const (
	recordEnv   = "AGENTKIT_ANTHROPIC_RECORD"
	upstreamAPI = "https://api.anthropic.com"
	// badKey is the key the "error_auth" recording sends: the API answers 401.
	badKey = "agentkit-invalid-key"
)

// recorder forwards requests to the real API and writes the responses.
type recorder struct {
	t     testing.TB
	key   string
	files func(n int) string // the fixture of request n, counting from 1
}

func (rc *recorder) serve(w http.ResponseWriter, r *http.Request, body []byte, n int) {
	raw, err := rc.forward(r, body)
	if err == nil {
		err = os.WriteFile(rc.files(n), raw, 0o644)
	}
	if err != nil {
		rc.t.Errorf("record: request %d: %v", n, err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest) // not retried by the SDK
		fmt.Fprint(w, `{"type":"error","error":{"type":"invalid_request_error","message":"recording failed"}}`)
		return
	}
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(raw)), r)
	if err != nil {
		rc.t.Errorf("record: fixture %d: %v", n, err)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		w.Header()[k] = vs
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// requestIDs matches the API's request IDs, which the fixtures replace.
var requestIDs = regexp.MustCompile(`req_[0-9A-Za-z]{12,}`)

// forward sends the request to the real API with the real key (or badKey,
// if that is the one the adapter sent) and returns the response in fixture
// form: status line, Content-Type, a placeholder Request-Id, then the body
// with its request IDs replaced.
func (rc *recorder) forward(r *http.Request, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamAPI+r.URL.RequestURI(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, vs := range r.Header {
		switch http.CanonicalHeaderKey(k) {
		case "X-Api-Key", "Authorization", "Accept-Encoding", "Content-Length", "Host":
		default:
			req.Header[k] = vs
		}
	}
	key := rc.key
	if r.Header.Get("X-Api-Key") == badKey {
		key = badKey
	}
	req.Header.Set("X-Api-Key", key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "HTTP/1.1 %s\r\nContent-Type: %s\r\nRequest-Id: req_replay\r\n\r\n", resp.Status, resp.Header.Get("Content-Type"))
	b.Write(requestIDs.ReplaceAll(got, []byte("req_replay")))
	return b.Bytes(), nil
}

// recording is one exchange recorded from the real API.
type recording struct {
	dir, name string
	opts      []anthropic.Option
	key       string // the adapter's key; the default is a dummy one
	run       func(t *testing.T, m llm.Model, rp *replay)
}

// newRecording returns a model and the server behind it: a replay of the
// recording's fixtures, or, with recordEnv set, the real API.
func newRecording(t *testing.T, rec recording) (llm.Model, *replay) {
	t.Helper()
	base := filepath.Join("testdata", rec.dir, rec.name)
	file := func(n int) string {
		if n == 1 {
			return base + ".http"
		}
		return fmt.Sprintf("%s.%d.http", base, n)
	}
	if os.Getenv(recordEnv) != "" {
		key := os.Getenv("ANTHROPIC_API_KEY")
		if key == "" {
			key = os.Getenv("ANTHROPIC_KEY")
		}
		if key == "" {
			t.Fatalf("%s needs ANTHROPIC_API_KEY or ANTHROPIC_KEY", recordEnv)
		}
		old, _ := filepath.Glob(base + ".*http")
		for _, f := range old {
			if err := os.Remove(f); err != nil {
				t.Fatal(err)
			}
		}
		rp, s := newReplay(t)
		rp.record = &recorder{t: t, key: key, files: file}
		return recordingModel(t, rec, s.URL), rp
	}
	files := fixtures(t, rec.dir, rec.name)
	rp, s := newReplay(t, files...)
	t.Cleanup(func() {
		if n := len(rp.sent()); n != len(files) {
			t.Errorf("served %d of %d fixtures of %s: the adapter made a different number of requests (re-record with make record-anthropic?)", n, len(files), base)
		}
	})
	return recordingModel(t, rec, s.URL), rp
}

func recordingModel(t *testing.T, rec recording, url string) llm.Model {
	t.Helper()
	key := rec.key
	if key == "" {
		key = "test-key"
	}
	m, err := anthropic.New(append([]anthropic.Option{
		anthropic.WithBaseURL(url),
		anthropic.WithClientOptions(option.WithAPIKey(key), option.WithMaxRetries(0)),
	}, rec.opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m
}

var echoTool = tool.Definition{
	Name:        "echo",
	Description: "Returns the text it is given.",
	InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`),
}

// cachedSystem is a system prompt long enough to be cached (Claude Opus
// needs at least 4096 tokens), and the same in every run.
var cachedSystem = func() string {
	var b strings.Builder
	b.WriteString("You are a test harness. Answer in one short sentence.\n\nReference table, unused:\n")
	for i := range 900 {
		fmt.Fprintf(&b, "row %d: value %d, square %d.\n", i, i*7%1000, i*i)
	}
	return b.String()
}()

var recordings = []recording{
	{dir: "recorded", name: "text", run: func(t *testing.T, m llm.Model, _ *replay) {
		got := generateRecorded(t, m, lowEffort(userText("Say hello in five words.")))
		wantStop(t, got, llm.StopEndTurn)
		if got.Message.Text() == "" || got.Usage.InputTokens == 0 || got.Usage.OutputTokens == 0 {
			t.Errorf("reply = %+v, want text and usage", got)
		}
	}},
	{dir: "recorded", name: "tool_call", run: func(t *testing.T, m llm.Model, rp *replay) {
		req := lowEffort(userText(`Call the echo tool once with the text "pineapple", then tell me what it returned.`))
		req.Tools = []tool.Definition{echoTool}
		first := generateRecorded(t, m, req)
		wantStop(t, first, llm.StopToolUse)
		var results []llm.Block
		for _, b := range first.Message.Content {
			if u, ok := b.(llm.ToolUse); ok {
				var in struct{ Text string }
				if u.Name != "echo" || json.Unmarshal(u.Input, &in) != nil || in.Text != "pineapple" {
					t.Fatalf("tool call %s %s, want echo with pineapple", u.Name, u.Input)
				}
				results = append(results, llm.ToolResult{ToolUseID: u.ID, Content: in.Text})
			}
		}
		if len(results) == 0 {
			t.Fatal("no tool call in a tool_use reply")
		}
		req.Messages = append(req.Messages, first.Message, llm.User(results...))
		second := generateRecorded(t, m, req)
		wantStop(t, second, llm.StopEndTurn)
		if !strings.Contains(strings.ToLower(second.Message.Text()), "pineapple") {
			t.Errorf("answer %q does not give the tool's result", second.Message.Text())
		}
		sentBack(t, first, rp.sent()[1].body)
	}},
	{dir: "recorded", name: "tool_call_no_input", run: func(t *testing.T, m llm.Model, _ *replay) {
		req := lowEffort(userText("What time is it? Use the now tool."))
		req.Tools = []tool.Definition{{
			Name:        "now",
			Description: "Returns the current time. Takes no input.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		}}
		got := generateRecorded(t, m, req)
		wantStop(t, got, llm.StopToolUse)
		for _, b := range got.Message.Content {
			if u, ok := b.(llm.ToolUse); ok && (u.Name != "now" || string(u.Input) != "{}") {
				t.Errorf("tool call %s %s, want now with {}", u.Name, u.Input)
			}
		}
	}},
	{dir: "recorded", name: "max_tokens", run: func(t *testing.T, m llm.Model, _ *replay) {
		req := lowEffort(userText("Count from 1 to 200, one number per line, with no other text."))
		req.MaxTokens = 32
		got := generateRecorded(t, m, req)
		wantStop(t, got, llm.StopMaxTokens)
		if got.Usage.OutputTokens != 32 {
			t.Errorf("output tokens = %d, want the limit, 32", got.Usage.OutputTokens)
		}
	}},
	{dir: "recorded", name: "thinking", run: func(t *testing.T, m llm.Model, rp *replay) {
		req := userText("How many primes are there below 60? Think it through, then answer with the number alone.")
		req.Provider = anthropic.Options{Effort: anthropic.EffortHigh, ThinkingDisplay: anthropic.ThinkingSummarized}
		first := generateRecorded(t, m, req)
		wantStop(t, first, llm.StopEndTurn)
		thinking := 0
		for _, b := range first.Message.Content {
			o, ok := b.(llm.Opaque)
			if !ok {
				continue
			}
			var block struct{ Type, Thinking, Signature string }
			if o.Provider != "anthropic" || json.Unmarshal(o.Data, &block) != nil {
				t.Fatalf("opaque block %s (provider %q)", o.Data, o.Provider)
			}
			if block.Type == "thinking" {
				thinking++
				if block.Thinking == "" || block.Signature == "" {
					t.Errorf("thinking block %s, want a summary and a signature", o.Data)
				}
			}
		}
		if thinking == 0 {
			t.Fatalf("no thinking block in %#v", first.Message.Content)
		}
		// The API accepted the block back (when recording), and it went back
		// byte for byte.
		req.Messages = append(req.Messages, first.Message, llm.User(llm.Text{Text: "And below 70? The number alone."}))
		second := generateRecorded(t, m, req)
		wantStop(t, second, llm.StopEndTurn)
		sentBack(t, first, rp.sent()[1].body)
	}},
	{dir: "recorded", name: "tool_truncated", run: func(t *testing.T, m llm.Model, _ *replay) {
		req := lowEffort(userText("Use write_file to write numbers.txt with the numbers 1 to 400 separated by spaces. Call the tool right away, with no text before it."))
		req.Tools = []tool.Definition{{
			Name:        "write_file",
			Description: "Writes content to the file at path.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"],"additionalProperties":false}`),
		}}
		req.MaxTokens = 200
		events, err := drain(m.Generate(t.Context(), req))
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		checkEvents(t, events)
		for _, ev := range events {
			if ev.Kind == llm.EventToolUseStart || ev.Kind == llm.EventToolInputDelta {
				t.Errorf("event %v for a tool call that was cut off", ev.Kind)
			}
		}
		got := *events[len(events)-1].Response
		wantStop(t, got, llm.StopMaxTokens)
		for _, b := range got.Message.Content {
			if _, ok := b.(llm.ToolUse); ok {
				t.Errorf("the cut-off tool call was kept: %#v", b)
			}
		}
	}},
	{dir: "recorded", name: "cache", run: func(t *testing.T, m llm.Model, _ *replay) {
		req := lowEffort(userText("Say hello."))
		req.System = cachedSystem
		first := generateRecorded(t, m, req)
		if first.Usage.CacheWriteTokens == 0 && first.Usage.CacheReadTokens == 0 {
			t.Errorf("first usage %+v, want the prompt cached", first.Usage)
		}
		second := generateRecorded(t, m, req)
		if second.Usage.CacheReadTokens < 4096 || second.Usage.InputTokens > 1000 {
			t.Errorf("second usage %+v, want the system prompt read from the cache and not counted as input", second.Usage)
		}
	}},
	// The conformance suite only checks the sentinel of its error scenarios,
	// so these are recorded straight into its fixtures.
	{dir: "conformance", name: "error_auth", key: badKey, run: func(t *testing.T, m llm.Model, _ *replay) {
		wantErr(t, m, lowEffort(userText("Hello.")), llm.ErrAuth, http.StatusUnauthorized)
	}},
	{dir: "conformance", name: "error_invalid_request", run: func(t *testing.T, m llm.Model, _ *replay) {
		req := lowEffort(userText("Hello."))
		req.MaxTokens = 10_000_000 // over every model's limit
		wantErr(t, m, req, llm.ErrInvalidRequest, http.StatusBadRequest)
	}},
	{dir: "conformance", name: "error_context_too_long", run: func(t *testing.T, m llm.Model, _ *replay) {
		wantErr(t, m, lowEffort(userText(strings.Repeat("lorem ipsum ", 700_000))), llm.ErrContextTooLong, http.StatusBadRequest)
	}},
	{dir: "errors", name: "not_found", opts: []anthropic.Option{anthropic.WithModel("claude-opus-9")}, run: func(t *testing.T, m llm.Model, _ *replay) {
		wantErr(t, m, lowEffort(userText("Hello.")), llm.ErrInvalidRequest, http.StatusNotFound)
	}},
}

// TestRecorded replays the recordings, or records them (see the top of this
// file).
func TestRecorded(t *testing.T) {
	t.Parallel()
	for _, rec := range recordings {
		t.Run(rec.dir+"/"+rec.name, func(t *testing.T) {
			t.Parallel()
			m, rp := newRecording(t, rec)
			rec.run(t, m, rp)
		})
	}
}

func lowEffort(req llm.Request) llm.Request {
	req.Provider = anthropic.Options{Effort: anthropic.EffortLow}
	return req
}

// generateRecorded runs one request and checks its event stream.
func generateRecorded(t *testing.T, m llm.Model, req llm.Request) llm.Response {
	t.Helper()
	events, err := drain(m.Generate(t.Context(), req))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	checkEvents(t, events)
	return *events[len(events)-1].Response
}

func wantStop(t *testing.T, got llm.Response, kind llm.StopKind) {
	t.Helper()
	if got.Stop.Kind != kind {
		t.Fatalf("stop = %+v, want %s; reply %#v", got.Stop, kind, got.Message.Content)
	}
}

// sentBack checks that every opaque block of reply is in the next request
// byte for byte.
func sentBack(t *testing.T, reply llm.Response, next []byte) {
	t.Helper()
	for i, b := range reply.Message.Content {
		if o, ok := b.(llm.Opaque); ok && !bytes.Contains(next, o.Data) {
			t.Errorf("next request lacks block %d byte for byte: %s", i, o.Data)
		}
	}
}

func wantErr(t *testing.T, m llm.Model, req llm.Request, want error, status int) {
	t.Helper()
	_, err := llm.Collect(m.Generate(t.Context(), req))
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	var apiErr *sdk.Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != status {
		t.Errorf("err = %v, want the SDK error with status %d", err, status)
	}
}

// accountData matches what a fixture must never hold: keys, account and
// organization IDs, and the API's own request IDs.
var accountData = regexp.MustCompile(`(?i)sk-ant-|anthropic-organization-id|organization[-_]id|cf-ray|` + requestIDs.String())

// fixtureHeaders are the only headers a fixture may have.
var fixtureHeaders = map[string]bool{"Content-Type": true, "Content-Length": true, "Request-Id": true, "Retry-After": true, "Retry-After-Ms": true}

// TestFixturesHoldNoAccountData: no fixture, recorded or written by hand,
// carries an API key, account headers or IDs that identify the account.
func TestFixturesHoldNoAccountData(t *testing.T) {
	t.Parallel()
	var keys []string
	for _, env := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_KEY"} {
		if k := os.Getenv(env); len(k) > 8 {
			keys = append(keys, k)
		}
	}
	n := 0
	err := filepath.WalkDir("testdata", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		n++
		if loc := accountData.FindIndex(data); loc != nil {
			t.Errorf("%s holds %q", path, data[loc[0]:loc[1]])
		}
		for _, k := range keys {
			if bytes.Contains(data, []byte(k)) {
				t.Errorf("%s holds the API key", path)
			}
		}
		if filepath.Ext(path) != ".http" {
			return nil
		}
		resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(data)), nil)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			return nil
		}
		_ = resp.Body.Close()
		for h := range resp.Header {
			if !fixtureHeaders[h] {
				t.Errorf("%s has header %s: fixtures keep only %v", path, h, fixtureHeaders)
			}
		}
		if id := resp.Header.Get("Request-Id"); id != "" && id != "req_replay" {
			t.Errorf("%s has Request-Id %q, want req_replay", path, id)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no fixtures found")
	}
}
