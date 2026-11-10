package notify

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
)

// LaneSetup is one lane as the app configured it.
type LaneSetup struct {
	Info LaneInfo
	// On is false for a lane without its keys: it is in the tree as [Off].
	On bool
	// Cost is what one message costs, in paise; Cap its bound.
	Cost int64
	Cap  Cap
	// Why says why a lane is off ("set FARM_WHATSAPP_TOKEN ...").
	Why string
	// Sender is the guarded lane (or Off).
	Sender Sender
}

// Hub is the messaging setup the app's API serves: the dispatcher, the
// lanes by name, and what the settings page needs.
type Hub struct {
	Dispatcher *Dispatcher
	Lanes      map[string]LaneSetup
	// Routes is the tree's configuration.
	Routes string
	// PushKey is the web push public key (applicationServerKey); empty
	// when push is off.
	PushKey string
	// TelegramBot is the bot's username, for its start link; empty when
	// Telegram is off.
	TelegramBot string
	// Unsubscribe makes and checks email unsubscribe links; nil for none.
	Unsubscribe *Unsubscriber
}

// LaneStatus is a lane for the settings page.
type LaneStatus struct {
	LaneInfo
	On         bool   `json:"on"`
	Why        string `json:"why,omitempty"`
	Cost       int64  `json:"cost"`
	Cap        Cap    `json:"cap"`
	TodayCount int    `json:"today_count"`
	TodayCost  int64  `json:"today_cost"`
	MonthCount int    `json:"month_count"`
	MonthCost  int64  `json:"month_cost"`
}

// Status lists the lanes, by name, with what each spent today and this
// month (India's days).
func (h *Hub) Status(ctx context.Context, now time.Time) ([]LaneStatus, error) {
	names := make([]string, 0, len(h.Lanes))
	for n := range h.Lanes {
		names = append(names, n)
	}
	slices.Sort(names)
	local := now.UTC().Add(IndiaOffset * time.Minute)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC).Add(-IndiaOffset * time.Minute)
	month := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, time.UTC).Add(-IndiaOffset * time.Minute)
	out := make([]LaneStatus, 0, len(names))
	for _, n := range names {
		l := h.Lanes[n]
		s := LaneStatus{LaneInfo: l.Info, On: l.On, Why: l.Why, Cost: l.Cost, Cap: l.Cap}
		var err error
		if s.TodayCount, s.TodayCost, err = h.Dispatcher.Store().Spend(ctx, n, day); err != nil {
			return nil, err
		}
		if s.MonthCount, s.MonthCost, err = h.Dispatcher.Store().Spend(ctx, n, month); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// NewID makes a random ID with prefix.
func NewID(prefix string) string {
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// LinkCode gives recipient id a new one-time code to link a chat (it
// replaces the last), and returns it.
func (h *Hub) LinkCode(ctx context.Context, id string) (string, error) {
	code := NewID("")
	_, err := h.Dispatcher.Store().Update(ctx, id, func(r *Recipient) error { r.LinkCode = code; return nil })
	return code, err
}

// Test sends a test message to recipient id at once: through lane, or
// through the tree when lane is "".
func (h *Hub) Test(ctx context.Context, id, lane, title, body string) ([]Receipt, error) {
	var via Sender
	if lane != "" {
		l, ok := h.Lanes[lane]
		if !ok {
			return nil, fmt.Errorf("%w: no lane %q", ErrInvalid, lane)
		}
		via = l.Sender
	}
	m := Message{ID: NewID("test_"), To: id, Kind: KindTest, Severity: Advice, Title: title, Body: body}
	return h.Dispatcher.SendNow(ctx, m, via)
}

// SetConsent records recipient id's answer on lane from the settings page.
func (h *Hub) SetConsent(ctx context.Context, id, lane string, on bool, now time.Time) (Recipient, error) {
	if _, ok := h.Lanes[lane]; !ok {
		return Recipient{}, fmt.Errorf("%w: no lane %q", ErrInvalid, lane)
	}
	return h.Dispatcher.Store().Update(ctx, id, func(r *Recipient) error {
		if r.Consent == nil {
			r.Consent = map[string]Consent{}
		}
		r.Consent[lane] = Consent{OptedIn: on, At: now.UTC(), Source: "settings"}
		return nil
	})
}

// AddPush adds a browser's subscription to recipient id (replacing one
// with the same endpoint; the oldest goes past ten) and opts them in to
// push.
func (h *Hub) AddPush(ctx context.Context, id string, s PushSubscription, now time.Time) (Recipient, error) {
	s.CreatedAt = now.UTC()
	return h.Dispatcher.Store().Update(ctx, id, func(r *Recipient) error {
		r.Push = slices.DeleteFunc(r.Push, func(p PushSubscription) bool { return p.Endpoint == s.Endpoint })
		r.Push = append(r.Push, s)
		if len(r.Push) > 10 {
			r.Push = r.Push[len(r.Push)-10:]
		}
		if r.Consent == nil {
			r.Consent = map[string]Consent{}
		}
		r.Consent["push"] = Consent{OptedIn: true, At: now.UTC(), Source: "subscribed"}
		return nil
	})
}

// DropPush removes a subscription from recipient id.
func (h *Hub) DropPush(ctx context.Context, id, endpoint string) error {
	_, err := h.Dispatcher.Store().Update(ctx, id, func(r *Recipient) error {
		r.Push = slices.DeleteFunc(r.Push, func(p PushSubscription) bool { return p.Endpoint == endpoint })
		return nil
	})
	return err
}

// Unsubscriber makes and checks unsubscribe links: an HMAC of the
// recipient and the lane, so a link works without signing in and cannot
// be made for someone else.
type Unsubscriber struct {
	secret []byte
	base   string
	store  Store
}

// LoadOrCreateSecret reads a 32-byte secret from path, making it the
// first time.
func LoadOrCreateSecret(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, []byte(hex.EncodeToString(b)), 0o600); err != nil {
			return nil, fmt.Errorf("notify: save secret: %w", err)
		}
		return b, nil
	}
	if err != nil {
		return nil, err
	}
	b, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(b) < 32 {
		return nil, fmt.Errorf("notify: secret in %s: want 64 hex characters", path)
	}
	return b, nil
}

// NewUnsubscriber returns links under base (the app's public URL, with
// the API at /api/notify/unsubscribe).
func NewUnsubscriber(secret []byte, base string, store Store) *Unsubscriber {
	return &Unsubscriber{secret: secret, base: strings.TrimRight(base, "/"), store: store}
}

func (u *Unsubscriber) token(recipient, lane string) string {
	mac := hmac.New(sha256.New, u.secret)
	_, _ = io.WriteString(mac, recipient+"\x00"+lane)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:18])
}

// Link is the unsubscribe link of m on lane.
func (u *Unsubscriber) Link(m Message, lane string) string {
	q := url.Values{"r": {m.To}, "l": {lane}, "t": {u.token(m.To, lane)}}
	return u.base + "/api/notify/unsubscribe?" + q.Encode()
}

// ServeHTTP shows a page with an unsubscribe button (GET, so a mail
// scanner following links does not unsubscribe anyone) and unsubscribes
// on POST (the button, or the mail app's one-click List-Unsubscribe-Post).
func (u *Unsubscriber) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rid, lane, tok := q.Get("r"), q.Get("l"), q.Get("t")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if rid == "" || lane == "" || !hmac.Equal([]byte(tok), []byte(u.token(rid, lane))) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "<p>This link is not valid.</p>")
		return
	}
	switch r.Method {
	case http.MethodGet:
		_, _ = fmt.Fprintf(w, `<!doctype html><meta name="viewport" content="width=device-width"><title>Unsubscribe</title>
<form method="post"><p>Stop farm messages by %s?</p><button>Unsubscribe</button></form>`, html.EscapeString(lane))
	case http.MethodPost:
		_, err := u.store.Update(r.Context(), rid, func(rc *Recipient) error {
			if rc.Consent == nil {
				rc.Consent = map[string]Consent{}
			}
			rc.Consent[lane] = Consent{OptedIn: false, At: time.Now().UTC(), Source: "unsubscribe"}
			return nil
		})
		if err != nil && !errors.Is(err, ErrNotFound) {
			http.Error(w, "could not save that, try again", http.StatusInternalServerError)
			return
		}
		_, _ = fmt.Fprintf(w, "<p>Done: no more farm messages by %s. You can turn them on again in the app's settings.</p>", html.EscapeString(lane))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
