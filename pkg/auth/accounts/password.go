package accounts

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/auth/login"
	"github.com/bsamanvith/agentkit/pkg/clock"
)

// PasswordLaneName is the password lane's name and its identities' Lane.
const PasswordLaneName = "password"

// maxHashing is how many password checks run at once; each takes 19 MiB.
const maxHashing = 4

// PasswordLane signs in with a username and a password the person set while
// signed in ([Service.SetPassword]): the farm app's lane for a farmer
// without a Google account, kept by pkg/auth/login's argon2id hashes. An
// unknown username is checked against a dummy hash, so it takes as long as
// a known one.
type PasswordLane struct {
	store   Store
	dummy   string
	hashing chan struct{}
}

// NewPasswordLane returns the password lane over store.
func NewPasswordLane(store Store) (*PasswordLane, error) {
	b := make([]byte, 24)
	_, _ = rand.Read(b) // never fails (crypto/rand)
	dummy, err := login.HashPassword(fmt.Sprintf("%x", b))
	if err != nil {
		return nil, err
	}
	return &PasswordLane{store: store, dummy: dummy, hashing: make(chan struct{}, maxHashing)}, nil
}

// Name implements [Lane].
func (*PasswordLane) Name() string { return PasswordLaneName }

// Kind implements [Lane].
func (*PasswordLane) Kind() Kind { return KindSecret }

// Check implements [SecretLane].
func (p *PasswordLane) Check(ctx context.Context, username, password string) (Identity, error) {
	u, uerr := NormalizeUsername(username)
	id, err := Identity{}, ErrNotFound
	if uerr == nil {
		id, err = p.store.Identity(ctx, PasswordLaneName, u)
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Identity{}, err
	}
	h := p.dummy
	if err == nil && id.Secret != "" {
		h = id.Secret
	}
	select {
	case p.hashing <- struct{}{}:
	case <-ctx.Done():
		return Identity{}, ctx.Err()
	}
	ok, herr := login.CheckPassword(h, password)
	<-p.hashing
	if herr != nil {
		return Identity{}, fmt.Errorf("accounts: a stored password hash: %w", herr)
	}
	if !ok || err != nil || id.Secret == "" || len(password) > login.MaxPasswordLength {
		return Identity{}, ErrBadSecret
	}
	return id, nil
}

// SetPassword gives account a username and password, or a new password
// for the username it has: an identity in the password lane. A username
// another account has is [ErrLinkedElsewhere]; an account has one username
// (a new one replaces the old). The service must have a [PasswordLane].
func (s *Service) SetPassword(ctx context.Context, account, username, password string) (Identity, error) {
	if _, ok := s.lanes[PasswordLaneName].(*PasswordLane); !ok {
		return Identity{}, ErrUnknownLane
	}
	u, err := NormalizeUsername(username)
	if err != nil {
		return Identity{}, err
	}
	h, err := login.HashPassword(password)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	ids, err := s.store.Identities(ctx, account)
	if err != nil {
		return Identity{}, err
	}
	id, err := s.Link(ctx, account, Identity{Lane: PasswordLaneName, Subject: u, Secret: h})
	if err != nil {
		return Identity{}, err
	}
	for _, old := range ids {
		if old.Lane == PasswordLaneName && old.Subject != u {
			if err := s.store.RemoveIdentity(ctx, PasswordLaneName, old.Subject); err != nil && !errors.Is(err, ErrNotFound) {
				return Identity{}, err
			}
		}
	}
	id.Secret = ""
	return id, nil
}

// NormalizeUsername returns a username in lower case, or [ErrInvalid]: 3 to
// 64 letters, digits, '.', '_', '-', '@' or '+' (a phone number or an
// email address is a fine username).
func NormalizeUsername(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) < 3 || len(s) > 64 {
		return "", fmt.Errorf("%w: a username is 3 to 64 characters", ErrInvalid)
	}
	for i := range len(s) {
		c := s[i]
		ok := c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.IndexByte("._-@+", c) >= 0
		if !ok {
			return "", fmt.Errorf("%w: a username is letters, digits, '.', '_', '-', '@' or '+'", ErrInvalid)
		}
	}
	return s, nil
}

// InviteLaneName is the sign-in code lane's name.
const InviteLaneName = "invite"

// InviteLane signs in with a sign-in code: one that someone allowed to
// (the farm app's partner field worker) made for an account with
// [Service.IssueSignInCode], at an assisted sign-up or to get a person back
// in who lost every other way. A code works once.
type InviteLane struct {
	store Store
	clock clock.Clock
}

// NewInviteLane returns the sign-in code lane over store.
func NewInviteLane(store Store, c clock.Clock) *InviteLane {
	if c == nil {
		c = clock.Real()
	}
	return &InviteLane{store: store, clock: c}
}

// Name implements [Lane].
func (*InviteLane) Name() string { return InviteLaneName }

// Kind implements [Lane].
func (*InviteLane) Kind() Kind { return KindSecret }

// Check implements [SecretLane]: id is ignored, secret is the code.
func (l *InviteLane) Check(ctx context.Context, _, code string) (Identity, error) {
	data, err := redeem(ctx, l.store, l.clock, PurposeSignIn, code)
	if err != nil {
		return Identity{}, err
	}
	if data["account"] == "" {
		return Identity{}, ErrBadSecret
	}
	return Identity{Lane: InviteLaneName, Account: data["account"]}, nil
}
