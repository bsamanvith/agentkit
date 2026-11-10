package accounts

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Errors callers branch on, checked with errors.Is.
var (
	// ErrNotFound is a store's answer for a missing account, identity,
	// session or challenge.
	ErrNotFound = errors.New("accounts: not found")
	// ErrExists is a store's answer for a new account or identity whose key
	// is taken.
	ErrExists = errors.New("accounts: already exists")
	// ErrNoAccount is a sign-in through a lane that may not create
	// accounts, by an identity no account has.
	ErrNoAccount = errors.New("accounts: no account has this identity")
	// ErrLinkedElsewhere is a link of an identity another account has.
	ErrLinkedElsewhere = errors.New("accounts: this identity belongs to another account")
	// ErrLastIdentity is the removal of an account's only identity.
	ErrLastIdentity = errors.New("accounts: an account keeps at least one way to sign in")
	// ErrBadSecret is a wrong password, an unknown username, or a wrong,
	// used or expired code: they are not told apart.
	ErrBadSecret = errors.New("accounts: wrong or expired")
	// ErrUnknownLane is a lane the service does not have.
	ErrUnknownLane = errors.New("accounts: no such sign-in lane")
	// ErrInvalid is a malformed request: a bad phone number, username or
	// name.
	ErrInvalid = errors.New("accounts: invalid")
	// ErrCSRF is an unsafe request that does not show it came from the
	// app: no same-origin Origin header, or no matching CSRF token.
	ErrCSRF = errors.New("accounts: cross-site request refused")
	// ErrNoSession is a request without a valid session.
	ErrNoSession = errors.New("accounts: not signed in")
)

// ThrottledError is an attempt refused, without checking it, because of
// earlier failures or sends for the same address or destination.
type ThrottledError struct{ RetryAfter time.Duration }

func (e *ThrottledError) Error() string {
	return fmt.Sprintf("accounts: too many attempts: try again in %s", e.RetryAfter.Round(time.Second))
}

// RoleAdmin is the account role that manages other accounts' roles. Apps
// add their own roles (the farm app's "partner").
const RoleAdmin = "admin"

// Account is a person.
type Account struct {
	ID string `json:"id"`
	// Name is what the app calls them; they may change it.
	Name string `json:"name"`
	// Phone is a contact number someone gave (an assisted sign-up), in
	// E.164; not proof of anything until a phone identity is linked.
	Phone string `json:"phone,omitempty"`
	// Roles are app-wide roles such as [RoleAdmin].
	Roles     []string  `json:"roles,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Identity is one way an account signs in: Subject in Lane.
type Identity struct {
	// Lane is the lane's name ("google", "password", "phone").
	Lane string `json:"lane"`
	// Subject identifies the person within the lane: a provider's "sub",
	// a username, a phone number in E.164, an email address in lower case.
	Subject string `json:"subject"`
	Account string `json:"account"`
	// Email, EmailVerified, Phone and Name are what the lane said about the
	// person; shown to them, never used to find or join accounts.
	Email         string `json:"email,omitempty"`
	EmailVerified bool   `json:"email_verified,omitempty"`
	Phone         string `json:"phone,omitempty"`
	Name          string `json:"name,omitempty"`
	// Secret is the password lane's argon2id hash (PHC form); empty for
	// every other lane. It never leaves the server.
	Secret     string    `json:"-"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at"`
}

// Session is what a store keeps of a session, under the SHA-256 of its id.
type Session struct {
	Account string
	// Lane is the lane it signed in with.
	Lane string
	// CSRF is the token its unsafe requests send in [CSRFHeader].
	CSRF              string
	Created, LastSeen time.Time
}

// Challenge is a secret waiting to be used: a one-time code sent to a
// destination, a sign-in or join code, or a redirect flow's state. The
// store keeps it under a key derived from the secret (or, for a one-time
// code, from its destination, with Hash the code's).
type Challenge struct {
	// Purpose says what it is for; a code made for one purpose is not
	// accepted for another.
	Purpose string `json:"purpose"`
	// Hash is the SHA-256 of a one-time code, in hex, when the key is not
	// derived from the code.
	Hash string `json:"hash,omitempty"`
	// Data is what using it gives: an account id, a farm and a role.
	Data     map[string]string `json:"data,omitempty"`
	Expires  time.Time         `json:"expires"`
	Attempts int               `json:"attempts"`
}

// Store keeps accounts, identities, sessions and challenges.
// Implementations are safe for concurrent use and pass
// accountstest.RunStore.
type Store interface {
	// CreateAccount saves a new account; ErrExists if its ID is taken.
	CreateAccount(ctx context.Context, a Account) error
	// Account returns an account; ErrNotFound if there is none.
	Account(ctx context.Context, id string) (Account, error)
	// UpdateAccount replaces an account; ErrNotFound if there is none.
	UpdateAccount(ctx context.Context, a Account) error
	// DeleteAccount removes an account with its identities and sessions;
	// ErrNotFound if there is none.
	DeleteAccount(ctx context.Context, id string) error

	// AddIdentity saves a new identity; ErrExists if its lane and subject
	// are taken (by any account).
	AddIdentity(ctx context.Context, id Identity) error
	// Identity returns the identity of subject in lane; ErrNotFound if
	// there is none.
	Identity(ctx context.Context, lane, subject string) (Identity, error)
	// Identities returns an account's identities, oldest first.
	Identities(ctx context.Context, account string) ([]Identity, error)
	// UpdateIdentity replaces an identity (its secret, its last use, what
	// the lane said); ErrNotFound if there is none. Its account does not
	// change.
	UpdateIdentity(ctx context.Context, id Identity) error
	// RemoveIdentity removes an identity; ErrNotFound if there is none.
	RemoveIdentity(ctx context.Context, lane, subject string) error

	// CreateSession stores a session under key, which is new.
	CreateSession(ctx context.Context, key string, s Session) error
	// Session returns the session under key; ErrNotFound if there is none.
	Session(ctx context.Context, key string) (Session, error)
	// TouchSession sets a session's LastSeen; ErrNotFound if there is none.
	TouchSession(ctx context.Context, key string, at time.Time) error
	// DeleteSession removes a session; a missing one is not an error.
	DeleteSession(ctx context.Context, key string) error
	// DeleteSessions removes every session of an account.
	DeleteSessions(ctx context.Context, account string) error
	// ExpireSessions removes every session last seen before idle or
	// created before created.
	ExpireSessions(ctx context.Context, idle, created time.Time) error

	// PutChallenge stores c under key, replacing any challenge there.
	PutChallenge(ctx context.Context, key string, c Challenge) error
	// Challenge returns the challenge under key; ErrNotFound if there is
	// none.
	Challenge(ctx context.Context, key string) (Challenge, error)
	// AttemptChallenge adds one to the challenge's Attempts and returns it
	// as it is after; ErrNotFound if there is none. Concurrent calls each
	// see a different count.
	AttemptChallenge(ctx context.Context, key string) (Challenge, error)
	// DeleteChallenge removes the challenge under key; ErrNotFound if there
	// is none, so of two callers using the same challenge only one
	// succeeds.
	DeleteChallenge(ctx context.Context, key string) error
	// ExpireChallenges removes every challenge that expired before now.
	ExpireChallenges(ctx context.Context, now time.Time) error
}
