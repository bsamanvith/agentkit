// Package telegram is the Telegram lane, through a bot (Telegram's Bot
// API). FREE: no charge per message; the bot's token comes from
// @BotFather.
//
// A person links their chat by opening the bot with their link code
// (https://t.me/<bot>?start=<code>, shown in the app's settings): the bot
// gets "/start <code>", and the chat becomes their Telegram address. /stop
// opts them out, /start opts them in again. Updates arrive on a webhook
// ([Lane.Webhook]) when the app has a public URL, or by asking Telegram
// ([Lane.Poll]) when it does not.
package telegram

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/apps/farm/notify"
)

// DefaultURL is the Bot API.
const DefaultURL = "https://api.telegram.org"

// Config configures the lane.
type Config struct {
	Token string
	// Secret is the webhook's secret token (setWebhook's secret_token),
	// checked on every update.
	Secret string
	URL    string
	Client *http.Client
	Log    *slog.Logger
}

// Lane sends Telegram messages.
type Lane struct{ cfg Config }

// New returns the lane.
func New(c Config) (*Lane, error) {
	if c.Token == "" {
		return nil, fmt.Errorf("%w: telegram: needs the bot's token", notify.ErrInvalid)
	}
	if c.URL == "" {
		c.URL = DefaultURL
	}
	c.URL = strings.TrimRight(c.URL, "/")
	if c.Client == nil {
		c.Client = &http.Client{Timeout: 70 * time.Second}
	}
	if c.Log == nil {
		c.Log = slog.New(slog.DiscardHandler)
	}
	return &Lane{cfg: c}, nil
}

// Info implements notify.Lane.
func (l *Lane) Info() notify.LaneInfo {
	return notify.LaneInfo{Name: "telegram", Address: notify.AddrTelegram, Note: "A Telegram bot. Free; the farmer links their chat from the app's settings."}
}

type result struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

func (l *Lane) call(ctx context.Context, method string, args any) (json.RawMessage, error) {
	body, _ := json.Marshal(args)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.cfg.URL+"/bot"+l.cfg.Token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return nil, notify.Rejected(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := l.cfg.Client.Do(req)
	if err != nil {
		// The error names the URL, which holds the token: say less.
		if ctx.Err() != nil {
			return nil, notify.Temporary(ctx.Err())
		}
		return nil, notify.Temporary(fmt.Errorf("telegram %s: the request failed", method))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var r result
	_ = json.Unmarshal(raw, &r)
	if r.OK {
		return r.Result, nil
	}
	why := fmt.Errorf("telegram %s: %d %s", method, r.ErrorCode, r.Description)
	switch code := max(r.ErrorCode, resp.StatusCode); {
	case code == http.StatusForbidden:
		return nil, notify.Gone(why) // blocked by the user, or the user is deactivated
	case code == http.StatusTooManyRequests || code >= 500:
		return nil, notify.Temporary(why)
	}
	return nil, notify.Rejected(why)
}

// Deliver implements notify.Lane.
func (l *Lane) Deliver(ctx context.Context, m notify.Message) (notify.Receipt, error) {
	chat := m.Recipient.Telegram
	if chat == "" {
		return notify.Receipt{}, notify.Rejected(errors.New("telegram: no chat"))
	}
	text := m.Text()
	if m.Link != "" {
		text += "\n\n" + m.Link
	}
	if utf8.RuneCountInString(text) > 4000 {
		text = string([]rune(text)[:3999]) + "…"
	}
	if m.Severity == notify.Urgent {
		text = "⚠️ " + text
	}
	raw, err := l.call(ctx, "sendMessage", map[string]any{"chat_id": chat, "text": text, "disable_notification": m.Severity == notify.Advice})
	if err != nil {
		return notify.Receipt{}, err
	}
	var sent struct {
		MessageID int64 `json:"message_id"`
	}
	_ = json.Unmarshal(raw, &sent)
	return notify.Receipt{Status: notify.StatusDelivered, ProviderID: strconv.FormatInt(sent.MessageID, 10)}, nil
}

// Update is the part of a Telegram update the lane reads.
type Update struct {
	ID      int64 `json:"update_id"`
	Message *struct {
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		Text string `json:"text"`
	} `json:"message"`
}

// Handle acts on one update: /start <code> links the chat, /start opts
// it in again, /stop (or STOP) opts it out; anything else gets a short
// answer.
func (l *Lane) Handle(ctx context.Context, rep notify.Reporter, u Update) {
	if u.Message == nil {
		return
	}
	chat := strconv.FormatInt(u.Message.Chat.ID, 10)
	text := strings.TrimSpace(u.Message.Text)
	reply := func(s string) {
		if _, err := l.call(ctx, "sendMessage", map[string]any{"chat_id": chat, "text": s}); err != nil {
			l.cfg.Log.Warn("telegram: reply", "err", err)
		}
	}
	cmd, arg, _ := strings.Cut(text, " ")
	cmd, _, _ = strings.Cut(cmd, "@") // /start@farm_bot
	switch {
	case cmd == "/start" && strings.TrimSpace(arg) != "":
		name, err := rep.Link(ctx, "telegram", strings.TrimSpace(arg), notify.AddrTelegram, chat)
		if err != nil {
			reply("That link has expired or was used already. Open the farm app's settings for a new one.")
			return
		}
		reply(fmt.Sprintf("Hello %s. Your farm alerts will come here. Send /stop to stop them.", name))
	case cmd == "/start":
		rep.Start(ctx, "telegram", notify.AddrTelegram, chat)
		reply("Your farm alerts will come here again. Send /stop to stop them.")
	case cmd == "/stop" || notify.IsStop(text):
		rep.Stop(ctx, "telegram", notify.AddrTelegram, chat)
		reply("Stopped. Send /start to get alerts again.")
	default:
		reply("This bot sends your farm alerts. Ask questions in the farm app. Send /stop to stop alerts.")
	}
}

// Webhook serves Telegram's updates, checking the secret token.
func (l *Lane) Webhook(rep notify.Reporter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		got := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
		if l.cfg.Secret == "" || subtle.ConstantTimeCompare([]byte(got), []byte(l.cfg.Secret)) != 1 {
			http.Error(w, "bad secret", http.StatusUnauthorized)
			return
		}
		var u Update
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&u); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		l.Handle(context.WithoutCancel(r.Context()), rep, u)
		w.WriteHeader(http.StatusOK)
	})
}

// Poll asks Telegram for updates until ctx is done, for an app with no
// public URL (it cannot be used while a webhook is set).
func (l *Lane) Poll(ctx context.Context, rep notify.Reporter) {
	var offset int64
	wait := time.Second
	for ctx.Err() == nil {
		raw, err := l.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 50, "allowed_updates": []string{"message"}})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			l.cfg.Log.Warn("telegram: get updates", "err", err, "wait", wait)
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			wait = min(wait*2, 5*time.Minute)
			continue
		}
		wait = time.Second
		var us []Update
		_ = json.Unmarshal(raw, &us)
		for _, u := range us {
			offset = max(offset, u.ID+1)
			l.Handle(ctx, rep, u)
		}
	}
}
