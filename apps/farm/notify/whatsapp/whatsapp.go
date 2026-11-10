// Package whatsapp is the WhatsApp lane, on Meta's WhatsApp Cloud API
// directly (no reseller in between).
//
// PAID: messages the app starts are template messages, charged per
// message by Meta (utility templates in India cost about ₹0.12 to ₹0.15
// each). Replies within 24 hours of a farmer's own message are free, up
// to Meta's monthly allowance. The business must pass Meta's business
// verification, and each template must be approved in each language,
// before the lane can send.
//
// Alerts go out as one template (Config.Template, "farm_alert" by
// default) with two body parameters: the title and the text. Its webhook
// ([Lane.Webhook]) reports sent, delivered, read and failed, and STOP.
package whatsapp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/apps/farm/notify"
)

// DefaultURL is the Graph API's base URL.
const DefaultURL = "https://graph.facebook.com/v23.0"

// Config configures the lane.
type Config struct {
	// Token is a system user's access token with whatsapp_business_messaging.
	Token string
	// PhoneNumberID is the sending number's ID in WhatsApp Manager.
	PhoneNumberID string
	// Template is the approved template's name (default "farm_alert"),
	// with two body parameters: the title and the text.
	Template string
	// CodeTemplate is an approved authentication template (a copy-code
	// button) for one-time sign-in codes; WhatsApp allows codes in no
	// other kind. Empty: the lane sends no codes.
	CodeTemplate string
	// Languages are the languages the template is approved in (default
	// ["en"]); a person's language not among them gets the first.
	Languages []string
	// AppSecret checks the webhook's signatures (X-Hub-Signature-256).
	AppSecret string
	// VerifyToken answers the webhook's subscription check.
	VerifyToken string
	// URL is the API's base URL (default [DefaultURL]).
	URL string
	// Client is the HTTP client (default one with a 30s timeout).
	Client *http.Client
	Log    *slog.Logger
}

// Lane sends WhatsApp messages.
type Lane struct{ cfg Config }

// New returns the lane.
func New(c Config) (*Lane, error) {
	if c.Token == "" || c.PhoneNumberID == "" {
		return nil, fmt.Errorf("%w: whatsapp: needs a token and a phone number ID", notify.ErrInvalid)
	}
	if c.Template == "" {
		c.Template = "farm_alert"
	}
	if len(c.Languages) == 0 {
		c.Languages = []string{"en"}
	}
	if c.URL == "" {
		c.URL = DefaultURL
	}
	c.URL = strings.TrimRight(c.URL, "/")
	if c.Client == nil {
		c.Client = &http.Client{Timeout: 30 * time.Second}
	}
	if c.Log == nil {
		c.Log = slog.New(slog.DiscardHandler)
	}
	return &Lane{cfg: c}, nil
}

// Info implements notify.Lane.
func (l *Lane) Info() notify.LaneInfo {
	return notify.LaneInfo{
		Name: "whatsapp", Address: notify.AddrPhone, Paid: true,
		Note: "WhatsApp Cloud API (Meta). Paid per template message (about ₹0.12 to ₹0.15 in India); replies within 24 hours of the farmer's message are free. Needs Meta business verification and approved templates.",
	}
}

// param makes text a template parameter: no new lines or tabs, no more
// than four spaces in a row, and at most n characters.
func param(text string, n int) string {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		text = "-"
	}
	if utf8.RuneCountInString(text) > n {
		r := []rune(text)
		text = string(r[:n-1]) + "…"
	}
	return text
}

type sendBody struct {
	Product  string    `json:"messaging_product"`
	To       string    `json:"to"`
	Type     string    `json:"type"`
	Template *template `json:"template,omitempty"`
	Text     *struct {
		Body string `json:"body"`
	} `json:"text,omitempty"`
}

type template struct {
	Name     string `json:"name"`
	Language struct {
		Code string `json:"code"`
	} `json:"language"`
	Components []component `json:"components"`
}

type component struct {
	Type       string   `json:"type"`
	SubType    string   `json:"sub_type,omitempty"`
	Index      string   `json:"index,omitempty"`
	Parameters []tparam `json:"parameters"`
}

type tparam struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (l *Lane) lang(want string) string {
	if want == "" {
		return l.cfg.Languages[0]
	}
	if slices.Contains(l.cfg.Languages, want) {
		return want
	}
	return l.cfg.Languages[0]
}

// Deliver implements notify.Lane: a reply as text (free inside the 24
// hours), anything else as the template.
func (l *Lane) Deliver(ctx context.Context, m notify.Message) (notify.Receipt, error) {
	to := strings.TrimPrefix(m.Recipient.Phone, "+")
	if to == "" {
		return notify.Receipt{}, notify.Rejected(errors.New("whatsapp: no phone"))
	}
	b := sendBody{Product: "whatsapp", To: to}
	switch m.Kind {
	case notify.KindCode:
		// An authentication template: the code in the body and in the
		// copy-code button.
		if l.cfg.CodeTemplate == "" || m.Code == "" {
			return notify.Receipt{}, notify.Rejected(errors.New("whatsapp: no authentication template for sign-in codes"))
		}
		t := &template{Name: l.cfg.CodeTemplate}
		t.Language.Code = l.lang(m.Recipient.Lang)
		code := []tparam{{"text", m.Code}}
		t.Components = []component{{Type: "body", Parameters: code}, {Type: "button", SubType: "url", Index: "0", Parameters: code}}
		b.Type, b.Template = "template", t
	case notify.KindReply:
		b.Type = "text"
		b.Text = &struct {
			Body string `json:"body"`
		}{Body: m.Text()}
	default:
		t := &template{Name: l.cfg.Template}
		t.Language.Code = l.lang(m.Recipient.Lang)
		c := component{Type: "body"}
		for _, p := range []string{param(m.Title, 200), param(m.Body, 700)} {
			c.Parameters = append(c.Parameters, tparam{"text", p})
		}
		t.Components = []component{c}
		b.Type, b.Template = "template", t
	}
	body, _ := json.Marshal(b)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.cfg.URL+"/"+l.cfg.PhoneNumberID+"/messages", bytes.NewReader(body))
	if err != nil {
		return notify.Receipt{}, notify.Rejected(err)
	}
	req.Header.Set("Authorization", "Bearer "+l.cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := l.cfg.Client.Do(req)
	if err != nil {
		return notify.Receipt{}, notify.Temporary(fmt.Errorf("whatsapp: %w", err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
		Error *apiError `json:"error"`
	}
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode/100 == 2 && len(out.Messages) > 0 {
		return notify.Receipt{Status: notify.StatusSent, ProviderID: out.Messages[0].ID}, nil
	}
	return notify.Receipt{}, classify(resp.StatusCode, out.Error)
}

type apiError struct {
	Message string `json:"message"`
	Code    int    `json:"code"`
	Details struct {
		Details string `json:"details"`
	} `json:"error_data"`
}

// Error codes that mean "try again later", and that mean the person
// stopped messages from businesses. The rest are final.
var (
	temporaryCodes = []int{1, 2, 4, 80007, 130429, 131000, 131016, 131048, 131056}
	goneCodes      = []int{131050}
)

func classify(status int, e *apiError) error {
	why := fmt.Sprintf("whatsapp: HTTP %d", status)
	code := 0
	if e != nil {
		why, code = fmt.Sprintf("whatsapp: %d %s %s", e.Code, e.Message, e.Details.Details), e.Code
	}
	switch {
	case slices.Contains(goneCodes, code):
		return notify.Gone(errors.New(why))
	case status == http.StatusTooManyRequests || status >= 500 || slices.Contains(temporaryCodes, code):
		return notify.Temporary(errors.New(why))
	}
	return notify.Rejected(errors.New(why))
}

// Webhook serves Meta's webhook for the number: the subscription check
// (GET), and delivery statuses and messages (POST, signed with the app
// secret). A message saying STOP opts the person out; START opts them in.
func (l *Lane) Webhook(rep notify.Reporter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			q := r.URL.Query()
			if l.cfg.VerifyToken == "" || q.Get("hub.mode") != "subscribe" || !hmac.Equal([]byte(q.Get("hub.verify_token")), []byte(l.cfg.VerifyToken)) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			_, _ = io.WriteString(w, q.Get("hub.challenge"))
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		if !l.signed(raw, r.Header.Get("X-Hub-Signature-256")) {
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		var ev event
		if err := json.Unmarshal(raw, &ev); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		ctx := context.WithoutCancel(r.Context())
		for _, e := range ev.Entry {
			for _, c := range e.Changes {
				for _, s := range c.Value.Statuses {
					errText := ""
					if len(s.Errors) > 0 {
						errText = fmt.Sprintf("%d %s", s.Errors[0].Code, s.Errors[0].Title)
					}
					rep.Status(ctx, "whatsapp", s.ID, s.Status, errText)
				}
				for _, m := range c.Value.Messages {
					text := m.Text.Body
					if m.Button.Text != "" {
						text = m.Button.Text
					}
					phone := "+" + strings.TrimPrefix(m.From, "+")
					switch {
					case notify.IsStop(text):
						rep.Stop(ctx, "whatsapp", notify.AddrPhone, phone)
					case notify.IsStart(text):
						rep.Start(ctx, "whatsapp", notify.AddrPhone, phone)
					default:
						l.cfg.Log.Info("whatsapp: a message from a farmer", "type", m.Type)
					}
				}
			}
		}
		w.WriteHeader(http.StatusOK)
	})
}

func (l *Lane) signed(raw []byte, header string) bool {
	if l.cfg.AppSecret == "" {
		return false
	}
	sig, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	got, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(l.cfg.AppSecret))
	mac.Write(raw)
	return hmac.Equal(got, mac.Sum(nil))
}

type event struct {
	Entry []struct {
		Changes []struct {
			Value struct {
				Statuses []struct {
					ID     string `json:"id"`
					Status string `json:"status"`
					Errors []struct {
						Code  int    `json:"code"`
						Title string `json:"title"`
					} `json:"errors"`
				} `json:"statuses"`
				Messages []struct {
					From string `json:"from"`
					Type string `json:"type"`
					Text struct {
						Body string `json:"body"`
					} `json:"text"`
					Button struct {
						Text string `json:"text"`
					} `json:"button"`
				} `json:"messages"`
			} `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}
