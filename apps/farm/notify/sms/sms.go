// Package sms is the SMS lane, with a provider picked by configuration:
// MSG91 (India, DLT templates) or Twilio (anywhere).
//
// PAID: every SMS is charged by the provider. In India every commercial
// SMS must be sent under the business's DLT registration with TRAI: the
// business, its sender ID and each template are registered first, and a
// template's variables hold at most 30 characters each, so an SMS carries
// a short alert and the app has the rest. That registration is a one-time
// setup by the owner.
package sms

import (
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // Twilio signs webhooks with HMAC-SHA1
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/apps/farm/notify"
)

func cut(text string, n int) string {
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) <= n {
		return text
	}
	r := []rune(text)
	return string(r[:n-1]) + "…"
}

func client(c *http.Client) *http.Client {
	if c == nil {
		return &http.Client{Timeout: 30 * time.Second}
	}
	return c
}

// classify turns an HTTP status into a lane error.
func classify(status int, why string, gone bool) error {
	err := errors.New(why)
	switch {
	case gone:
		return notify.Gone(err)
	case status == http.StatusTooManyRequests || status >= 500:
		return notify.Temporary(err)
	}
	return notify.Rejected(err)
}

// --- MSG91

// MSG91URL is MSG91's Flow API.
const MSG91URL = "https://control.msg91.com/api/v5/flow"

// MSG91Config configures the MSG91 lane.
type MSG91Config struct {
	AuthKey string
	// TemplateID is the MSG91 template (flow) linked to the DLT template.
	TemplateID string
	// Vars name the template's variables for the title and the text
	// (default "var1" and "var2"); each holds VarLen characters (default
	// 30, DLT's limit).
	Vars   [2]string
	VarLen int
	// CodeTemplateID is the MSG91 template linked to a DLT OTP template
	// for one-time sign-in codes, the code in its first variable. Empty:
	// the lane sends no codes.
	CodeTemplateID string
	URL            string
	Client         *http.Client
}

type msg91 struct{ cfg MSG91Config }

// MSG91 returns the SMS lane on MSG91.
func MSG91(c MSG91Config) (notify.Lane, error) {
	if c.AuthKey == "" || c.TemplateID == "" {
		return nil, fmt.Errorf("%w: sms msg91: needs an auth key and a template ID", notify.ErrInvalid)
	}
	if c.Vars[0] == "" {
		c.Vars = [2]string{"var1", "var2"}
	}
	if c.VarLen == 0 {
		c.VarLen = 30
	}
	if c.URL == "" {
		c.URL = MSG91URL
	}
	c.Client = client(c.Client)
	return &msg91{cfg: c}, nil
}

func (*msg91) Info() notify.LaneInfo {
	return notify.LaneInfo{
		Name: "sms", Address: notify.AddrPhone, Paid: true,
		Note: "SMS through MSG91 on a DLT template. Paid per SMS (about ₹0.15 to ₹0.30 in India); needs DLT registration with TRAI.",
	}
}

func (s *msg91) Deliver(ctx context.Context, m notify.Message) (notify.Receipt, error) {
	to := strings.TrimPrefix(m.Recipient.Phone, "+")
	if to == "" {
		return notify.Receipt{}, notify.Rejected(errors.New("sms: no phone"))
	}
	tmpl, rcpt := s.cfg.TemplateID, map[string]string{"mobiles": to, s.cfg.Vars[0]: cut(m.Title, s.cfg.VarLen), s.cfg.Vars[1]: cut(m.Body, s.cfg.VarLen)}
	if m.Kind == notify.KindCode {
		if s.cfg.CodeTemplateID == "" || m.Code == "" {
			return notify.Receipt{}, notify.Rejected(errors.New("sms msg91: no DLT OTP template for sign-in codes"))
		}
		tmpl, rcpt = s.cfg.CodeTemplateID, map[string]string{"mobiles": to, s.cfg.Vars[0]: m.Code}
	}
	body, _ := json.Marshal(map[string]any{"template_id": tmpl, "short_url": "0", "recipients": []any{rcpt}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.URL, strings.NewReader(string(body)))
	if err != nil {
		return notify.Receipt{}, notify.Rejected(err)
	}
	req.Header.Set("authkey", s.cfg.AuthKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := s.cfg.Client.Do(req)
	if err != nil {
		return notify.Receipt{}, notify.Temporary(fmt.Errorf("sms msg91: %w", err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode/100 == 2 && out.Type == "success" {
		return notify.Receipt{Status: notify.StatusSent, ProviderID: out.Message}, nil
	}
	return notify.Receipt{}, classify(resp.StatusCode, fmt.Sprintf("sms msg91: HTTP %d %s", resp.StatusCode, out.Message), false)
}

// --- Twilio

// TwilioURL is Twilio's API.
const TwilioURL = "https://api.twilio.com"

// TwilioConfig configures the Twilio lane.
type TwilioConfig struct {
	AccountSID string
	AuthToken  string
	// From is the sending number, or MessagingService a messaging
	// service's SID; one of them.
	From             string
	MessagingService string
	// CallbackURL is the public URL of [Twilio.Webhook], for delivery
	// statuses; empty for none.
	CallbackURL string
	URL         string
	Client      *http.Client
	Log         *slog.Logger
}

// Twilio is the SMS lane on Twilio.
type Twilio struct{ cfg TwilioConfig }

// NewTwilio returns the SMS lane on Twilio.
func NewTwilio(c TwilioConfig) (*Twilio, error) {
	if c.AccountSID == "" || c.AuthToken == "" || (c.From == "") == (c.MessagingService == "") {
		return nil, fmt.Errorf("%w: sms twilio: needs the account SID, the auth token, and a from number or a messaging service", notify.ErrInvalid)
	}
	if c.URL == "" {
		c.URL = TwilioURL
	}
	c.URL = strings.TrimRight(c.URL, "/")
	c.Client = client(c.Client)
	if c.Log == nil {
		c.Log = slog.New(slog.DiscardHandler)
	}
	return &Twilio{cfg: c}, nil
}

// Info implements notify.Lane.
func (*Twilio) Info() notify.LaneInfo {
	return notify.LaneInfo{
		Name: "sms", Address: notify.AddrPhone, Paid: true,
		Note: "SMS through Twilio. Paid per SMS; Indian numbers still need DLT registration.",
	}
}

// Twilio error codes: the person opted out, or try again later.
var (
	twilioGone      = []int{21610}
	twilioTemporary = []int{20429, 30001, 30008}
)

// Deliver implements notify.Lane.
func (s *Twilio) Deliver(ctx context.Context, m notify.Message) (notify.Receipt, error) {
	if m.Recipient.Phone == "" {
		return notify.Receipt{}, notify.Rejected(errors.New("sms: no phone"))
	}
	text := m.Body
	if m.Title != "" {
		text = m.Title + ": " + m.Body
	}
	form := url.Values{"To": {m.Recipient.Phone}, "Body": {cut(text, 320)}}
	if s.cfg.From != "" {
		form.Set("From", s.cfg.From)
	} else {
		form.Set("MessagingServiceSid", s.cfg.MessagingService)
	}
	if s.cfg.CallbackURL != "" {
		form.Set("StatusCallback", s.cfg.CallbackURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.URL+"/2010-04-01/Accounts/"+url.PathEscape(s.cfg.AccountSID)+"/Messages.json", strings.NewReader(form.Encode()))
	if err != nil {
		return notify.Receipt{}, notify.Rejected(err)
	}
	req.SetBasicAuth(s.cfg.AccountSID, s.cfg.AuthToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.cfg.Client.Do(req)
	if err != nil {
		return notify.Receipt{}, notify.Temporary(fmt.Errorf("sms twilio: %w", err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		SID     string `json:"sid"`
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode/100 == 2 && out.SID != "" {
		return notify.Receipt{Status: notify.StatusSent, ProviderID: out.SID}, nil
	}
	why := fmt.Sprintf("sms twilio: HTTP %d %d %s", resp.StatusCode, out.Code, out.Message)
	if slices.Contains(twilioTemporary, out.Code) {
		return notify.Receipt{}, notify.Temporary(errors.New(why))
	}
	return notify.Receipt{}, classify(resp.StatusCode, why, slices.Contains(twilioGone, out.Code))
}

// Webhook serves Twilio's status callbacks and incoming messages (set it
// as the number's "a message comes in" webhook too, for STOP), checking
// Twilio's signature against CallbackURL.
func (s *Twilio) Webhook(rep notify.Reporter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if s.cfg.CallbackURL == "" || !TwilioSigned(s.cfg.AuthToken, s.cfg.CallbackURL, r.PostForm, r.Header.Get("X-Twilio-Signature")) {
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		ctx := context.WithoutCancel(r.Context())
		f := r.PostForm
		if st := f.Get("MessageStatus"); st != "" {
			status := map[string]string{
				"sent": notify.StatusSent, "delivered": notify.StatusDelivered, "read": notify.StatusRead,
				"undelivered": notify.StatusFailed, "failed": notify.StatusFailed,
			}[st]
			if status != "" {
				errText := ""
				if c := f.Get("ErrorCode"); c != "" {
					errText = "twilio " + c
				}
				rep.Status(ctx, "sms", f.Get("MessageSid"), status, errText)
			}
		} else if body := f.Get("Body"); body != "" {
			switch {
			case notify.IsStop(body):
				rep.Stop(ctx, "sms", notify.AddrPhone, f.Get("From"))
			case notify.IsStart(body):
				rep.Start(ctx, "sms", notify.AddrPhone, f.Get("From"))
			}
		}
		w.Header().Set("Content-Type", "text/xml")
		_, _ = io.WriteString(w, "<Response></Response>")
	})
}

// TwilioSigned checks Twilio's X-Twilio-Signature: base64 of HMAC-SHA1,
// keyed by the auth token, over the URL and the POST parameters sorted by
// name, each name followed by its value.
func TwilioSigned(token, fullURL string, form url.Values, sig string) bool {
	keys := make([]string, 0, len(form))
	for k := range form {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(fullURL)
	for _, k := range keys {
		for _, v := range form[k] {
			b.WriteString(k + v)
		}
	}
	mac := hmac.New(sha1.New, []byte(token))
	mac.Write([]byte(b.String()))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return sig != "" && hmac.Equal([]byte(sig), []byte(want))
}
