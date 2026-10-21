package fakellm_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/google/go-cmp/cmp"

	"github.com/bsamanvith/agentkit/internal/fakellm"
	"github.com/bsamanvith/agentkit/internal/testkit"
)

// fixture is a recorded answer, in the replay tests' format.
const fixture = "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream; charset=utf-8\r\nRequest-Id: req_replay\r\n\r\n" +
	"event: message_start\ndata: {\"type\":\"message_start\"}\n\n"

// newServer serves the given scripts (name: text); the first is the default.
// Fixtures come from files.
func newServer(t *testing.T, files fstest.MapFS, scripts ...string) *httptest.Server {
	t.Helper()
	m := map[string]*fakellm.Script{}
	var def string
	for i := 0; i+1 < len(scripts); i += 2 {
		s, err := fakellm.ParseScript(scripts[i], strings.NewReader(scripts[i+1]), files)
		if err != nil {
			t.Fatal(err)
		}
		m[scripts[i]] = s
		if def == "" {
			def = scripts[i]
		}
	}
	h, err := fakellm.New(m, fakellm.WithDefault(def))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

type msg = map[string]any

func user(content any) msg      { return msg{"role": "user", "content": content} }
func assistant(content any) msg { return msg{"role": "assistant", "content": content} }

func request(msgs ...msg) msg {
	return msg{"model": "claude-test", "max_tokens": 100, "messages": msgs}
}

// result is a server's answer to one request.
type result struct {
	status int
	header http.Header
	body   []byte
}

func post(t *testing.T, url string, req any, header ...string) result {
	t.Helper()
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	hr, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url+"/v1/messages", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	hr.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(header); i += 2 {
		hr.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(hr)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return result{resp.StatusCode, resp.Header, body}
}

// exchange sends req to the server streaming and not, checks the invariants
// every answer keeps, and returns the non-streaming answer: the message, or
// the error with its status.
//
// Invariants: a 200 is a well-formed message (blocks, unique tool ids, a
// known stop reason, tool_use exactly when it asks for tools); its stream is
// a valid event sequence that adds up to the same message; an error is the
// API's error shape with a 4xx and a message, the same streaming or not; the
// same request gets the same bytes again.
func exchange(t *testing.T, url string, req msg, header ...string) (int, msg) {
	t.Helper()
	plain := post(t, url, req, header...)
	if again := post(t, url, req, header...); !bytes.Equal(again.body, plain.body) {
		t.Errorf("the same request got another answer:\n%s\n%s", plain.body, again.body)
	}
	sreq := msg{"stream": true}
	for k, v := range req {
		sreq[k] = v
	}
	stream := post(t, url, sreq, header...)
	if stream.status != plain.status {
		t.Fatalf("status %d streaming, %d not", stream.status, plain.status)
	}
	var got msg
	if err := json.Unmarshal(plain.body, &got); err != nil {
		t.Fatalf("answer is not JSON: %v\n%s", err, plain.body)
	}
	if plain.status != http.StatusOK {
		checkError(t, plain)
		checkError(t, stream)
		return plain.status, got
	}
	checkMessage(t, got)
	if ct := stream.header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("stream Content-Type %q", ct)
	}
	if diff := cmp.Diff(got, accumulate(t, stream.body)); diff != "" {
		t.Errorf("the stream adds up to another message (-plain +stream):\n%s", diff)
	}
	return plain.status, got
}

func checkError(t *testing.T, r result) {
	t.Helper()
	if r.status < 400 || r.status > 499 {
		t.Errorf("error status %d, want 4xx", r.status)
	}
	var e struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(r.body, &e); err != nil || e.Type != "error" || e.Error.Type == "" || e.Error.Message == "" {
		t.Errorf("not an API error body (%v): %s", err, r.body)
	}
}

var stopReasons = []string{"end_turn", "max_tokens", "stop_sequence", "tool_use", "pause_turn", "refusal"}

func checkMessage(t *testing.T, m msg) {
	t.Helper()
	if m["type"] != "message" || m["role"] != "assistant" || m["id"] == "" || m["model"] == "" {
		t.Errorf("not a message: %v", m)
	}
	stop, _ := m["stop_reason"].(string)
	if !slices.Contains(stopReasons, stop) {
		t.Errorf("stop_reason %v", m["stop_reason"])
	}
	content, _ := m["content"].([]any)
	if len(content) == 0 {
		t.Errorf("no content: %v", m)
	}
	ids := map[string]bool{}
	tools := 0
	for _, c := range content {
		b, _ := c.(msg)
		switch b["type"] {
		case "text":
			if s, _ := b["text"].(string); s == "" {
				t.Errorf("empty text block: %v", b)
			}
		case "tool_use":
			tools++
			id, _ := b["id"].(string)
			if !strings.HasPrefix(id, "toolu_") || ids[id] {
				t.Errorf("tool_use id %q missing, malformed or repeated", id)
			}
			ids[id] = true
			if _, ok := b["input"].(msg); !ok || b["name"] == "" {
				t.Errorf("tool_use without a name or an object input: %v", b)
			}
		default:
			t.Errorf("block type %v", b["type"])
		}
	}
	if (tools > 0) != (stop == "tool_use") {
		t.Errorf("%d tool calls with stop_reason %s", tools, stop)
	}
	if u, _ := m["usage"].(msg); u == nil || u["input_tokens"] == nil || u["output_tokens"] == nil {
		t.Errorf("usage %v", m["usage"])
	}
}

// accumulate checks that body is a valid Messages API event stream and
// returns the message it adds up to.
func accumulate(t *testing.T, body []byte) msg {
	t.Helper()
	var (
		m      msg
		blocks []msg
		parts  []string // tool input JSON per block
		open   = -1
		names  []string
	)
	sc := bufio.NewScanner(bytes.NewReader(body))
	var name string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
			continue
		case line == "":
			continue
		case !strings.HasPrefix(line, "data: "):
			t.Fatalf("stream line %q", line)
		}
		var ev msg
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			t.Fatalf("event data: %v", err)
		}
		if ev["type"] != name {
			t.Fatalf("event %q has data of type %v", name, ev["type"])
		}
		names = append(names, name)
		if len(names) == 1 && name != "message_start" {
			t.Fatalf("first event %s", name)
		}
		idx := -1
		if f, ok := ev["index"].(float64); ok {
			idx = int(f)
		}
		switch name {
		case "message_start":
			if m != nil {
				t.Fatal("second message_start")
			}
			m, _ = ev["message"].(msg)
			if c, _ := m["content"].([]any); len(c) != 0 || m["stop_reason"] != nil {
				t.Fatalf("message_start with content or a stop reason: %v", m)
			}
		case "ping":
		case "content_block_start":
			if open != -1 || idx != len(blocks) {
				t.Fatalf("block %d starts while %d is open, after %d blocks", idx, open, len(blocks))
			}
			b, _ := ev["content_block"].(msg)
			blocks, parts, open = append(blocks, b), append(parts, ""), idx
		case "content_block_delta":
			if idx != open {
				t.Fatalf("delta for block %d, open %d", idx, open)
			}
			d, _ := ev["delta"].(msg)
			switch {
			case d["type"] == "text_delta" && blocks[idx]["type"] == "text":
				blocks[idx]["text"] = blocks[idx]["text"].(string) + d["text"].(string)
			case d["type"] == "input_json_delta" && blocks[idx]["type"] == "tool_use":
				parts[idx] += d["partial_json"].(string)
			default:
				t.Fatalf("delta %v for block %v", d, blocks[idx])
			}
		case "content_block_stop":
			if idx != open {
				t.Fatalf("stop for block %d, open %d", idx, open)
			}
			if blocks[idx]["type"] == "tool_use" && parts[idx] != "" {
				var in msg
				if err := json.Unmarshal([]byte(parts[idx]), &in); err != nil {
					t.Fatalf("tool input %q: %v", parts[idx], err)
				}
				blocks[idx]["input"] = in
			}
			open = -1
		case "message_delta":
			if open != -1 {
				t.Fatalf("message_delta with block %d open", open)
			}
			d, _ := ev["delta"].(msg)
			m["stop_reason"] = d["stop_reason"]
			m["stop_sequence"] = d["stop_sequence"]
			u, _ := ev["usage"].(msg)
			m["usage"].(msg)["output_tokens"] = u["output_tokens"]
		case "message_stop":
		default:
			t.Fatalf("event %s", name)
		}
	}
	if len(names) == 0 || names[len(names)-1] != "message_stop" || slices.Index(names, "message_delta") != len(names)-2 {
		t.Fatalf("stream does not end with message_delta, message_stop: %v", names)
	}
	content := make([]any, len(blocks))
	for i, b := range blocks {
		content[i] = b
	}
	m["content"] = content
	return m
}

func content(t *testing.T, m msg) []msg {
	t.Helper()
	var out []msg
	for _, c := range m["content"].([]any) {
		out = append(out, c.(msg))
	}
	return out
}

// Criterion 1: a matching entry returns its text in the API's shape; the
// first match wins, and the fallback answers the rest wherever it is.
func TestTextEntry(t *testing.T) {
	t.Parallel()
	srv := newServer(t, nil, "s", `
# comment
{"response": {"text": "fallback"}}
{"match": "hello", "response": {"text": "Hi there."}}
{"match": "hello world", "response": {"text": "never: the line above matches first"}}
{"match": "bye", "response": {"text": "Bye.", "stop_reason": "max_tokens"}}
`)
	for _, tc := range []struct {
		name, said, want, stop string
	}{
		{"match", "well hello world", "Hi there.", "end_turn"},
		{"stop reason", "bye now", "Bye.", "max_tokens"},
		{"fallback", "something else", "fallback", "end_turn"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			status, m := exchange(t, srv.URL, request(user(tc.said)))
			if status != http.StatusOK {
				t.Fatalf("status %d: %v", status, m)
			}
			want := []msg{{"type": "text", "text": tc.want}}
			if diff := cmp.Diff(want, content(t, m)); diff != "" || m["stop_reason"] != tc.stop || m["model"] != "claude-test" {
				t.Errorf("content (-want +got):\n%s\nstop %v, model %v", diff, m["stop_reason"], m["model"])
			}
		})
	}
}

// Criterion 2: a tool-call entry returns tool_use blocks, and the request
// that carries their result matches the next entry, on the result's text.
func TestToolCallRoundTrip(t *testing.T) {
	t.Parallel()
	srv := newServer(t, nil, "s", `
{"match": "sunny", "response": {"text": "It is sunny in Paris."}}
{"match": "weather", "response": {"text": "Let me look.", "tool_calls": [{"name": "get_weather", "input": {"city": "Paris"}}, {"id": "toolu_given", "name": "clock"}]}}
`)
	first := request(user([]msg{{"type": "text", "text": "What is the weather?"}}))
	status, m := exchange(t, srv.URL, first)
	if status != http.StatusOK {
		t.Fatalf("status %d: %v", status, m)
	}
	blocks := content(t, m)
	if len(blocks) != 3 || blocks[0]["text"] != "Let me look." ||
		blocks[1]["name"] != "get_weather" || !cmp.Equal(blocks[1]["input"], msg{"city": "Paris"}) ||
		blocks[2]["id"] != "toolu_given" || !cmp.Equal(blocks[2]["input"], msg{}) {
		t.Fatalf("blocks %v", blocks)
	}
	// Another conversation asking for the same call gets other ids: tool ids
	// stay unique when a client keeps both in one transcript.
	_, other := exchange(t, srv.URL, request(user("And the weather tomorrow?")))
	if id := content(t, other)[1]["id"]; id == blocks[1]["id"] {
		t.Errorf("two conversations got tool id %v", id)
	}
	// The result as a string and as text blocks.
	for _, res := range []any{`{"forecast":"sunny"}`, []msg{{"type": "text", "text": "sunny"}}} {
		follow := request(user("What is the weather?"), assistant(m["content"]), user([]msg{
			{"type": "tool_result", "tool_use_id": blocks[1]["id"], "content": res},
			{"type": "tool_result", "tool_use_id": "toolu_given", "content": "12:00"},
		}))
		status, m2 := exchange(t, srv.URL, follow)
		if status != http.StatusOK {
			t.Fatalf("status %d: %v", status, m2)
		}
		if got := content(t, m2); len(got) != 1 || got[0]["text"] != "It is sunny in Paris." {
			t.Errorf("follow-up answer %v", got)
		}
	}
}

// Criterion 3: stream: true is a valid event sequence, byte for byte the
// golden file (exchange checks the sequence itself).
func TestStreamGolden(t *testing.T) {
	t.Parallel()
	srv := newServer(t, nil, "s", `{"response": {"text": "Hi, 日本語", "tool_calls": [{"name": "echo", "input": {"text": "hi"}}]}}`)
	req := request(user("hi"))
	if status, m := exchange(t, srv.URL, req); status != http.StatusOK {
		t.Fatalf("status %d: %v", status, m)
	}
	req["stream"] = true
	r := post(t, srv.URL, req)
	testkit.Golden(t, filepath.Join("testdata", "stream.golden"), r.body)
}

// Criterion 4: no match and no fallback is a 4xx with an error body that
// says so; so are an unknown script and a request that is not one.
func TestNoAnswerIsAClientError(t *testing.T) {
	t.Parallel()
	srv := newServer(t, nil, "s", `{"match": "hello", "response": {"text": "Hi."}}`)
	for _, tc := range []struct {
		name   string
		req    msg
		header []string
		want   string
	}{
		{"no match", request(user("goodbye")), nil, "no entry matches"},
		{"unknown script", request(user("hello")), []string{fakellm.ScriptHeader, "nope"}, `no script "nope"`},
		{"no user message", request(assistant("hello")), nil, "no user message"},
		{"no messages", request(), nil, "at least one message"},
		{"bad content", request(user(42)), nil, "content"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			status, m := exchange(t, srv.URL, tc.req, tc.header...)
			if status != http.StatusBadRequest {
				t.Fatalf("status %d, want 400: %v", status, m)
			}
			// The message is for a person reading a failed test's log.
			if e, _ := m["error"].(msg); !strings.Contains(e["message"].(string), tc.want) {
				t.Errorf("error %v does not say %q", e, tc.want)
			}
		})
	}
	r := post(t, srv.URL, "not a request")
	checkError(t, r)
}

// The X-Fakellm-Script header picks the script; without it, the default.
func TestScriptHeader(t *testing.T) {
	t.Parallel()
	srv := newServer(t, nil, "a", `{"response": {"text": "from a"}}`, "b", `{"response": {"text": "from b"}}`)
	for _, tc := range []struct {
		header []string
		want   string
	}{
		{nil, "from a"},
		{[]string{fakellm.ScriptHeader, "b"}, "from b"},
		{[]string{fakellm.ScriptHeader, "a"}, "from a"},
	} {
		if _, m := exchange(t, srv.URL, request(user("hi")), tc.header...); content(t, m)[0]["text"] != tc.want {
			t.Errorf("header %v: %v, want %q", tc.header, m, tc.want)
		}
	}
}

// A fixture entry serves the recorded response as it was recorded.
func TestFixture(t *testing.T) {
	t.Parallel()
	files := fstest.MapFS{"rec/01.http": {Data: []byte(fixture)}}
	srv := newServer(t, files, "s", `{"match": "replay", "fixture": "rec/01.http"}`)
	r := post(t, srv.URL, request(user("replay please")))
	want := "event: message_start\ndata: {\"type\":\"message_start\"}\n\n"
	if r.status != http.StatusOK || string(r.body) != want ||
		r.header.Get("Request-Id") != "req_replay" || !strings.HasPrefix(r.header.Get("Content-Type"), "text/event-stream") {
		t.Errorf("got %d %v %q", r.status, r.header, r.body)
	}
}

func TestParseScriptErrors(t *testing.T) {
	t.Parallel()
	files := fstest.MapFS{"bad.http": {Data: []byte("not http")}}
	for name, script := range map[string]string{
		"empty":             "# nothing\n",
		"not json":          `{"match": `,
		"unknown field":     `{"matc": "x", "response": {"text": "y"}}`,
		"two values":        `{"response": {"text": "y"}} {}`,
		"empty match":       `{"match": "", "response": {"text": "y"}}`,
		"neither":           `{"match": "x"}`,
		"both":              `{"match": "x", "response": {"text": "y"}, "fixture": "f.http"}`,
		"empty response":    `{"response": {}}`,
		"bad stop":          `{"response": {"text": "y", "stop_reason": "done"}}`,
		"nameless tool":     `{"response": {"tool_calls": [{"input": {}}]}}`,
		"array input":       `{"response": {"tool_calls": [{"name": "t", "input": [1]}]}}`,
		"null input":        `{"response": {"tool_calls": [{"name": "t", "input": null}]}}`,
		"repeated id":       `{"response": {"tool_calls": [{"id": "a", "name": "t"}, {"id": "a", "name": "u"}]}}`,
		"two fallbacks":     "{\"response\": {\"text\": \"y\"}}\n{\"response\": {\"text\": \"z\"}}",
		"missing fixture":   `{"fixture": "nope.http"}`,
		"escaping fixture":  `{"fixture": "../x.http"}`,
		"absolute fixture":  `{"fixture": "/etc/passwd"}`,
		"malformed fixture": `{"fixture": "bad.http"}`,
	} {
		if _, err := fakellm.ParseScript("s", strings.NewReader(script), files); !errors.Is(err, fakellm.ErrScript) {
			t.Errorf("%s: err %v, want ErrScript", name, err)
		}
	}
	if _, err := fakellm.ParseScript("../s", strings.NewReader(`{"response": {"text": "y"}}`), nil); !errors.Is(err, fakellm.ErrScript) {
		t.Errorf("a name that is a path: err %v", err)
	}
	if _, err := fakellm.New(nil); !errors.Is(err, fakellm.ErrScript) {
		t.Errorf("no scripts: err %v", err)
	}
	s, err := fakellm.ParseScript("s", strings.NewReader(`{"response": {"text": "y"}}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fakellm.New(map[string]*fakellm.Script{"s": s}, fakellm.WithDefault("t")); !errors.Is(err, fakellm.ErrScript) {
		t.Errorf("missing default: err %v", err)
	}
	if _, err := fakellm.New(map[string]*fakellm.Script{"t": s}); !errors.Is(err, fakellm.ErrScript) {
		t.Errorf("script under another name: err %v", err)
	}
}

// LoadDir loads every script of a directory, and its fixtures cannot reach
// out of it through a symbolic link.
func TestLoadDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.http")
	write := func(name, data string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("one.jsonl", `{"match": "replay", "fixture": "rec.http"}`+"\n"+`{"response": {"text": "one"}}`)
	write("two.jsonl", `{"response": {"text": "two"}}`)
	write("rec.http", fixture)
	write("notes.txt", "not a script")
	scripts, err := fakellm.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(scripts) != 2 || scripts["one"].Name() != "one" || scripts["two"] == nil {
		t.Fatalf("scripts %v", scripts)
	}

	if err := os.WriteFile(outside, []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link.http")); err != nil {
		t.Skip("no symbolic links here:", err)
	}
	write("three.jsonl", `{"fixture": "link.http"}`)
	if _, err := fakellm.LoadDir(dir); !errors.Is(err, fakellm.ErrScript) {
		t.Errorf("a fixture linked from outside the directory: err %v", err)
	}
	if _, err := fakellm.LoadDir(filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing directory loaded")
	}
	if _, err := fakellm.LoadDir(t.TempDir()); !errors.Is(err, fakellm.ErrScript) {
		t.Errorf("a directory without scripts: err %v", err)
	}
}

// The scripts this repository ships load, and answer what their comments say.
func TestShippedScripts(t *testing.T) {
	t.Parallel()
	scripts, err := fakellm.LoadDir(filepath.Join("..", "..", "deploy", "local", "fakellm"))
	if err != nil {
		t.Fatal(err)
	}
	h, err := fakellm.New(scripts, fakellm.WithDefault("default"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	_, m := exchange(t, srv.URL, request(user("please multiply 6 by 7")))
	if b := content(t, m); len(b) != 2 || b[1]["name"] != "calculate" {
		t.Fatalf("multiply: %v", m)
	}
	_, m = exchange(t, srv.URL, request(user("multiply"), assistant(m["content"]),
		user([]msg{{"type": "tool_result", "tool_use_id": content(t, m)[1]["id"], "content": `{"result":42}`}})))
	if b := content(t, m); len(b) != 1 || !strings.Contains(b[0]["text"].(string), "42") {
		t.Errorf("result: %v", m)
	}
	if r := post(t, srv.URL, request(user("recorded"))); r.status != http.StatusOK || !bytes.Contains(r.body, []byte("message_stop")) {
		t.Errorf("recorded: %d %s", r.status, r.body)
	}
}

func TestHealthAndRoutes(t *testing.T) {
	t.Parallel()
	srv := newServer(t, nil, "s", `{"response": {"text": "y"}}`)
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/healthz", http.StatusOK},
		{http.MethodGet, "/v1/messages", http.StatusMethodNotAllowed},
		{http.MethodPost, "/v1/complete", http.StatusNotFound},
	} {
		req, err := http.NewRequestWithContext(t.Context(), tc.method, srv.URL+tc.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s %s: %d, want %d", tc.method, tc.path, resp.StatusCode, tc.want)
		}
		if tc.want != http.StatusOK {
			checkError(t, result{resp.StatusCode, resp.Header, body})
		}
	}
}

// Many clients at once get the answers each would get alone (run with -race).
func TestConcurrentRequests(t *testing.T) {
	t.Parallel()
	srv := newServer(t, nil, "s", `{"match": "a", "response": {"text": "A"}}`+"\n"+`{"response": {"tool_calls": [{"name": "t"}]}}`)
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			said, want := "b", "tool_use"
			if i%2 == 0 {
				said, want = "a", "end_turn"
			}
			req := request(user(said))
			req["stream"] = i%4 < 2
			r := post(t, srv.URL, req)
			if r.status != http.StatusOK || !bytes.Contains(r.body, []byte(`"stop_reason":"`+want+`"`)) {
				t.Errorf("request %d: %d %s", i, r.status, r.body)
			}
		})
	}
	wg.Wait()
}

// The server answers anything a client sends with a message or an API
// error, never a panic or a 5xx.
func FuzzServe(f *testing.F) {
	for _, s := range []string{
		`{"messages":[{"role":"user","content":"hello"}]}`,
		`{"stream":true,"messages":[{"role":"user","content":[{"type":"tool_result","content":[{"type":"text","text":"hello"}]}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"text"}]}]}`,
		`{"messages":[{"role":"user","content":null}]}`,
		`{"messages":"x"}`, `[]`, `null`, ``,
	} {
		f.Add([]byte(s))
	}
	s, err := fakellm.ParseScript("s", strings.NewReader(`{"match": "hello", "response": {"text": "Hi.", "tool_calls": [{"name": "t"}]}}`), nil)
	if err != nil {
		f.Fatal(err)
	}
	h, err := fakellm.New(map[string]*fakellm.Script{"s": s})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body)))
		r := result{w.Code, w.Header(), w.Body.Bytes()}
		switch {
		case r.status != http.StatusOK:
			checkError(t, r)
		case strings.HasPrefix(r.header.Get("Content-Type"), "text/event-stream"):
			checkMessage(t, accumulate(t, r.body))
		default:
			var m msg
			if err := json.Unmarshal(r.body, &m); err != nil {
				t.Fatal(err)
			}
			checkMessage(t, m)
		}
	})
}
