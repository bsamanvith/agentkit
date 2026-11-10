package accounts

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
)

// One-time codes: six digits, ten minutes, five tries.
const (
	OneTimeCodeTTL      = 10 * time.Minute
	OneTimeCodeAttempts = 5
	oneTimeCodeDigits   = 6
)

// OneTimeCodeLane signs in with a one-time code sent to a phone number or
// an email address through a [Sender] (SMS, WhatsApp, email: message
// lanes). A code is six digits, works once, for [OneTimeCodeTTL], and for
// [OneTimeCodeAttempts] tries; a new code to the same destination replaces
// the last. The store keeps its SHA-256 only.
type OneTimeCodeLane struct {
	name      string
	sender    Sender
	store     Store
	clock     clock.Clock
	normalize func(string) (string, error)
	text      func(code string) string
	email     bool
}

// CodeOption configures a [OneTimeCodeLane].
type CodeOption func(*OneTimeCodeLane)

// WithCodeClock sets the clock of the codes' expiry.
func WithCodeClock(c clock.Clock) CodeOption { return func(l *OneTimeCodeLane) { l.clock = c } }

// WithCodeText sets the message that carries a code (default: English,
// with the code first so a phone can offer to fill it in).
func WithCodeText(f func(code string) string) CodeOption {
	return func(l *OneTimeCodeLane) { l.text = f }
}

// NewPhoneLane returns a lane named name that sends codes to phone numbers.
// A number without a country code takes country (digits, "91" for India);
// country "" requires one.
func NewPhoneLane(name, country string, sender Sender, store Store, opts ...CodeOption) *OneTimeCodeLane {
	l := newCodeLane(name, sender, store, opts)
	l.normalize = func(s string) (string, error) { return NormalizePhone(s, country) }
	return l
}

// NewEmailLane returns a lane named name that sends codes to email
// addresses.
func NewEmailLane(name string, sender Sender, store Store, opts ...CodeOption) *OneTimeCodeLane {
	l := newCodeLane(name, sender, store, opts)
	l.normalize, l.email = NormalizeEmail, true
	return l
}

func newCodeLane(name string, sender Sender, store Store, opts []CodeOption) *OneTimeCodeLane {
	l := &OneTimeCodeLane{
		name: name, sender: sender, store: store, clock: clock.Real(),
		text: func(code string) string {
			return code + " is your sign-in code. It works for 10 minutes. Do not share it."
		},
	}
	for _, o := range opts {
		o(l)
	}
	return l
}

// Name implements [Lane].
func (l *OneTimeCodeLane) Name() string { return l.name }

// Kind implements [Lane].
func (*OneTimeCodeLane) Kind() Kind { return KindCode }

// Normalize implements [CodeLane].
func (l *OneTimeCodeLane) Normalize(to string) (string, error) { return l.normalize(to) }

func (l *OneTimeCodeLane) key(to string) string { return "otp:" + l.name + ":" + sha(to) }

func (l *OneTimeCodeLane) hash(to, code string) string {
	return sha(l.name + "\x00" + to + "\x00" + code)
}

// Send implements [CodeLane].
func (l *OneTimeCodeLane) Send(ctx context.Context, to string) error {
	var b strings.Builder
	for range oneTimeCodeDigits {
		b.WriteByte(byte('0' + randIntn(10)))
	}
	code := b.String()
	c := Challenge{Purpose: purposeOTP, Hash: l.hash(to, code), Expires: l.clock.Now().Add(OneTimeCodeTTL)}
	if err := l.store.PutChallenge(ctx, l.key(to), c); err != nil {
		return err
	}
	if err := l.sender.Send(ctx, to, l.text(code)); err != nil {
		_ = l.store.DeleteChallenge(ctx, l.key(to))
		return fmt.Errorf("accounts: sending the code: %w", err)
	}
	return nil
}

// Verify implements [CodeLane].
func (l *OneTimeCodeLane) Verify(ctx context.Context, to, code string) (Identity, error) {
	k := l.key(to)
	c, err := l.store.AttemptChallenge(ctx, k)
	switch {
	case errors.Is(err, ErrNotFound):
		return Identity{}, ErrBadSecret
	case err != nil:
		return Identity{}, err
	}
	if c.Purpose != purposeOTP || c.Attempts > OneTimeCodeAttempts || !l.clock.Now().Before(c.Expires) {
		_ = l.store.DeleteChallenge(ctx, k)
		return Identity{}, ErrBadSecret
	}
	code = strings.TrimSpace(code)
	if subtle.ConstantTimeCompare([]byte(l.hash(to, code)), []byte(c.Hash)) != 1 {
		if c.Attempts == OneTimeCodeAttempts {
			_ = l.store.DeleteChallenge(ctx, k)
		}
		return Identity{}, ErrBadSecret
	}
	if err := l.store.DeleteChallenge(ctx, k); err != nil {
		if errors.Is(err, ErrNotFound) {
			return Identity{}, ErrBadSecret
		}
		return Identity{}, err
	}
	id := Identity{Lane: l.name, Subject: to}
	if l.email {
		id.Email, id.EmailVerified = to, true
	} else {
		id.Phone = to
	}
	return id, nil
}

// NormalizePhone returns a phone number in E.164 ("+919876543210"), or
// [ErrInvalid]. Spaces, dashes, dots and brackets are dropped; "00" is
// read as "+". A number without a country code takes country, after a
// leading trunk "0" is dropped; with country "" it is invalid. Indian
// numbers must be ten digits starting 6 to 9.
func NormalizePhone(s, country string) (string, error) {
	bad := func(why string) (string, error) { return "", fmt.Errorf("%w: phone number: %s", ErrInvalid, why) }
	var d strings.Builder
	plus := false
	for i, r := range strings.TrimSpace(s) {
		switch {
		case r >= '0' && r <= '9':
			d.WriteRune(r)
		case r == '+' && i == 0:
			plus = true
		case strings.ContainsRune(" -.()", r):
		default:
			return bad("only digits, and + at the start")
		}
	}
	n := d.String()
	switch {
	case plus:
	case strings.HasPrefix(n, "00"):
		n = n[2:]
	case country == "":
		return bad("give the country code, starting with +")
	default:
		n = country + strings.TrimPrefix(n, "0")
	}
	if len(n) < 8 || len(n) > 15 || n[0] == '0' {
		return bad("not a number with a country code")
	}
	if rest, ok := strings.CutPrefix(n, "91"); ok && (len(rest) != 10 || rest[0] < '6') {
		return bad("an Indian mobile number is 10 digits starting with 6, 7, 8 or 9")
	}
	return "+" + n, nil
}

// NormalizeEmail returns an email address in lower case, or [ErrInvalid]:
// a bare address (no name, no brackets) with a domain that has a dot.
func NormalizeEmail(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	a, err := mail.ParseAddress(s)
	_, domain, _ := strings.Cut(s, "@")
	if err != nil || a.Address != s || len(s) > 254 || !strings.Contains(domain, ".") {
		return "", fmt.Errorf("%w: not an email address", ErrInvalid)
	}
	return s, nil
}
