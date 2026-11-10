package notify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// CodeTTL is how long a one-time code message is worth sending.
const CodeTTL = 10 * time.Minute

// SendCode sends a one-time sign-in code to to (a phone number in E.164 or
// an email address) through s, at once: not through the outbox, the routes
// or quiet hours. Asking for the code is the person's consent to the lanes
// s may use, so none needs to be on record; the lanes' cost caps and
// records still apply. text is the whole message, code the code alone
// (for lanes whose templates carry only it). The error is nil when a lane
// took it.
func SendCode(ctx context.Context, s Sender, to, code, text string, now time.Time) ([]Receipt, error) {
	to = strings.TrimSpace(to)
	sum := sha256.Sum256([]byte(to))
	// The person may have no account yet: the recipient is made up for
	// this message, named by a hash so the delivery records do not keep
	// the address.
	r := Recipient{ID: "signin:" + hex.EncodeToString(sum[:8]), Consent: map[string]Consent{}}
	m := Message{
		ID: NewID("code-"), To: r.ID, Kind: KindCode, Severity: Urgent, Body: text, Code: code,
		CreatedAt: now.UTC(), ExpiresAt: now.UTC().Add(CodeTTL),
	}
	switch {
	case strings.Contains(to, "@") && validEmail(to):
		r.Email, m.Title = to, "Your sign-in code"
	case validPhone(to):
		r.Phone = to
	default:
		return nil, fmt.Errorf("%w: %q is not a phone number or an email address", ErrInvalid, to)
	}
	for _, l := range s.Lanes() {
		r.Consent[l] = Consent{OptedIn: true, At: now.UTC(), Source: "signin"}
	}
	m.Recipient = r
	if err := m.check(); err != nil {
		return nil, err
	}
	return s.Send(ctx, m)
}
