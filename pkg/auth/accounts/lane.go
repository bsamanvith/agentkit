package accounts

import (
	"context"
)

// Kind is what a person does to sign in through a lane.
type Kind string

// The kinds of lane.
const (
	// KindRedirect: the browser goes to a provider and back ([RedirectLane]).
	KindRedirect Kind = "redirect"
	// KindSecret: the person types an id and a secret, or only a secret
	// ([SecretLane]).
	KindSecret Kind = "secret"
	// KindCode: a one-time code is sent to the person and typed back
	// ([CodeLane]).
	KindCode Kind = "code"
)

// Lane is one way to sign in. Its name is the Lane of the identities it
// gives and part of the lane's URLs: 1 to 32 lower-case letters, digits
// and '-'. Each lane is also one of [RedirectLane], [SecretLane] or
// [CodeLane], as its Kind says.
type Lane interface {
	Name() string
	Kind() Kind
}

// Flow is one redirect sign-in in progress: what the service made for it
// and keeps until the browser comes back.
type Flow struct {
	// State is the value the provider sends back; the service checks it.
	State string
	// Verifier is the PKCE code verifier (RFC 7636); the lane sends its S256
	// challenge.
	Verifier string
	// Nonce is the OpenID Connect nonce the ID token must carry.
	Nonce string
}

// RedirectLane signs in at a provider, such as Google.
type RedirectLane interface {
	Lane
	// AuthCodeURL is where to send the browser to start f.
	AuthCodeURL(f Flow) string
	// Finish exchanges the code the provider sent back for the person's
	// identity. The service has checked the state.
	Finish(ctx context.Context, f Flow, code string) (Identity, error)
}

// SecretLane checks something the person types: an id (a username; empty
// for a lane without one) and a secret. A lane that knows the account
// already (a password, a sign-in code) returns an identity with Account
// set.
type SecretLane interface {
	Lane
	// Check returns the identity of id and secret, or an error matching
	// [ErrBadSecret] for a wrong one, alike whether or not id exists.
	Check(ctx context.Context, id, secret string) (Identity, error)
}

// CodeLane sends a one-time code to a destination, a phone number or an
// email address, and checks it when it is typed back.
type CodeLane interface {
	Lane
	// Normalize returns the destination's canonical form (E.164, lower
	// case), or an error matching [ErrInvalid].
	Normalize(to string) (string, error)
	// Send makes a new code for the normalized destination and sends it,
	// replacing any code sent there before.
	Send(ctx context.Context, to string) error
	// Verify checks a code sent to the normalized destination and returns
	// its identity (Subject the destination). A wrong, used or expired code
	// is [ErrBadSecret].
	Verify(ctx context.Context, to, code string) (Identity, error)
}

// Sender delivers a one-time code's message to a phone number or an email
// address. Message lanes (SMS, WhatsApp, email) implement it; tests use
// accountstest.Outbox.
type Sender interface {
	Send(ctx context.Context, to, text string) error
}

// CodeSender is a [Sender] that also takes the code alone, for channels
// whose templates carry only it (WhatsApp's authentication templates, an
// SMS OTP template). A [OneTimeCodeLane] uses SendCode when its sender has
// it.
type CodeSender interface {
	Sender
	SendCode(ctx context.Context, to, code, text string) error
}

// SenderFunc is a function as a [Sender].
type SenderFunc func(ctx context.Context, to, text string) error

// Send implements [Sender].
func (f SenderFunc) Send(ctx context.Context, to, text string) error { return f(ctx, to, text) }

func validLaneName(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}
