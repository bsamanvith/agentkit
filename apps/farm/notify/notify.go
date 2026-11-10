package notify

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Errors a lane or a sender returns, and callers branch on with errors.Is.
var (
	// ErrSkipped is a lane that did not try: the person has no address on
	// it, has not agreed to it, the lane is off, or it is over its cap. A
	// chain moves on to its next sender.
	ErrSkipped = errors.New("notify: skipped")
	// ErrNoAddress: the person has no address on the lane.
	ErrNoAddress = fmt.Errorf("%w: no address on this lane", ErrSkipped)
	// ErrNoConsent: the person has not agreed to messages on the lane, or
	// said STOP.
	ErrNoConsent = fmt.Errorf("%w: no consent on this lane", ErrSkipped)
	// ErrLaneOff: the lane is not configured (no keys).
	ErrLaneOff = fmt.Errorf("%w: the lane is off", ErrSkipped)
	// ErrOverCap: the lane reached its daily or monthly cap.
	ErrOverCap = fmt.Errorf("%w: the lane is over its cost cap", ErrSkipped)

	// ErrTemporary is a lane that could not send now (the network, a 5xx,
	// a rate limit): the message is tried again later.
	ErrTemporary = errors.New("notify: temporary failure")
	// ErrGone is a person who is no longer reachable on the lane: they
	// blocked the bot, unsubscribed, or the push subscription expired. The
	// guard records it as a STOP.
	ErrGone = errors.New("notify: the person left the lane")
	// ErrRejected is a lane that refused the message for good (a bad
	// number, a template it does not know).
	ErrRejected = errors.New("notify: rejected")

	// ErrNotFound is a recipient or a message the store does not have.
	ErrNotFound = errors.New("notify: not found")
	// ErrInvalid is a message, recipient or configuration that is not
	// well formed.
	ErrInvalid = errors.New("notify: invalid")
)

// Temporary wraps err as an [ErrTemporary].
func Temporary(err error) error { return fmt.Errorf("%w: %w", ErrTemporary, err) }

// Rejected wraps err as an [ErrRejected].
func Rejected(err error) error { return fmt.Errorf("%w: %w", ErrRejected, err) }

// Gone wraps err as an [ErrGone].
func Gone(err error) error { return fmt.Errorf("%w: %w", ErrGone, err) }

// Severities, most pressing first (the automation package's words).
const (
	Urgent  = "urgent"
	Warning = "warning"
	Advice  = "advice"
)

// Kinds of message.
const (
	KindAlert  = "alert"  // a notice from an automation
	KindDigest = "digest" // a summary (the week, the morning brief)
	KindReply  = "reply"  // an answer to something the person sent
	KindTest   = "test"   // a test from the settings page
	// KindCode is a one-time sign-in code the person asked for, in Code.
	// It never goes through the outbox or the routes: [SendCode] sends it
	// at once.
	KindCode = "code"
)

// Message is one message to one person. Its ID is its idempotency key: the
// outbox keeps one message per ID, and a lane sends one ID at most once.
type Message struct {
	ID string `json:"id"`
	// To is the recipient's ID.
	To string `json:"to"`
	// Farm and Notice say what it is about, when it comes from a notice.
	Farm     string `json:"farm,omitempty"`
	Notice   string `json:"notice,omitempty"`
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	// Code is a [KindCode] message's code alone, for lanes whose templates
	// carry it as a parameter (WhatsApp's authentication templates, a DLT
	// OTP template). It is never stored.
	Code string `json:"-"`
	// Link opens the app at the message's subject, when there is one.
	Link      string    `json:"link,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	// ExpiresAt is when it is no longer worth sending (yesterday's rain
	// warning); zero for never.
	ExpiresAt time.Time `json:"expires_at,omitzero"`

	// Recipient is the person, filled in by the dispatcher at send time
	// (so a STOP or a new number applies at once).
	Recipient Recipient `json:"-"`
	// Sent are the lanes this message was already sent on, filled in by
	// the dispatcher: a retry does not send again on them, and a chain
	// that already got through on one of its lanes stops there.
	Sent []string `json:"-"`
}

// check checks m's fields.
func (m Message) check() error {
	switch {
	case strings.TrimSpace(m.ID) == "" || len(m.ID) > 200:
		return fmt.Errorf("%w: a message needs an ID of 1 to 200 bytes", ErrInvalid)
	case m.To == "":
		return fmt.Errorf("%w: a message needs a recipient", ErrInvalid)
	case strings.TrimSpace(m.Title) == "" && strings.TrimSpace(m.Body) == "":
		return fmt.Errorf("%w: a message needs a title or a body", ErrInvalid)
	case len(m.Title) > 500 || len(m.Body) > 8<<10:
		return fmt.Errorf("%w: a message's title is up to 500 bytes and its body 8 KB", ErrInvalid)
	}
	switch m.Severity {
	case Urgent, Warning, Advice:
	default:
		return fmt.Errorf("%w: severity %q: want urgent, warning or advice", ErrInvalid, m.Severity)
	}
	return nil
}

// Text is the title and the body as one plain text, for lanes with no
// subject line.
func (m Message) Text() string {
	switch {
	case m.Title == "":
		return m.Body
	case m.Body == "":
		return m.Title
	}
	return m.Title + "\n\n" + m.Body
}

// AddressKind is what a lane sends to.
type AddressKind string

// The address kinds.
const (
	AddrNone     AddressKind = "none"     // the inbox, the log: no address
	AddrPhone    AddressKind = "phone"    // E.164, such as +919876543210
	AddrEmail    AddressKind = "email"    // name@example.com
	AddrTelegram AddressKind = "telegram" // a chat ID, set when the person starts the bot
	AddrPush     AddressKind = "push"     // the browser's push subscriptions
)

// PushSubscription is one browser's web push subscription (the
// PushSubscription's JSON in the browser).
type PushSubscription struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
	CreatedAt time.Time `json:"created_at,omitzero"`
}

// Consent is a person's answer on one lane, and where it came from.
type Consent struct {
	OptedIn bool      `json:"opted_in"`
	At      time.Time `json:"at"`
	// Source is where it came from: the settings page, "stop" (a STOP
	// message), the lane itself (blocked the bot, an expired push
	// subscription), the Telegram start link.
	Source string `json:"source"`
}

// QuietHours are the hours with no messages except urgent ones, in the
// person's local time, as minutes after midnight. Start after End spans
// midnight (21:00 to 06:00).
type QuietHours struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// DefaultQuiet is 9 pm to 6 am.
var DefaultQuiet = QuietHours{Start: 21 * 60, End: 6 * 60}

// IndiaOffset is India's time (UTC+5:30) in minutes.
const IndiaOffset = 330

// Recipient is a person who gets messages: their addresses, which lanes
// they agreed to, their language and quiet hours, and the plots they hear
// about.
type Recipient struct {
	ID       string             `json:"id"`
	Name     string             `json:"name"`
	Phone    string             `json:"phone,omitempty"`
	Email    string             `json:"email,omitempty"`
	Telegram string             `json:"telegram,omitempty"`
	Push     []PushSubscription `json:"push,omitempty"`
	// Lang is the language of messages (BCP 47: te, en, hi); "en" when
	// empty.
	Lang string `json:"lang,omitempty"`
	// UTCOffset is the person's time zone as minutes east of UTC; India's
	// when zero (the binary has no time zone database).
	UTCOffset int `json:"utc_offset,omitempty"`
	// Quiet is their quiet hours; [DefaultQuiet] when nil. A zero-length
	// span (Start == End) means none.
	Quiet *QuietHours `json:"quiet,omitempty"`
	// Farms are the plots they hear about; empty for every plot.
	Farms []string `json:"farms,omitempty"`
	// Consent is their answer per lane name.
	Consent map[string]Consent `json:"consent,omitempty"`
	// LinkCode is a one-time code that links a chat to them (Telegram's
	// start link); empty when there is none.
	LinkCode  string    `json:"link_code,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Address is r's address of kind k, or "" when there is none. Push is
// "push" when r has a subscription.
func (r Recipient) Address(k AddressKind) string {
	switch k {
	case AddrNone:
		return "-"
	case AddrPhone:
		return r.Phone
	case AddrEmail:
		return r.Email
	case AddrTelegram:
		return r.Telegram
	case AddrPush:
		if len(r.Push) > 0 {
			return "push"
		}
	}
	return ""
}

// OptedIn reports whether r agreed to lane.
func (r Recipient) OptedIn(lane string) bool { return r.Consent[lane].OptedIn }

// Offset is r's UTC offset, India's by default.
func (r Recipient) Offset() time.Duration {
	if r.UTCOffset == 0 {
		return IndiaOffset * time.Minute
	}
	return time.Duration(r.UTCOffset) * time.Minute
}

// QuietUntil reports whether t is in r's quiet hours, and when they end.
func (r Recipient) QuietUntil(t time.Time) (time.Time, bool) {
	q := DefaultQuiet
	if r.Quiet != nil {
		q = *r.Quiet
	}
	if q.Start == q.End {
		return time.Time{}, false
	}
	local := t.UTC().Add(r.Offset())
	mins := local.Hour()*60 + local.Minute()
	in := false
	if q.Start < q.End {
		in = mins >= q.Start && mins < q.End
	} else {
		in = mins >= q.Start || mins < q.End
	}
	if !in {
		return time.Time{}, false
	}
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	end := day.Add(time.Duration(q.End) * time.Minute)
	if !end.After(local) {
		end = end.AddDate(0, 0, 1)
	}
	return end.Add(-r.Offset()), true
}

func (r Recipient) check() error {
	switch {
	case strings.TrimSpace(r.ID) == "" || len(r.ID) > 100:
		return fmt.Errorf("%w: a recipient needs an ID of 1 to 100 bytes", ErrInvalid)
	case len(r.Name) > 200:
		return fmt.Errorf("%w: a name is up to 200 bytes", ErrInvalid)
	case r.Phone != "" && !validPhone(r.Phone):
		return fmt.Errorf("%w: phone %q: want +, the country code and the number (+919876543210)", ErrInvalid, r.Phone)
	case r.Email != "" && !validEmail(r.Email):
		return fmt.Errorf("%w: email %q is not an address", ErrInvalid, r.Email)
	case r.UTCOffset < -14*60 || r.UTCOffset > 14*60:
		return fmt.Errorf("%w: utc_offset %d: want -840 to 840 minutes", ErrInvalid, r.UTCOffset)
	case len(r.Push) > 10:
		return fmt.Errorf("%w: up to 10 push subscriptions", ErrInvalid)
	}
	if q := r.Quiet; q != nil && (q.Start < 0 || q.Start >= 24*60 || q.End < 0 || q.End >= 24*60) {
		return fmt.Errorf("%w: quiet hours are minutes after midnight, 0 to 1439", ErrInvalid)
	}
	for _, p := range r.Push {
		if !strings.HasPrefix(p.Endpoint, "https://") || p.Keys.P256dh == "" || p.Keys.Auth == "" {
			return fmt.Errorf("%w: a push subscription needs an https endpoint and its keys", ErrInvalid)
		}
	}
	return nil
}

// Check checks r's fields: an ID, a phone in E.164, an email address,
// quiet hours and push subscriptions that are well formed.
func (r Recipient) Check() error { return r.check() }

func validPhone(p string) bool {
	if len(p) < 9 || len(p) > 16 || p[0] != '+' || p[1] == '0' {
		return false
	}
	for _, c := range p[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func validEmail(e string) bool {
	at := strings.LastIndexByte(e, '@')
	return at > 0 && at < len(e)-3 && strings.Contains(e[at:], ".") && !strings.ContainsAny(e, " \r\n<>,;") && len(e) <= 254
}

// Delivery statuses, in the order they happen.
const (
	StatusSending   = "sending"   // claimed, the lane is being asked
	StatusSent      = "sent"      // the lane took it
	StatusDelivered = "delivered" // it reached the phone or the inbox
	StatusRead      = "read"      // the person opened it (WhatsApp, some email)
	StatusFailed    = "failed"    // the lane refused it, or it did not arrive
	StatusSkipped   = "skipped"   // not tried: no address, no consent, off, over the cap
)

// Rank orders statuses so a late "delivered" does not undo a "read".
func Rank(status string) int {
	switch status {
	case StatusSending:
		return 1
	case StatusSent:
		return 2
	case StatusDelivered:
		return 3
	case StatusRead:
		return 4
	case StatusFailed:
		return 5
	}
	return 0
}

// Accepted reports whether status means the lane took the message.
func Accepted(status string) bool {
	return status == StatusSent || status == StatusDelivered || status == StatusRead
}

// Receipt is what one lane did with one message.
type Receipt struct {
	Lane   string `json:"lane"`
	Status string `json:"status"`
	// ProviderID is the lane's own ID of the message (a WhatsApp wamid, a
	// Twilio SID, an email Message-ID), for status updates.
	ProviderID string `json:"provider_id,omitempty"`
	// Cost is what the lane charged, in paise (hundredths of a rupee).
	Cost  int64  `json:"cost,omitempty"`
	Error string `json:"error,omitempty"`
	// Repeat is a receipt of an earlier send: this call did not send.
	Repeat bool `json:"repeat,omitempty"`
}

// Sender sends a message: a guarded lane, or a composite of senders.
type Sender interface {
	// Name names it in logs and in the configuration it came from.
	Name() string
	// Lanes are the lanes it may send on (itself, for a lane).
	Lanes() []string
	// Send sends m and says what each lane did. The error is nil when at
	// least one lane took it (and, for a fan-out, none failed for a
	// while); otherwise an ErrSkipped, ErrTemporary or ErrRejected.
	Send(ctx context.Context, m Message) ([]Receipt, error)
}

// LaneInfo describes a lane.
type LaneInfo struct {
	Name    string      `json:"name"`
	Address AddressKind `json:"address"`
	// Paid is a lane that charges per message.
	Paid bool `json:"paid"`
	// Note says what it is and what it costs, for the settings page.
	Note string `json:"note,omitempty"`
}

// Lane is one way to reach a person. Implementations only send: the
// checks, the records and the cost caps are [Guard]'s.
type Lane interface {
	Info() LaneInfo
	// Deliver sends m to m.Recipient's address on the lane. A lane
	// returns an ErrTemporary when trying later may work, an ErrGone when
	// the person left the lane, and an ErrRejected otherwise.
	Deliver(ctx context.Context, m Message) (Receipt, error)
}
