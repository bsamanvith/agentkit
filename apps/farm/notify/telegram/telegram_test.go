package telegram_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/notify"
	"github.com/bsamanvith/agentkit/apps/farm/notify/notifytest"
	"github.com/bsamanvith/agentkit/apps/farm/notify/telegram"
)

// bot is a fake Bot API.
type bot struct {
	mu      sync.Mutex
	sent    []map[string]any
	fails   []string
	updates [][]byte
}

func (b *bot) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	b.mu.Lock()
	defer b.mu.Unlock()
	if !strings.HasPrefix(r.URL.Path, "/botTOKEN/") {
		w.WriteHeader(401)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
		return
	}
	switch strings.TrimPrefix(r.URL.Path, "/botTOKEN/") {
	case "sendMessage":
		if len(b.fails) > 0 {
			f := b.fails[0]
			b.fails = b.fails[1:]
			_, _ = io.WriteString(w, f)
			return
		}
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		b.sent = append(b.sent, m)
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":77,"chat":{"id":4242}}}`)
	case "getUpdates":
		if len(b.updates) == 0 {
			_, _ = io.WriteString(w, `{"ok":true,"result":[]}`)
			return
		}
		u := b.updates[0]
		b.updates = b.updates[1:]
		_, _ = io.WriteString(w, `{"ok":true,"result":[`+string(u)+`]}`)
	}
}

func (b *bot) texts() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, m := range b.sent {
		out = append(out, m["chat_id"].(string)+": "+m["text"].(string))
	}
	return out
}

func newLane(t *testing.T, b *bot) *telegram.Lane {
	t.Helper()
	srv := httptest.NewServer(b)
	t.Cleanup(srv.Close)
	l, err := telegram.New(telegram.Config{Token: "TOKEN", Secret: "s3cret", URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestLane(t *testing.T) {
	notifytest.RunLane(t, func(t *testing.T) notifytest.LaneHarness {
		b := &bot{}
		return notifytest.LaneHarness{
			Lane: newLane(t, b),
			To:   notify.Recipient{ID: "r1", Telegram: "4242"},
			Fail: func(kind string) bool {
				f := map[string]string{
					notifytest.FailTemporary: `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 5","parameters":{"retry_after":5}}`,
					notifytest.FailRejected:  `{"ok":false,"error_code":400,"description":"Bad Request: message text is empty"}`,
					notifytest.FailGone:      `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`,
				}[kind]
				b.mu.Lock()
				b.fails = append(b.fails, f)
				b.mu.Unlock()
				return true
			},
			Got: b.texts,
		}
	})
}

type report struct {
	mu   sync.Mutex
	logs []string
}

func (r *report) add(s string) { r.mu.Lock(); r.logs = append(r.logs, s); r.mu.Unlock() }

func (r *report) got() string { r.mu.Lock(); defer r.mu.Unlock(); return strings.Join(r.logs, "\n") }

func (r *report) Status(context.Context, string, string, string, string) {}

func (r *report) Stop(_ context.Context, lane string, _ notify.AddressKind, addr string) {
	r.add("stop " + lane + " " + addr)
}

func (r *report) Start(_ context.Context, lane string, _ notify.AddressKind, addr string) {
	r.add("start " + lane + " " + addr)
}

func (r *report) Link(_ context.Context, lane, code string, _ notify.AddressKind, addr string) (string, error) {
	if code != "good-code-1" {
		return "", notify.ErrNotFound
	}
	r.add("link " + lane + " " + code + " " + addr)
	return "Ravi", nil
}

func update(chat int64, text string) string {
	t, _ := json.Marshal(text)
	return fmt.Sprintf(`{"update_id":1,"message":{"message_id":1,"chat":{"id":%d,"type":"private"},"text":%s}}`, chat, t)
}

func TestWebhookLinksAndStops(t *testing.T) {
	b := &bot{}
	l := newLane(t, b)
	rep := &report{}
	h := l.Webhook(rep)
	post := func(body, secret string) int {
		req := httptest.NewRequest(http.MethodPost, "/hooks/telegram", strings.NewReader(body))
		req.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := post(update(4242, "/stop"), "wrong"); code != http.StatusUnauthorized {
		t.Errorf("a wrong secret = %d", code)
	}
	for _, text := range []string{"/start good-code-1", "/start bad-code-1", "/stop", "/start@farm_bot", "what about rain?"} {
		if code := post(update(4242, text), "s3cret"); code != 200 {
			t.Errorf("%q = %d", text, code)
		}
	}
	if got, want := rep.got(), "link telegram good-code-1 4242\nstop telegram 4242\nstart telegram 4242"; got != want {
		t.Errorf("reported:\n%s\nwant:\n%s", got, want)
	}
	replies := b.texts()
	if len(replies) != 5 || !strings.Contains(replies[0], "Hello Ravi") || !strings.Contains(replies[1], "expired") {
		t.Errorf("replies %q", replies)
	}
}

func TestPoll(t *testing.T) {
	b := &bot{updates: [][]byte{[]byte(update(9, "/start good-code-1")), []byte(update(9, "STOP"))}}
	l := newLane(t, b)
	rep := &report{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { l.Poll(ctx, rep); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for rep.got() != "link telegram good-code-1 9\nstop telegram 9" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if got := rep.got(); got != "link telegram good-code-1 9\nstop telegram 9" {
		t.Errorf("polled %q", got)
	}
}
