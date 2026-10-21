package fakellm

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"
)

// ScriptHeader selects the script that answers a request.
const ScriptHeader = "X-Fakellm-Script"

// maxBody bounds a request body.
const maxBody = 32 << 20

// The fixed usage every scripted reply reports.
const (
	fakeInputTokens  = 10
	fakeOutputTokens = 5
)

// Server answers Anthropic Messages API requests from scripts. Create it with
// [New]; it is safe for concurrent use.
type Server struct {
	scripts map[string]*Script
	def     string
	log     *slog.Logger
}

// Option configures a [Server].
type Option func(*Server)

// WithDefault names the script that answers requests without the
// X-Fakellm-Script header. Without it, such requests get a 400, unless there
// is only one script.
func WithDefault(name string) Option { return func(s *Server) { s.def = name } }

// WithLogger logs every request to l. The default discards the log.
func WithLogger(l *slog.Logger) Option { return func(s *Server) { s.log = l } }

// New returns a server that answers from scripts, by name. The default
// script, if set, must be one of them.
func New(scripts map[string]*Script, opts ...Option) (*Server, error) {
	if len(scripts) == 0 {
		return nil, fmt.Errorf("%w: no scripts", ErrScript)
	}
	s := &Server{scripts: make(map[string]*Script, len(scripts)), log: slog.New(slog.DiscardHandler)}
	for name, sc := range scripts {
		if sc == nil || sc.name != name {
			return nil, fmt.Errorf("%w: script %q is registered under another name", ErrScript, name)
		}
		s.scripts[name] = sc
	}
	for _, o := range opts {
		o(s)
	}
	if s.def == "" && len(scripts) == 1 {
		for name := range scripts {
			s.def = name
		}
	}
	if s.def != "" && s.scripts[s.def] == nil {
		return nil, fmt.Errorf("%w: default script %q not found (have %s)", ErrScript, s.def, strings.Join(s.names(), ", "))
	}
	return s, nil
}

func (s *Server) names() []string {
	out := make([]string, 0, len(s.scripts))
	for n := range s.scripts {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// request is the part of a Messages API request the server reads.
type request struct {
	Model    string           `json:"model"`
	Stream   bool             `json:"stream"`
	Messages []requestMessage `json:"messages"`
}

type requestMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// contentBlock is a block of a message's content, or of a tool result's.
type contentBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Content json.RawMessage `json:"content"`
}

// ServeHTTP answers POST /v1/messages and GET /healthz.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/healthz" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok\n")
	case r.URL.Path == "/v1/messages" && r.Method == http.MethodPost:
		s.messages(w, r)
	case r.URL.Path == "/v1/messages":
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "fakellm: use POST")
	default:
		writeError(w, http.StatusNotFound, "not_found_error", "fakellm: only POST /v1/messages and GET /healthz")
	}
}

func (s *Server) messages(w http.ResponseWriter, r *http.Request) {
	name := r.Header.Get(ScriptHeader)
	if name == "" {
		name = s.def
	}
	attrs := []any{"script", name}
	fail := func(status int, typ, msg string, extra ...any) {
		s.log.Warn("fakellm request", append(append(attrs, extra...), "status", status, "error", msg)...)
		writeError(w, status, typ, msg)
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		if tooLarge := (*http.MaxBytesError)(nil); errors.As(err, &tooLarge) {
			fail(http.StatusRequestEntityTooLarge, "request_too_large", "fakellm: reading the request: "+err.Error())
		} else {
			fail(http.StatusBadRequest, "invalid_request_error", "fakellm: reading the request: "+err.Error())
		}
		return
	}
	var req request
	if err := json.Unmarshal(body, &req); err != nil {
		fail(http.StatusBadRequest, "invalid_request_error", "fakellm: the body is not a Messages API request: "+err.Error())
		return
	}
	attrs = append(attrs, "model", req.Model, "stream", req.Stream, "messages", len(req.Messages))
	sc := s.scripts[name]
	if sc == nil {
		msg := fmt.Sprintf("fakellm: no script %q (have %s); name one in the %s header", name, strings.Join(s.names(), ", "), ScriptHeader)
		if name == "" {
			msg = fmt.Sprintf("fakellm: no script named and no default (have %s); name one in the %s header", strings.Join(s.names(), ", "), ScriptHeader)
		}
		fail(http.StatusBadRequest, "invalid_request_error", msg)
		return
	}
	text, err := lastUserText(req.Messages)
	if err != nil {
		fail(http.StatusBadRequest, "invalid_request_error", "fakellm: "+err.Error())
		return
	}
	attrs = append(attrs, "last", excerpt(text))
	e := sc.answer(text)
	if e == nil {
		fail(http.StatusBadRequest, "invalid_request_error",
			fmt.Sprintf("fakellm: script %q: no entry matches the last user message %q, and the script has no fallback (an entry without \"match\")", name, excerpt(text)))
		return
	}
	attrs = append(attrs, "line", e.line)
	if e.fixture != nil {
		attrs = append(attrs, "fixture", true, "status", e.status)
		// Read whole when loaded, so only writing it can fail, after its
		// status is sent: log that, and write no second response.
		if err := ServeFixture(w, r, e.fixture); err != nil {
			s.log.Warn("fakellm request", append(attrs, "error", err.Error())...)
			return
		}
		s.log.Info("fakellm request", attrs...)
		return
	}
	conv, _ := json.Marshal(req.Messages) // decoded from JSON, so it encodes
	msg := render(e.reply, req.Model, conv)
	if req.Stream {
		writeStream(w, msg)
	} else {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Request-Id", "req_fakellm")
		_ = json.NewEncoder(w).Encode(msg)
	}
	s.log.Info("fakellm request", append(attrs, "status", http.StatusOK, "stop_reason", *msg.StopReason)...)
}

// lastUserText returns the text of the last user message: its text blocks
// and the text of its tool results, one per line.
func lastUserText(msgs []requestMessage) (string, error) {
	if len(msgs) == 0 {
		return "", errors.New("messages: at least one message is required")
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "user" {
			continue
		}
		var parts []string
		if err := appendText(&parts, msgs[i].Content, true); err != nil {
			return "", fmt.Errorf("messages.%d.content: %w", i, err)
		}
		return strings.Join(parts, "\n"), nil
	}
	return "", errors.New("messages: no user message")
}

// appendText appends the text of content (a string or an array of blocks)
// to parts; with results, it descends into tool results' content.
func appendText(parts *[]string, content json.RawMessage, results bool) error {
	content = bytes.TrimSpace(content)
	if len(content) == 0 || string(content) == "null" {
		return nil
	}
	if content[0] == '"' {
		var s string
		if err := json.Unmarshal(content, &s); err != nil {
			return err
		}
		*parts = append(*parts, s)
		return nil
	}
	var blocks []contentBlock
	if err := json.Unmarshal(content, &blocks); err != nil {
		return errors.New("want a string or an array of content blocks")
	}
	for _, b := range blocks {
		switch {
		case b.Type == "text":
			*parts = append(*parts, b.Text)
		case b.Type == "tool_result" && results:
			if err := appendText(parts, b.Content, false); err != nil {
				return err
			}
		}
	}
	return nil
}

func excerpt(s string) string {
	const n = 120
	i := 0
	for range n {
		if i >= len(s) {
			return s
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	if i >= len(s) {
		return s
	}
	return s[:i] + "…"
}

// message is a Messages API response.
type message struct {
	ID           string  `json:"id"`
	Type         string  `json:"type"`
	Role         string  `json:"role"`
	Model        string  `json:"model"`
	Content      []block `json:"content"`
	StopReason   *string `json:"stop_reason"`
	StopSequence *string `json:"stop_sequence"`
	Usage        usage   `json:"usage"`
}

// block is a content block of a response: text or tool_use.
type block struct {
	Type  string          `json:"type"`
	Text  *string         `json:"text,omitempty"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

type usage struct {
	InputTokens              int `json:"input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	OutputTokens             int `json:"output_tokens"`
}

// render builds the response to a conversation (its messages, as JSON) from
// a reply. The ids are derived from the conversation: the same one gets the
// same answer, streamed or not, and different ones get different ids.
func render(rp *reply, model string, conv []byte) message {
	sum := sha256.Sum256(conv)
	h := hex.EncodeToString(sum[:])
	if model == "" {
		model = "fakellm"
	}
	m := message{
		ID: "msg_fake_" + h[:24], Type: "message", Role: "assistant", Model: model,
		Content: []block{},
		Usage:   usage{InputTokens: fakeInputTokens, OutputTokens: fakeOutputTokens},
	}
	if rp.text != "" {
		m.Content = append(m.Content, block{Type: "text", Text: &rp.text})
	}
	for i, c := range rp.calls {
		id := c.id
		if id == "" {
			id = fmt.Sprintf("toolu_fake_%s_%d", h[:16], i)
		}
		m.Content = append(m.Content, block{Type: "tool_use", ID: id, Name: c.name, Input: c.input})
	}
	stop := rp.stopReason
	m.StopReason = &stop
	return m
}

// writeStream writes msg as the Messages API's event stream: message_start,
// a ping, each block as start, deltas and stop, then message_delta and
// message_stop. Text and tool input arrive in two deltas each, so a client's
// accumulation is exercised.
func writeStream(w http.ResponseWriter, msg message) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Request-Id", "req_fakellm")
	w.WriteHeader(http.StatusOK)
	f, _ := w.(http.Flusher)
	event := func(typ string, data any) {
		b, _ := json.Marshal(data) // only our own types
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ, b)
		if f != nil {
			f.Flush()
		}
	}
	type obj = map[string]any
	start := msg
	start.Content = []block{}
	start.StopReason = nil
	start.Usage.OutputTokens = 1
	event("message_start", obj{"type": "message_start", "message": start})
	event("ping", obj{"type": "ping"})
	for i, b := range msg.Content {
		switch b.Type {
		case "text":
			event("content_block_start", obj{"type": "content_block_start", "index": i, "content_block": obj{"type": "text", "text": ""}})
			for _, part := range halves(*b.Text) {
				event("content_block_delta", obj{"type": "content_block_delta", "index": i, "delta": obj{"type": "text_delta", "text": part}})
			}
		case "tool_use":
			event("content_block_start", obj{
				"type": "content_block_start", "index": i,
				"content_block": obj{"type": "tool_use", "id": b.ID, "name": b.Name, "input": obj{}},
			})
			for _, part := range halves(string(b.Input)) {
				event("content_block_delta", obj{"type": "content_block_delta", "index": i, "delta": obj{"type": "input_json_delta", "partial_json": part}})
			}
		}
		event("content_block_stop", obj{"type": "content_block_stop", "index": i})
	}
	event("message_delta", obj{
		"type":  "message_delta",
		"delta": obj{"stop_reason": msg.StopReason, "stop_sequence": nil},
		"usage": obj{"output_tokens": msg.Usage.OutputTokens},
	})
	event("message_stop", obj{"type": "message_stop"})
}

// halves splits s in two at a rune boundary near its middle, leaving out
// an empty half.
func halves(s string) []string {
	mid := len(s) / 2
	for mid > 0 && mid < len(s) && !utf8.RuneStart(s[mid]) {
		mid++
	}
	var out []string
	for _, p := range []string{s[:mid], s[mid:]} {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// writeError writes an error in the Messages API's shape.
func writeError(w http.ResponseWriter, status int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Request-Id", "req_fakellm")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":  "error",
		"error": map[string]string{"type": typ, "message": msg},
	})
}

// ServeFixture writes a recorded response (status line, headers, blank line,
// body, as curl -i prints it, which is how the replay tests record the real
// API) to w, as the answer to r. It writes nothing if raw is not a response.
func ServeFixture(w http.ResponseWriter, r *http.Request, raw []byte) error {
	resp, err := parseFixture(raw, r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		w.Header()[k] = vs
	}
	w.WriteHeader(resp.StatusCode)
	_, err = io.Copy(w, resp.Body)
	return err
}

// parseFixture reads raw's status line and headers as the answer to r (nil:
// to a GET); the body is left to read.
func parseFixture(raw []byte, r *http.Request) (*http.Response, error) {
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(raw)), r)
	if err != nil {
		return nil, fmt.Errorf("not a recorded HTTP response: %w", err)
	}
	return resp, nil
}
