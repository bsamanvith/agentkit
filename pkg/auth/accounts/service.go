package accounts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/pkg/clock"
)

// Session lifetimes by default: long, because people sign in on their own
// phone and should rarely have to again. Browsers keep a cookie at most 400
// days.
const (
	DefaultIdleTimeout     = 90 * 24 * time.Hour
	DefaultAbsoluteTimeout = 400 * 24 * time.Hour
)

// MaxNameLength bounds an account's name, in bytes.
const MaxNameLength = 100

// Option configures [New].
type Option func(*Service)

// WithLanes adds sign-in lanes, in the order apps show them. Each must be
// a [RedirectLane], [SecretLane] or [CodeLane] as its Kind says, and have a
// name no other lane has.
func WithLanes(lanes ...Lane) Option {
	return func(s *Service) { s.order = append(s.order, lanes...) }
}

// WithSignUp names the lanes through which a person with no account gets a
// new one at their first sign-in. Through other lanes an unknown identity
// is [ErrNoAccount]. Lanes that know the account (password, sign-in code)
// never create one.
func WithSignUp(lanes ...string) Option {
	return func(s *Service) {
		for _, l := range lanes {
			s.signUp[l] = true
		}
	}
}

// WithAdminEmails makes an account [RoleAdmin] when it signs in with an
// identity whose verified email is one of emails (compared in lower case):
// the first admin of a new deployment.
func WithAdminEmails(emails ...string) Option { return WithRoleEmails(RoleAdmin, emails...) }

// WithRoleEmails gives an account role when it signs in with an identity
// whose verified email is one of emails (compared in lower case), such as
// the farm app's partners. Roles are only added this way, never taken.
func WithRoleEmails(role string, emails ...string) Option {
	return func(s *Service) {
		for _, e := range emails {
			if e = strings.ToLower(strings.TrimSpace(e)); e != "" && !slices.Contains(s.roleEmails[e], role) {
				s.roleEmails[e] = append(s.roleEmails[e], role)
			}
		}
	}
}

// WithSessionTimeouts sets how long a session lasts unused, and in all.
func WithSessionTimeouts(idle, absolute time.Duration) Option {
	return func(s *Service) { s.idle, s.absolute = idle, absolute }
}

// WithClock sets the clock of sessions, codes and limits.
func WithClock(c clock.Clock) Option { return func(s *Service) { s.clock = c } }

// WithLogger sets the logger (default: discard). Sign-ins, failures and
// throttling are logged with the lane, the account id and the client's
// address; never a secret, a code or a session id.
func WithLogger(l *slog.Logger) Option { return func(s *Service) { s.log = l } }

// WithTrustedProxies names reverse proxies whose X-Forwarded-Proto and
// X-Forwarded-For are believed (see pkg/auth/login's option of that name).
func WithTrustedProxies(p ...netip.Prefix) Option {
	return func(s *Service) { s.proxies = append(s.proxies, p...) }
}

// WithCookieName sets the session cookie's name (default "agentkit_account";
// "__Host-" is put in front over TLS).
func WithCookieName(name string) Option { return func(s *Service) { s.cookieName = name } }

// Service is accounts and sign-in: lanes, sessions and codes over a store.
// It is safe for concurrent use.
type Service struct {
	store      Store
	order      []Lane
	lanes      map[string]Lane
	signUp     map[string]bool
	roleEmails map[string][]string
	idle       time.Duration
	absolute   time.Duration
	clock      clock.Clock
	log        *slog.Logger
	proxies    []netip.Prefix
	cookieName string

	// failures counts wrong secrets per client address and per id;
	// sends counts codes sent per destination and per client address.
	failures, failuresByID *limiter
	sends, sendsByAddr     *limiter

	mu         sync.Mutex
	lastExpire time.Time
}

// Limits on guessing and sending.
const (
	// failuresPerAddr wrong secrets from one address in failureWindow hold
	// it back; failuresPerID for one username or destination.
	failuresPerAddr = 30
	failuresPerID   = 10
	failureWindow   = 15 * time.Minute
	// sendsPerDest codes to one destination, and sendsPerAddr from one
	// address, in sendWindow.
	sendsPerDest = 3
	sendsPerAddr = 10
	sendWindow   = 15 * time.Minute
	// expireEvery is how often the store is swept of expired sessions and
	// challenges.
	expireEvery = time.Minute
)

// New returns a Service over store with opts. A lane of the wrong shape or
// a name used twice is an error.
func New(store Store, opts ...Option) (*Service, error) {
	s := &Service{
		store: store, lanes: map[string]Lane{}, signUp: map[string]bool{}, roleEmails: map[string][]string{},
		idle: DefaultIdleTimeout, absolute: DefaultAbsoluteTimeout,
		clock: clock.Real(), log: slog.New(slog.DiscardHandler), cookieName: "agentkit_account",
		failures: newLimiter(failuresPerAddr, failureWindow), failuresByID: newLimiter(failuresPerID, failureWindow),
		sends: newLimiter(sendsPerDest, sendWindow), sendsByAddr: newLimiter(sendsPerAddr, sendWindow),
	}
	for _, o := range opts {
		o(s)
	}
	if store == nil {
		return nil, errors.New("accounts: no store")
	}
	for _, l := range s.order {
		name := l.Name()
		if !validLaneName(name) {
			return nil, fmt.Errorf("accounts: lane name %q: 1 to 32 of a-z, 0-9 and '-'", name)
		}
		if _, dup := s.lanes[name]; dup {
			return nil, fmt.Errorf("accounts: lane %q listed twice", name)
		}
		var ok bool
		switch l.Kind() {
		case KindRedirect:
			_, ok = l.(RedirectLane)
		case KindSecret:
			_, ok = l.(SecretLane)
		case KindCode:
			_, ok = l.(CodeLane)
		}
		if !ok {
			return nil, fmt.Errorf("accounts: lane %q is not a %s lane", name, l.Kind())
		}
		s.lanes[name] = l
	}
	for name := range s.signUp {
		if _, ok := s.lanes[name]; !ok {
			return nil, fmt.Errorf("accounts: sign-up through %q, which is not a lane", name)
		}
	}
	if s.idle <= 0 || s.absolute < s.idle {
		return nil, errors.New("accounts: session timeouts: want 0 < idle <= absolute")
	}
	return s, nil
}

// Store is the service's store.
func (s *Service) Store() Store { return s.store }

// LaneInfo describes a lane to an app's sign-in screen.
type LaneInfo struct {
	Name string `json:"name"`
	Kind Kind   `json:"kind"`
	// SignUp says whether a person with no account may start one here.
	SignUp bool `json:"sign_up"`
}

// Lanes lists the lanes in order.
func (s *Service) Lanes() []LaneInfo {
	out := make([]LaneInfo, 0, len(s.order))
	for _, l := range s.order {
		out = append(out, LaneInfo{Name: l.Name(), Kind: l.Kind(), SignUp: s.signUp[l.Name()]})
	}
	return out
}

// Lane returns the lane of name.
func (s *Service) Lane(name string) (Lane, bool) {
	l, ok := s.lanes[name]
	return l, ok
}

// SignIn returns the account of id, an identity a lane vouched for: the
// account it names (a password or a sign-in code), or the one that has it,
// or, through a sign-up lane, a new account with it. created says whether
// the account is new.
func (s *Service) SignIn(ctx context.Context, id Identity) (acc Account, created bool, err error) {
	now := s.clock.Now()
	if id.Account != "" {
		acc, err = s.store.Account(ctx, id.Account)
		if err != nil {
			return Account{}, false, err
		}
		return s.grant(ctx, acc, id)
	}
	for range 2 {
		have, err := s.store.Identity(ctx, id.Lane, id.Subject)
		if err == nil {
			have.LastUsedAt = now
			if id.Email != "" {
				have.Email, have.EmailVerified = id.Email, id.EmailVerified
			}
			have.Name, have.Phone = orOld(id.Name, have.Name), orOld(id.Phone, have.Phone)
			if err := s.store.UpdateIdentity(ctx, have); err != nil {
				return Account{}, false, err
			}
			acc, err := s.store.Account(ctx, have.Account)
			if err != nil {
				return Account{}, false, err
			}
			return s.grant(ctx, acc, id)
		}
		if !errors.Is(err, ErrNotFound) {
			return Account{}, false, err
		}
		if !s.signUp[id.Lane] {
			return Account{}, false, ErrNoAccount
		}
		acc, err = s.CreateAccount(ctx, Account{Name: nameOf(id), Phone: id.Phone})
		if err != nil {
			return Account{}, false, err
		}
		id.Account, id.CreatedAt, id.LastUsedAt = acc.ID, now, now
		err = s.store.AddIdentity(ctx, id)
		if err == nil {
			s.log.LogAttrs(ctx, slog.LevelInfo, "accounts: signed up", slog.String("lane", id.Lane), slog.String("account", acc.ID))
			acc, _, err = s.grant(ctx, acc, id)
			return acc, true, err
		}
		// Someone signed up with the same identity at the same moment:
		// theirs stands, and this sign-in uses it.
		_ = s.store.DeleteAccount(ctx, acc.ID)
		if !errors.Is(err, ErrExists) {
			return Account{}, false, err
		}
	}
	return Account{}, false, errors.New("accounts: the identity changed twice while signing in")
}

func orOld(v, old string) string {
	if v == "" {
		return old
	}
	return v
}

// nameOf is a new account's name from what the lane said.
func nameOf(id Identity) string {
	if n := strings.TrimSpace(id.Name); n != "" {
		return truncate(n, MaxNameLength)
	}
	if id.Email != "" {
		local, _, _ := strings.Cut(id.Email, "@")
		return truncate(local, MaxNameLength)
	}
	return ""
}

func truncate(s string, n int) string {
	for len(s) > n {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return s
}

// grant adds the roles configured for id's verified email to acc.
func (s *Service) grant(ctx context.Context, acc Account, id Identity) (Account, bool, error) {
	if !id.EmailVerified {
		return acc, false, nil
	}
	changed := false
	for _, r := range s.roleEmails[strings.ToLower(id.Email)] {
		if !slices.Contains(acc.Roles, r) {
			acc.Roles, changed = append(acc.Roles, r), true
			s.log.LogAttrs(ctx, slog.LevelInfo, "accounts: role by configured email", slog.String("account", acc.ID), slog.String("role", r))
		}
	}
	if changed {
		acc.UpdatedAt = s.clock.Now()
		if err := s.store.UpdateAccount(ctx, acc); err != nil {
			return Account{}, false, err
		}
	}
	return acc, false, nil
}

// CreateAccount saves a new account with a new id. Its name is trimmed and
// at most [MaxNameLength] bytes; its phone, if any, is normalized
// ([NormalizePhone] with no default country).
func (s *Service) CreateAccount(ctx context.Context, a Account) (Account, error) {
	a.Name = strings.TrimSpace(a.Name)
	if len(a.Name) > MaxNameLength || !utf8.ValidString(a.Name) {
		return Account{}, fmt.Errorf("%w: a name is at most %d bytes", ErrInvalid, MaxNameLength)
	}
	if a.Phone != "" {
		p, err := NormalizePhone(a.Phone, "")
		if err != nil {
			return Account{}, err
		}
		a.Phone = p
	}
	now := s.clock.Now()
	a.ID, a.CreatedAt, a.UpdatedAt = newID("acc_"), now, now
	if err := s.store.CreateAccount(ctx, a); err != nil {
		return Account{}, err
	}
	return a, nil
}

// Rename sets an account's name.
func (s *Service) Rename(ctx context.Context, account, name string) (Account, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > MaxNameLength || !utf8.ValidString(name) {
		return Account{}, fmt.Errorf("%w: a name is 1 to %d bytes", ErrInvalid, MaxNameLength)
	}
	a, err := s.store.Account(ctx, account)
	if err != nil {
		return Account{}, err
	}
	a.Name, a.UpdatedAt = name, s.clock.Now()
	return a, s.store.UpdateAccount(ctx, a)
}

// SetRoles sets an account's app-wide roles. Who may is the caller's
// decision (an admin).
func (s *Service) SetRoles(ctx context.Context, account string, roles []string) (Account, error) {
	a, err := s.store.Account(ctx, account)
	if err != nil {
		return Account{}, err
	}
	var clean []string
	for _, r := range roles {
		if !validLaneName(r) {
			return Account{}, fmt.Errorf("%w: role %q", ErrInvalid, r)
		}
		if !slices.Contains(clean, r) {
			clean = append(clean, r)
		}
	}
	a.Roles, a.UpdatedAt = clean, s.clock.Now()
	return a, s.store.UpdateAccount(ctx, a)
}

// Link adds id, which a lane vouched for, to account. An identity the
// account has already is updated; one another account has is
// [ErrLinkedElsewhere].
func (s *Service) Link(ctx context.Context, account string, id Identity) (Identity, error) {
	now := s.clock.Now()
	have, err := s.store.Identity(ctx, id.Lane, id.Subject)
	switch {
	case err == nil && have.Account != account:
		return Identity{}, ErrLinkedElsewhere
	case err == nil:
		id.Account, id.CreatedAt, id.LastUsedAt = account, have.CreatedAt, now
		return id, s.store.UpdateIdentity(ctx, id)
	case !errors.Is(err, ErrNotFound):
		return Identity{}, err
	}
	if _, err := s.store.Account(ctx, account); err != nil {
		return Identity{}, err
	}
	id.Account, id.CreatedAt, id.LastUsedAt = account, now, now
	if err := s.store.AddIdentity(ctx, id); err != nil {
		if errors.Is(err, ErrExists) {
			return Identity{}, ErrLinkedElsewhere
		}
		return Identity{}, err
	}
	s.log.LogAttrs(ctx, slog.LevelInfo, "accounts: linked", slog.String("lane", id.Lane), slog.String("account", account))
	return id, nil
}

// Unlink removes an identity of account. The last one stays
// ([ErrLastIdentity]): an account always has a way in.
func (s *Service) Unlink(ctx context.Context, account, lane, subject string) error {
	ids, err := s.store.Identities(ctx, account)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(ids, func(id Identity) bool { return id.Lane == lane && id.Subject == subject })
	switch {
	case i < 0:
		return ErrNotFound
	case len(ids) == 1:
		return ErrLastIdentity
	}
	return s.store.RemoveIdentity(ctx, lane, subject)
}

// Identities returns an account's identities, without secrets.
func (s *Service) Identities(ctx context.Context, account string) ([]Identity, error) {
	ids, err := s.store.Identities(ctx, account)
	for i := range ids {
		ids[i].Secret = ""
	}
	return ids, err
}

// Code purposes the package itself uses.
const (
	// PurposeSignIn is a sign-in code: Data["account"] signs in.
	PurposeSignIn = "signin"
	purposeFlow   = "flow"
	purposeOTP    = "otp"
)

// IssueCode makes a code for purpose that gives data once, until ttl
// passes, and returns it: twelve letters and digits that are easy to read
// out over the phone, in groups of four ("K7QM-2XDP-9RTA", about 60 bits).
// The store keeps only its hash.
func (s *Service) IssueCode(ctx context.Context, purpose string, data map[string]string, ttl time.Duration) (string, error) {
	code := newCode()
	c := Challenge{Purpose: purpose, Data: data, Expires: s.clock.Now().Add(ttl)}
	if err := s.store.PutChallenge(ctx, codeKey(code), c); err != nil {
		return "", err
	}
	return format(code), nil
}

// RedeemCode returns the data of a code [Service.IssueCode] made for
// purpose, and uses it up. A wrong, used or expired code, or one made for
// another purpose, is [ErrBadSecret]; of two redeems of one code at once,
// one succeeds.
func (s *Service) RedeemCode(ctx context.Context, purpose, code string) (map[string]string, error) {
	return redeem(ctx, s.store, s.clock, purpose, code)
}

// IssueSignInCode makes a sign-in code for account, for the [InviteLane],
// that works once within ttl.
func (s *Service) IssueSignInCode(ctx context.Context, account string, ttl time.Duration) (string, error) {
	if _, err := s.store.Account(ctx, account); err != nil {
		return "", err
	}
	return s.IssueCode(ctx, PurposeSignIn, map[string]string{"account": account}, ttl)
}

func redeem(ctx context.Context, store Store, c clock.Clock, purpose, code string) (map[string]string, error) {
	norm, ok := parseCode(code)
	if !ok {
		return nil, ErrBadSecret
	}
	k := codeKey(norm)
	ch, err := store.Challenge(ctx, k)
	switch {
	case errors.Is(err, ErrNotFound):
		return nil, ErrBadSecret
	case err != nil:
		return nil, err
	case ch.Purpose != purpose:
		return nil, ErrBadSecret
	}
	if err := store.DeleteChallenge(ctx, k); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrBadSecret
		}
		return nil, err
	}
	if !c.Now().Before(ch.Expires) {
		return nil, ErrBadSecret
	}
	return ch.Data, nil
}

// codeAlphabet has no 0/O, 1/I/L, so a code read out loud or copied by
// hand comes through.
const codeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

const codeLength = 12

func newCode() string {
	b := make([]byte, codeLength)
	for i := range b {
		b[i] = codeAlphabet[randIntn(len(codeAlphabet))]
	}
	return string(b)
}

// randIntn is a uniform number below n from crypto/rand.
func randIntn(n int) int {
	limit := 256 - 256%n
	var b [1]byte
	for {
		_, _ = rand.Read(b[:]) // never fails (crypto/rand)
		if int(b[0]) < limit {
			return int(b[0]) % n
		}
	}
}

func format(code string) string {
	return code[:4] + "-" + code[4:8] + "-" + code[8:]
}

// parseCode reads a code as people type it: any case, with or without
// spaces and dashes.
func parseCode(s string) (string, bool) {
	if len(s) > 4*codeLength {
		return "", false
	}
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if r == ' ' || r == '-' || r == '\t' {
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	if len(out) != codeLength {
		return "", false
	}
	for i := range len(out) {
		if !strings.ContainsRune(codeAlphabet, rune(out[i])) {
			return "", false
		}
	}
	return out, true
}

func codeKey(code string) string { return "code:" + sha(code) }

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// newID is prefix and 128 random bits in lower-case base32.
func newID(prefix string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // never fails (crypto/rand)
	return prefix + strings.ToLower(idEncoding.EncodeToString(b))
}

// expire sweeps expired sessions and challenges, at most once a minute.
func (s *Service) expire(ctx context.Context) {
	now := s.clock.Now()
	s.mu.Lock()
	due := now.Sub(s.lastExpire) >= expireEvery
	if due {
		s.lastExpire = now
	}
	s.mu.Unlock()
	if !due {
		return
	}
	if err := s.store.ExpireSessions(ctx, now.Add(-s.idle), now.Add(-s.absolute)); err != nil {
		s.log.LogAttrs(ctx, slog.LevelWarn, "accounts: expiring sessions", slog.String("error", err.Error()))
	}
	if err := s.store.ExpireChallenges(ctx, now); err != nil {
		s.log.LogAttrs(ctx, slog.LevelWarn, "accounts: expiring codes", slog.String("error", err.Error()))
	}
}
