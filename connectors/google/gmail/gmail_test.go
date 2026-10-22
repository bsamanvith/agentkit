package gmail_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/connectors/google/common"
	"github.com/bsamanvith/agentkit/connectors/google/gmail"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// upstream is a fake Gmail: handler answers each request, and every request
// is recorded.
type upstream struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []recorded
}

type recorded struct {
	method, path, query string
	auth                string
	body                map[string]any
}

func newUpstream(t *testing.T, handler func(r recorded, w http.ResponseWriter)) *upstream {
	t.Helper()
	u := &upstream{}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		rec := recorded{method: req.Method, path: req.URL.Path, query: req.URL.RawQuery, auth: req.Header.Get("Authorization")}
		if b, _ := io.ReadAll(req.Body); len(b) > 0 {
			_ = json.Unmarshal(b, &rec.body)
		}
		u.mu.Lock()
		u.reqs = append(u.reqs, rec)
		u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		handler(rec, w)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *upstream) requests() []recorded {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.reqs)
}

// toolbox builds the toolset against u with the settings and returns its
// tools by name.
func toolbox(t *testing.T, u *upstream, settings string, opts ...gmail.Option) map[string]tool.Tool {
	t.Helper()
	opts = append([]gmail.Option{
		gmail.WithBaseURL(u.srv.URL), gmail.WithHTTPClient(u.srv.Client()),
		gmail.WithLookupEnv(func(string) (string, bool) { return "test-token", true }),
	}, opts...)
	tools, err := gmail.Factory(opts...).Build(t.Context(), toolset.BuildInput{Settings: json.RawMessage(settings)})
	if err != nil {
		t.Fatalf("Build(%s): %v", settings, err)
	}
	m := map[string]tool.Tool{}
	for _, tl := range tools {
		m[tl.Definition().Name] = tl
	}
	return m
}

func call(t *testing.T, tl tool.Tool, input string) (map[string]any, error) {
	t.Helper()
	out, err := tl.Invoke(t.Context(), json.RawMessage(input))
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("output is not an object: %s", out)
	}
	return m, nil
}

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func writeJSON(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }

func part(mime, text string) map[string]any {
	return map[string]any{"mimeType": mime, "body": map[string]any{"data": b64(text), "size": len(text)}}
}

func msg(payload map[string]any) map[string]any {
	return map[string]any{"id": "m1", "threadId": "t1", "payload": payload}
}

func TestReadMessageFindsTheTextInEveryMessageShape(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		payload map[string]any
		want    string
		attach  int
	}{
		{"single plain part", part("text/plain", "Hello\r\nworld"), "Hello\nworld", 0},
		{"html only", part("text/html", "<html><head><title>x</title><style>p{}</style></head><body><p>Hi&nbsp;there &amp; welcome</p><ul><li>one</li><li>two</li></ul><script>alert(1)</script></body></html>"), "Hi there & welcome\none\ntwo", 0},
		{"alternative prefers plain", map[string]any{"mimeType": "multipart/alternative", "parts": []any{part("text/plain", "plain wins"), part("text/html", "<b>html loses</b>")}}, "plain wins", 0},
		{"mixed with attachments", map[string]any{"mimeType": "multipart/mixed", "parts": []any{
			map[string]any{"mimeType": "multipart/alternative", "parts": []any{part("text/plain", "see attached")}},
			map[string]any{"mimeType": "application/pdf", "filename": "a.pdf", "body": map[string]any{"attachmentId": "att1", "size": 10}},
			map[string]any{"mimeType": "image/png", "filename": "b.png", "body": map[string]any{"attachmentId": "att2", "size": 20}},
		}}, "see attached", 2},
		{"non-ascii", part("text/plain", "Café Ångström 日本語 🎉"), "Café Ångström 日本語 🎉", 0},
		{"invalid base64 is empty text", map[string]any{"mimeType": "text/plain", "body": map[string]any{"data": "!!!not base64", "size": 3}}, "", 0},
		{"no body at all", map[string]any{"mimeType": "multipart/mixed"}, "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			u := newUpstream(t, func(_ recorded, w http.ResponseWriter) { writeJSON(w, msg(tt.payload)) })
			out, err := call(t, toolbox(t, u, `{}`)["gmail_read_message"], `{"id":"m1"}`)
			if err != nil {
				t.Fatal(err)
			}
			if out["body"] != tt.want {
				t.Errorf("body = %q, want %q", out["body"], tt.want)
			}
			atts, _ := out["attachments"].([]any)
			if len(atts) != tt.attach {
				t.Errorf("attachments = %v, want %d", atts, tt.attach)
			}
			for _, r := range u.requests() {
				if strings.Contains(r.path, "attachments") {
					t.Errorf("fetched attachment content: %s", r.path)
				}
			}
		})
	}
}

func TestBodyIsCutAtARuneBoundaryAndSaysHowToGetMore(t *testing.T) {
	t.Parallel()
	u := newUpstream(t, func(_ recorded, w http.ResponseWriter) {
		writeJSON(w, msg(part("text/plain", strings.Repeat("日本語", 200))))
	})
	out, err := call(t, toolbox(t, u, `{}`)["gmail_read_message"], `{"id":"m1","max_body_chars":100}`)
	if err != nil {
		t.Fatal(err)
	}
	body := out["body"].(string)
	if !utf8.ValidString(body) || !strings.HasPrefix(body, strings.Repeat("日本語", 33)+"日…[truncated 500 characters; ") ||
		!strings.Contains(body, "max_body_chars") {
		t.Errorf("body = %q", body)
	}
}

func TestBodyLimitSetting(t *testing.T) {
	t.Parallel()
	u := newUpstream(t, func(_ recorded, w http.ResponseWriter) {
		writeJSON(w, msg(part("text/plain", strings.Repeat("a", 500))))
	})
	tools := toolbox(t, u, `{"max_body_chars": 200}`)
	out, _ := call(t, tools["gmail_read_message"], `{"id":"m1"}`)
	if got := len(out["body"].(string)); got < 200 || got > 300 || !strings.Contains(out["body"].(string), "truncated 300 characters") {
		t.Errorf("default limit not applied: %d bytes", got)
	}
	out, _ = call(t, tools["gmail_read_message"], `{"id":"m1","max_body_chars":400}`)
	if !strings.Contains(out["body"].(string), "truncated 100 characters") {
		t.Errorf("per-call limit not applied: %q", out["body"])
	}
	if _, err := call(t, tools["gmail_read_message"], `{"id":"m1","max_body_chars":99999999}`); !errors.Is(err, tool.ErrInvalidInput) {
		t.Errorf("huge limit: %v", err)
	}
}

func TestReadThreadSharesTheBodyBudget(t *testing.T) {
	t.Parallel()
	u := newUpstream(t, func(_ recorded, w http.ResponseWriter) {
		var ms []any
		for i := range 4 {
			m := msg(part("text/plain", strings.Repeat("x", 1000)))
			m["id"] = fmt.Sprint("m", i)
			ms = append(ms, m)
		}
		writeJSON(w, map[string]any{"id": "t1", "messages": ms})
	})
	out, err := call(t, toolbox(t, u, `{}`)["gmail_read_thread"], `{"id":"t1","max_body_chars":400}`)
	if err != nil {
		t.Fatal(err)
	}
	msgs := out["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("messages = %d", len(msgs))
	}
	for _, m := range msgs {
		if body := m.(map[string]any)["body"].(string); !strings.HasPrefix(body, strings.Repeat("x", 100)+"…[truncated 900") {
			t.Errorf("each of 4 messages should keep 100 characters: %q", body)
		}
	}
}

func TestSearchFetchesEachMessageOnceFewAtATimeAndKeepsGmailsOrder(t *testing.T) {
	t.Parallel()
	const n = 50
	var inflight, peak atomic.Int32
	release := make(chan struct{})
	var releaseOnce sync.Once
	var arrived atomic.Int32
	u := newUpstream(t, func(r recorded, w http.ResponseWriter) {
		if r.path == "/gmail/v1/users/me/messages" {
			var ids []any
			for i := range n {
				ids = append(ids, map[string]any{"id": fmt.Sprint("m", i), "threadId": "t"})
			}
			writeJSON(w, map[string]any{"messages": ids, "nextPageToken": "next"})
			return
		}
		cur := inflight.Add(1)
		for {
			p := peak.Load()
			if cur <= p || peak.CompareAndSwap(p, cur) {
				break
			}
		}
		// Every request waits until five are in flight. The first five then
		// stay in flight a little longer, so that a sixth shows up if the
		// connector does not hold to five. The extra wait can only make a
		// broken connector pass, never fail a correct one.
		first := arrived.Add(1) <= 5
		if arrived.Load() >= 5 {
			releaseOnce.Do(func() { close(release) })
		}
		<-release
		if first {
			time.Sleep(150 * time.Millisecond)
		}
		inflight.Add(-1)
		id := strings.TrimPrefix(r.path, "/gmail/v1/users/me/messages/")
		writeJSON(w, map[string]any{"id": id, "threadId": "t", "snippet": "it&#39;s " + id, "payload": map[string]any{
			"headers": []any{map[string]any{"name": "Subject", "value": "Subject " + id}},
		}})
	})
	out, err := call(t, toolbox(t, u, `{}`)["gmail_search_messages"], `{"query":"x","max_results":50,"page_token":"cur"}`)
	if err != nil {
		t.Fatal(err)
	}
	msgs := out["messages"].([]any)
	for i, m := range msgs {
		if m.(map[string]any)["id"] != fmt.Sprint("m", i) || m.(map[string]any)["subject"] != fmt.Sprint("Subject m", i) {
			t.Fatalf("message %d = %v", i, m)
		}
	}
	if msgs[3].(map[string]any)["snippet"] != "it's m3" {
		t.Errorf("snippet not unescaped: %v", msgs[3])
	}
	if out["next_page_token"] != "next" {
		t.Errorf("next_page_token = %v", out["next_page_token"])
	}
	reqs := u.requests()
	if len(reqs) != 1+n {
		t.Errorf("requests = %d, want %d", len(reqs), 1+n)
	}
	if !strings.Contains(reqs[0].query, "pageToken=cur") && !slices.ContainsFunc(reqs, func(r recorded) bool { return strings.Contains(r.query, "pageToken=cur") }) {
		t.Errorf("page token not sent: %v", reqs[0].query)
	}
	if peak.Load() > 5 {
		t.Errorf("%d metadata requests in flight, want at most 5", peak.Load())
	}
}

func TestSearchFailsWholeWhenOneMessageFails(t *testing.T) {
	t.Parallel()
	u := newUpstream(t, func(r recorded, w http.ResponseWriter) {
		switch {
		case r.path == "/gmail/v1/users/me/messages":
			writeJSON(w, map[string]any{"messages": []any{map[string]any{"id": "a"}, map[string]any{"id": "b"}}})
		case strings.HasSuffix(r.path, "/b"):
			w.WriteHeader(404)
			writeJSON(w, map[string]any{"error": map[string]any{"message": "Requested entity was not found."}})
		default:
			writeJSON(w, map[string]any{"id": "a"})
		}
	})
	_, err := call(t, toolbox(t, u, `{}`)["gmail_search_messages"], `{"query":"x"}`)
	if !errors.Is(err, tool.ErrToolFailed) {
		t.Errorf("error = %v, want ErrToolFailed", err)
	}
}

// capture decodes the raw message the fake received for a write.
func capture(t *testing.T, raw any) (*mail.Message, string) {
	t.Helper()
	s, ok := raw.(string)
	if !ok {
		t.Fatalf("raw = %v", raw)
	}
	b, err := base64.URLEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	m, err := mail.ReadMessage(strings.NewReader(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(quotedprintable.NewReader(m.Body))
	if err != nil {
		t.Fatal(err)
	}
	return m, strings.ReplaceAll(string(body), "\r\n", "\n")
}

func TestDraftRoundTripsHeadersAndNonASCIIText(t *testing.T) {
	t.Parallel()
	u := newUpstream(t, func(_ recorded, w http.ResponseWriter) {
		writeJSON(w, map[string]any{"id": "d1", "message": map[string]any{"id": "m9", "threadId": "t9"}})
	})
	body := "Grüße,\n\n日本語 and 🎉 — a line long enough to be wrapped by the quoted-printable encoder: " + strings.Repeat("word ", 40) + "\nBye"
	in, _ := json.Marshal(map[string]any{
		"to": []string{"Ada Lovelace <ada@example.com>", "grace@example.com"}, "cc": []string{"c@example.com"}, "bcc": []string{"b@example.com"},
		"subject": "Café ☕ plans", "body": body,
	})
	out, err := call(t, toolbox(t, u, `{}`)["gmail_create_draft"], string(in))
	if err != nil {
		t.Fatal(err)
	}
	if out["draft_id"] != "d1" || out["message_id"] != "m9" || out["thread_id"] != "t9" {
		t.Errorf("output = %v", out)
	}
	reqs := u.requests()
	if len(reqs) != 1 || reqs[0].method != "POST" || reqs[0].path != "/gmail/v1/users/me/drafts" {
		t.Fatalf("requests = %+v", reqs)
	}
	m, gotBody := capture(t, reqs[0].body["message"].(map[string]any)["raw"])
	// On the wire the subject is RFC 2047 encoded, plain ASCII.
	for _, c := range []byte(m.Header.Get("Subject")) {
		if c >= 0x80 {
			t.Errorf("subject header is not ASCII: %q", m.Header.Get("Subject"))
			break
		}
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	if err != nil || subject != "Café ☕ plans" {
		t.Errorf("subject = %q, %v", subject, err)
	}
	to, err := m.Header.AddressList("To")
	if err != nil || len(to) != 2 || to[0].Name != "Ada Lovelace" || to[0].Address != "ada@example.com" || to[1].Address != "grace@example.com" {
		t.Errorf("To = %v, %v", to, err)
	}
	if m.Header.Get("Cc") != "<c@example.com>" || m.Header.Get("Bcc") != "<b@example.com>" {
		t.Errorf("Cc/Bcc = %q / %q", m.Header.Get("Cc"), m.Header.Get("Bcc"))
	}
	if gotBody != body {
		t.Errorf("body = %q, want %q", gotBody, body)
	}
	if ct := m.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") || !strings.Contains(strings.ToLower(ct), "utf-8") {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestReplyKeepsTheConversationAndDefaultsRecipientAndSubject(t *testing.T) {
	t.Parallel()
	u := newUpstream(t, func(r recorded, w http.ResponseWriter) {
		if r.method == "GET" {
			writeJSON(w, map[string]any{"id": "m1", "threadId": "t1", "payload": map[string]any{"headers": []any{
				map[string]any{"name": "Message-ID", "value": "<orig@x>"},
				map[string]any{"name": "References", "value": "<first@x>"},
				map[string]any{"name": "Subject", "value": "Lunch"},
				map[string]any{"name": "From", "value": "Ada <ada@example.com>"},
				map[string]any{"name": "Reply-To", "value": "lists@example.com"},
			}}})
			return
		}
		writeJSON(w, map[string]any{"id": "d1", "message": map[string]any{"id": "m2", "threadId": "t1"}})
	})
	if _, err := call(t, toolbox(t, u, `{}`)["gmail_create_draft"], `{"reply_to_message_id":"m1","body":"Yes"}`); err != nil {
		t.Fatal(err)
	}
	reqs := u.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %+v", reqs)
	}
	msgBody := reqs[1].body["message"].(map[string]any)
	if msgBody["threadId"] != "t1" {
		t.Errorf("threadId = %v", msgBody["threadId"])
	}
	m, _ := capture(t, msgBody["raw"])
	for header, want := range map[string]string{
		"In-Reply-To": "<orig@x>", "References": "<first@x> <orig@x>", "Subject": "Re: Lunch", "To": "<lists@example.com>",
	} {
		if got := m.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

func TestInvalidMessagesMakeNoRequest(t *testing.T) {
	t.Parallel()
	u := newUpstream(t, func(recorded, http.ResponseWriter) {})
	tools := toolbox(t, u, `{"allow_send": true}`)
	many := make([]string, 101)
	for i := range many {
		many[i] = fmt.Sprintf("u%d@example.com", i)
	}
	manyJSON, _ := json.Marshal(many)
	for name, in := range map[string]string{
		"101 recipients":                 fmt.Sprintf(`{"to": %s, "body": "x"}`, manyJSON),
		"newline in a cc address":        `{"to": ["a@example.com"], "cc": ["b@example.com\r\nX-Evil: 1"], "body": "x"}`,
		"newline in the subject":         `{"to": ["a@example.com"], "subject": "a\nb", "body": "x"}`,
		"reply to an id with a slash":    `{"reply_to_message_id": "m1/x", "body": "x"}`,
		"reply to a message but no body": `{"reply_to_message_id": "m1", "body": ""}`,
		"only a blank recipient":         `{"to": ["  "], "body": "x"}`,
	} {
		for _, name2 := range []string{"gmail_create_draft", "gmail_send_message"} {
			if _, err := call(t, tools[name2], in); !errors.Is(err, tool.ErrInvalidInput) {
				t.Errorf("%s: %s = %v, want ErrInvalidInput", name, name2, err)
			}
		}
	}
	if n := len(u.requests()); n != 0 {
		t.Errorf("%d requests for invalid input", n)
	}
}

func TestSendToolsAndModifyAreGatedBySettingsAndClassified(t *testing.T) {
	t.Parallel()
	u := newUpstream(t, func(recorded, http.ResponseWriter) {})
	names := func(settings string) []string {
		var out []string
		for n := range toolbox(t, u, settings) {
			out = append(out, n)
		}
		slices.Sort(out)
		return out
	}
	base := []string{"gmail_create_draft", "gmail_list_labels", "gmail_read_message", "gmail_read_thread", "gmail_search_messages"}
	if got, want := names(`{"allow_modify": false}`), base; !slices.Equal(got, want) {
		t.Errorf("read and draft only: %v, want %v", got, want)
	}
	if got, want := names(`{}`), slices.Concat(base, []string{"gmail_modify_labels"}); !slices.Equal(got, slices.Sorted(slices.Values(want))) {
		t.Errorf("default: %v, want %v", got, want)
	}
	all := toolbox(t, u, `{"allow_send": true}`)
	for _, n := range []string{"gmail_send_message", "gmail_send_draft"} {
		d := all[n].Definition()
		if d.Kind != tool.KindSend || d.Risk != tool.RiskHigh || !d.RequiresApproval {
			t.Errorf("%s: kind %q risk %q approval %v", n, d.Kind, d.Risk, d.RequiresApproval)
		}
	}
	for n, tl := range all {
		if strings.HasPrefix(n, "gmail_read") || n == "gmail_search_messages" || n == "gmail_list_labels" {
			if d := tl.Definition(); d.Kind != tool.KindRead || d.RequiresApproval {
				t.Errorf("%s: kind %q approval %v", n, d.Kind, d.RequiresApproval)
			}
		}
	}
}

func TestScopesFollowTheSettings(t *testing.T) {
	t.Parallel()
	for settings, want := range map[string][]string{
		`{}`:                      {gmail.ScopeReadonly, gmail.ScopeCompose, gmail.ScopeModify},
		`{"allow_modify": false}`: {gmail.ScopeReadonly, gmail.ScopeCompose},
		`{"allow_send": true}`:    {gmail.ScopeReadonly, gmail.ScopeCompose, gmail.ScopeModify, gmail.ScopeSend},
		`{"allow_modify":false,"allow_send":true}`: {gmail.ScopeReadonly, gmail.ScopeCompose, gmail.ScopeSend},
	} {
		got, err := gmail.Scopes(json.RawMessage(settings))
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("Scopes(%s) = %v, %v; want %v", settings, got, err, want)
		}
	}
}

func TestModifyLabels(t *testing.T) {
	t.Parallel()
	u := newUpstream(t, func(_ recorded, w http.ResponseWriter) { w.WriteHeader(204) })
	tools := toolbox(t, u, `{}`)
	for _, in := range []string{
		`{"ids":["m1"],"add_label_ids":["trash"]}`, `{"ids":["m1"],"remove_label_ids":["SPAM"]}`,
		`{"ids":[],"add_label_ids":["STARRED"]}`, `{"ids":["m1"]}`,
	} {
		if _, err := call(t, tools["gmail_modify_labels"], in); !errors.Is(err, tool.ErrInvalidInput) {
			t.Errorf("%s = %v, want ErrInvalidInput", in, err)
		}
	}
	if n := len(u.requests()); n != 0 {
		t.Errorf("%d requests for invalid input", n)
	}
	out, err := call(t, tools["gmail_modify_labels"], `{"ids":["m1","m2"],"remove_label_ids":["UNREAD"]}`)
	if err != nil || out["modified"] != float64(2) {
		t.Fatalf("%v, %v", out, err)
	}
	// The API wants both arrays, even an empty one.
	if b := u.requests()[0].body; fmt.Sprint(b["addLabelIds"]) != "[]" || fmt.Sprint(b["removeLabelIds"]) != "[UNREAD]" {
		t.Errorf("body = %v", b)
	}
}

func TestTokenComesFromTheSourcePerCallAndNeverFromTheEnvironmentWhenGiven(t *testing.T) {
	t.Parallel()
	u := newUpstream(t, func(_ recorded, w http.ResponseWriter) { writeJSON(w, map[string]any{"labels": []any{}}) })
	var n atomic.Int32
	src := common.TokenSourceFunc(func(context.Context) (string, error) { return fmt.Sprint("user-token-", n.Add(1)), nil })
	tools := toolbox(t, u, `{}`, gmail.WithTokenSource(src))
	for range 2 {
		if _, err := call(t, tools["gmail_list_labels"], `{}`); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	for _, r := range u.requests() {
		got = append(got, r.auth)
	}
	if want := []string{"Bearer user-token-1", "Bearer user-token-2"}; !slices.Equal(got, want) {
		t.Errorf("Authorization = %v, want %v", got, want)
	}
}

func TestBuildNeedsATokenAndRefusesBadSettings(t *testing.T) {
	t.Parallel()
	noEnv := func(string) (string, bool) { return "", false }
	if _, err := gmail.Factory(gmail.WithLookupEnv(noEnv)).Build(t.Context(), toolset.BuildInput{Settings: nil}); err == nil || !strings.Contains(err.Error(), "GOOGLE_ACCESS_TOKEN") {
		t.Errorf("no token: %v", err)
	}
	// With a source, no variable is needed.
	src := common.TokenSourceFunc(func(context.Context) (string, error) { return "t", nil })
	if _, err := gmail.Factory(gmail.WithLookupEnv(noEnv), gmail.WithTokenSource(src)).Build(t.Context(), toolset.BuildInput{Settings: nil}); err != nil {
		t.Errorf("with a source: %v", err)
	}
	for _, s := range []string{`{"max_body_chars": 5}`, `{"max_body_chars": 1000000}`, `{"allow_send": "yes"}`, `{"access_token": "ya29.x"}`} {
		if _, err := gmail.Factory(gmail.WithLookupEnv(func(string) (string, bool) { return "t", true })).Build(t.Context(), toolset.BuildInput{Settings: json.RawMessage(s)}); err == nil {
			t.Errorf("settings %s accepted", s)
		}
	}
}
