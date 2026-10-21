package login

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/clock"
)

// KindSession is the auth.Credential Kind of a principal [Login]
// authenticated: its ID is the username.
const KindSession = "session"

// Cookie names: the __Host- prefix needs Secure, so a server reached over
// plain HTTP (a loopback dev server) uses the other.
const (
	SecureCookieName = "__Host-agentkit_session"
	CookieName       = "agentkit_session"
)

// CSRFHeader carries a session's CSRF token on unsafe requests.
const CSRFHeader = "X-Agentkit-CSRF"

// Session lifetimes.
const (
	// IdleTimeout ends a session not used for this long.
	IdleTimeout = 12 * time.Hour
	// AbsoluteTimeout ends a session this long after sign-in, used or not.
	AbsoluteTimeout = 7 * 24 * time.Hour
	// touchEvery is how stale LastSeen may get before a request updates it.
	touchEvery = time.Minute
	// expireEvery is how often a sign-in sweeps expired sessions.
	expireEvery = time.Minute
)

// idBytes is the length of a session id and of a CSRF token, from
// crypto/rand: 256 bits.
const idBytes = 32

// maxHashing is how many password checks run at once; others wait. Each
// takes the hash's memory (19 MiB by default).
const maxHashing = 4

// Errors of [Login.SignIn] and [Login.CheckCSRF], checked with errors.Is.
var (
	// ErrBadLogin is a sign-in with a wrong password or an unknown
	// username: the two are not told apart.
	ErrBadLogin = errors.New("wrong username or password")
	// ErrCSRF is an unsafe request authenticated by a session that does not
	// show it came from the console: no same-origin Origin header, or no
	// matching CSRF token. Also a sign-in from another origin.
	ErrCSRF = errors.New("cross-site request refused")
)

// Option configures [New].
type Option func(*Login)

// WithStore sets where sessions are kept (default: [NewMemoryStore](0)).
func WithStore(s Store) Option { return func(l *Login) { l.store = s } }

// WithClock sets the clock of session lifetimes and throttling.
func WithClock(c clock.Clock) Option { return func(l *Login) { l.clock = c } }

// WithHasher replaces argon2.IDKey, for tests that count the work.
func WithHasher(h Hasher) Option { return func(l *Login) { l.hasher = h } }

// WithLogger sets the logger (default: discard). Failed and throttled
// sign-ins are logged at info and warn, with the username if it is a known
// one and the client's address; never a password or a session id.
func WithLogger(lg *slog.Logger) Option { return func(l *Login) { l.log = lg } }

// WithTrustedProxies names the addresses of reverse proxies whose
// X-Forwarded-Proto and X-Forwarded-For are believed: a request from one
// with "X-Forwarded-Proto: https" gets a Secure cookie, and its client's
// address is the last one in X-Forwarded-For that is not a trusted proxy.
func WithTrustedProxies(p ...netip.Prefix) Option {
	return func(l *Login) { l.proxies = append(l.proxies, p...) }
}

// Login is password sign-in for local users, and an auth.Authenticator of
// the session cookies it hands out. It is safe for concurrent use.
type Login struct {
	users   map[string]*user
	dummy   hash // checked for an unknown user, as costly as the real ones
	store   Store
	clock   clock.Clock
	hasher  Hasher
	log     *slog.Logger
	proxies []netip.Prefix
	hashing chan struct{}
	throttle

	mu         sync.Mutex
	lastExpire time.Time
}

// New returns a [Login] of users. Every user is checked: a bad username,
// hash, tenant, role or toolset pattern is a [*ConfigError], as is a
// username listed twice.
func New(users []User, opts ...Option) (*Login, error) {
	l := &Login{users: map[string]*user{}, hasher: argon2.IDKey, clock: clock.Real(), log: slog.New(slog.DiscardHandler), hashing: make(chan struct{}, maxHashing)}
	for _, o := range opts {
		o(l)
	}
	if l.store == nil {
		l.store = NewMemoryStore(0)
	}
	for _, u := range users {
		if _, dup := l.users[u.Username]; dup {
			return nil, &ConfigError{Username: u.Username, Err: errors.New("listed twice")}
		}
		c, err := checkUser(u)
		if err != nil {
			return nil, &ConfigError{Username: u.Username, Err: err}
		}
		l.users[u.Username] = c
	}
	// The dummy costs what the costliest real hash does, so an unknown user
	// takes no less time than any known one (HashPassword gives every user
	// the same parameters).
	l.dummy = hash{params: DefaultParams, key: make([]byte, keyLength), salt: make([]byte, saltLength)}
	first := true
	for _, u := range l.users {
		if first || u.hash.cost() > l.dummy.cost() {
			l.dummy.params, l.dummy.key, first = u.hash.params, make([]byte, len(u.hash.key)), false
		}
	}
	if _, err := rand.Read(l.dummy.salt); err != nil {
		return nil, err
	}
	if _, err := rand.Read(l.dummy.key); err != nil {
		return nil, err
	}
	return l, nil
}

// Users is the number of users.
func (l *Login) Users() int { return len(l.users) }

// Format prints the number of users, whatever the verb: never a hash or a
// session.
func (l *Login) Format(f fmt.State, _ rune) {
	_, _ = fmt.Fprintf(f, "login.Login{%d users}", len(l.users))
}

// SignIn checks username and password and, if they are right, starts a
// session and returns its cookie, to set on the response. The request is
// only read: for its client address, its scheme, and its Origin, which if
// present must be this server (else [ErrCSRF]).
//
// A wrong password and an unknown username both return [ErrBadLogin] after
// the same work. After too many failures for the username or the address it
// returns a [*ThrottledError] without checking the password. Other errors
// are internal.
func (l *Login) SignIn(r *http.Request, username, password string) (*http.Cookie, error) {
	if o := r.Header.Values("Origin"); len(o) > 0 && !l.sameOrigin(r, o) {
		return nil, fmt.Errorf("%w: Origin is not this server", ErrCSRF)
	}
	now := l.clock.Now()
	addr := l.clientAddr(r)
	userKey, addrKey := "user:"+username, "addr:"+throttleAddr(addr)
	if d := l.attempt(now, limit{userKey, userThreshold}, limit{addrKey, addrThreshold}); d > 0 {
		l.log.LogAttrs(r.Context(), slog.LevelWarn, "login: throttled", l.who(username), slog.String("addr", addr), slog.Duration("retry_after", d))
		return nil, &ThrottledError{RetryAfter: d}
	}

	u, known := l.users[username]
	h := l.dummy
	if known {
		h = u.hash
	}
	ok, err := l.check(r.Context(), h, password)
	if err != nil {
		l.finish(now, false, userKey, addrKey)
		return nil, err
	}
	if !ok || !known || len(password) > MaxPasswordLength {
		l.finish(now, true, userKey, addrKey)
		l.log.LogAttrs(r.Context(), slog.LevelInfo, "login: failed", l.who(username), slog.String("addr", addr))
		return nil, ErrBadLogin
	}
	l.finish(now, false, userKey, addrKey)
	l.forget(userKey)

	// A session the browser already had (another user's, or one planted in
	// it) ends here: the new one replaces it, so the old must not live on.
	if old, err := l.sessionID(r); err == nil {
		if err := l.store.Delete(r.Context(), key(old)); err != nil {
			return nil, fmt.Errorf("login: ending the previous session: %w", err)
		}
	}
	id, csrf := random(), random()
	s := Session{Username: u.name, CSRF: csrf, Created: now, LastSeen: now}
	l.expire(r.Context(), now)
	if err := l.store.Create(r.Context(), key(id), s); err != nil {
		return nil, fmt.Errorf("login: storing the session: %w", err)
	}
	l.log.LogAttrs(r.Context(), slog.LevelInfo, "login: signed in", slog.String("user", u.name), slog.String("tenant", u.tenant.ID()), slog.String("addr", addr))
	return l.cookie(r, id, int(AbsoluteTimeout/time.Second)), nil
}

// check reports whether password matches h, holding one of the hashing
// slots while it hashes.
func (l *Login) check(ctx context.Context, h hash, password string) (bool, error) {
	select {
	case l.hashing <- struct{}{}:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	defer func() { <-l.hashing }()
	if len(password) > MaxPasswordLength {
		password = password[:MaxPasswordLength]
	}
	return h.matches(l.hasher, password), nil
}

// who is the log attribute for a sign-in's username: the name when it is a
// user's, since a name that is not might be a password typed in the wrong
// field.
func (l *Login) who(username string) slog.Attr {
	if _, ok := l.users[username]; ok {
		return slog.String("user", username)
	}
	return slog.String("user", "(unknown)")
}

// expire sweeps expired sessions out of the store, at most once a minute.
func (l *Login) expire(ctx context.Context, now time.Time) {
	l.mu.Lock()
	due := now.Sub(l.lastExpire) >= expireEvery
	if due {
		l.lastExpire = now
	}
	l.mu.Unlock()
	if due {
		if err := l.store.Expire(ctx, now.Add(-IdleTimeout), now.Add(-AbsoluteTimeout)); err != nil {
			l.log.LogAttrs(ctx, slog.LevelWarn, "login: expiring sessions", slog.String("error", err.Error()))
		}
	}
}

// SignOut ends the request's session, if it has one, and returns the cookie
// that clears it in the browser.
func (l *Login) SignOut(r *http.Request) (*http.Cookie, error) {
	if id, err := l.sessionID(r); err == nil {
		if err := l.store.Delete(r.Context(), key(id)); err != nil {
			return nil, fmt.Errorf("login: deleting the session: %w", err)
		}
	}
	return l.cookie(r, "", -1), nil
}

// Info is what /v1/auth/me says about a session.
type Info struct {
	Username string
	Roles    []string
	// CSRF is the token the session's unsafe requests send in [CSRFHeader].
	CSRF string
}

// Session returns the request's session. Its errors are those of
// [Login.Authenticate].
func (l *Login) Session(r *http.Request) (Info, error) {
	s, u, err := l.session(r)
	if err != nil {
		return Info{}, err
	}
	return Info{Username: u.name, Roles: append([]string(nil), u.roles...), CSRF: s.CSRF}, nil
}

// Authenticate implements auth.Authenticator: the principal of the request's
// session cookie. No cookie is auth.ErrNoCredentials; a malformed, unknown
// or expired one auth.ErrInvalidCredentials.
func (l *Login) Authenticate(r *http.Request) (auth.Principal, error) {
	_, u, err := l.session(r)
	if err != nil {
		return auth.Principal{}, err
	}
	return auth.Principal{
		Tenant: u.tenant, User: u.name,
		Scopes: append([]string(nil), u.scopes...), Toolsets: append([]string(nil), u.toolsets...),
		Via: auth.Credential{Kind: KindSession, ID: u.name},
	}, nil
}

func (l *Login) session(r *http.Request) (Session, *user, error) {
	id, err := l.sessionID(r)
	if err != nil {
		return Session{}, nil, err
	}
	ctx, k, now := r.Context(), key(id), l.clock.Now()
	s, err := l.store.Get(ctx, k)
	switch {
	case errors.Is(err, ErrNoSession):
		return Session{}, nil, fmt.Errorf("%w: no such session", auth.ErrInvalidCredentials)
	case err != nil:
		return Session{}, nil, fmt.Errorf("login: reading the session: %w", err)
	}
	if now.Sub(s.LastSeen) >= IdleTimeout || now.Sub(s.Created) >= AbsoluteTimeout {
		_ = l.store.Delete(ctx, k)
		return Session{}, nil, fmt.Errorf("%w: the session expired", auth.ErrInvalidCredentials)
	}
	u, ok := l.users[s.Username]
	if !ok {
		return Session{}, nil, fmt.Errorf("%w: the session's user is gone", auth.ErrInvalidCredentials)
	}
	if now.Sub(s.LastSeen) >= touchEvery {
		if err := l.store.Touch(ctx, k, now); err != nil && !errors.Is(err, ErrNoSession) {
			return Session{}, nil, fmt.Errorf("login: updating the session: %w", err)
		}
	}
	return s, u, nil
}

// sessionID returns the request's session id: the value of its secure
// cookie, or over plain HTTP its other one.
func (l *Login) sessionID(r *http.Request) (string, error) {
	var vals []string
	for _, c := range r.CookiesNamed(SecureCookieName) {
		vals = append(vals, c.Value)
	}
	if !l.secure(r) {
		for _, c := range r.CookiesNamed(CookieName) {
			vals = append(vals, c.Value)
		}
	}
	switch {
	case len(vals) == 0:
		return "", auth.ErrNoCredentials
	case len(vals) > 1:
		return "", fmt.Errorf("%w: %d session cookies", auth.ErrInvalidCredentials, len(vals))
	}
	if b, err := base64.RawURLEncoding.Strict().DecodeString(vals[0]); err != nil || len(b) != idBytes {
		return "", fmt.Errorf("%w: a session cookie is %d bytes of base64url", auth.ErrInvalidCredentials, idBytes)
	}
	return vals[0], nil
}

// CheckCSRF returns nil if r may act for its session: a safe method, or an
// Origin header naming this server and the session's token in [CSRFHeader].
// Otherwise it returns an error matching [ErrCSRF], or one of
// [Login.Authenticate]'s for a request with no valid session. Call it only
// for requests a session authenticated: bearer credentials are not sent by
// a browser on its own, so they need no check.
func (l *Login) CheckCSRF(r *http.Request) error {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return nil
	}
	if !l.sameOrigin(r, r.Header.Values("Origin")) {
		return fmt.Errorf("%w: Origin is missing or not this server", ErrCSRF)
	}
	s, _, err := l.session(r)
	if err != nil {
		return err
	}
	got := r.Header.Values(CSRFHeader)
	if len(got) != 1 || subtle.ConstantTimeCompare([]byte(got[0]), []byte(s.CSRF)) != 1 {
		return fmt.Errorf("%w: %s is missing or wrong", ErrCSRF, CSRFHeader)
	}
	return nil
}

// sameOrigin reports whether origins is one Origin header naming the
// request's own origin: its scheme and Host.
func (l *Login) sameOrigin(r *http.Request, origins []string) bool {
	if len(origins) != 1 || r.Host == "" {
		return false
	}
	scheme := "http"
	if l.secure(r) {
		scheme = "https"
	}
	return strings.EqualFold(origins[0], scheme+"://"+r.Host)
}

// secure reports whether the request came over TLS: to this server, or to a
// trusted proxy in front of it.
func (l *Login) secure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if !l.fromProxy(r.RemoteAddr) {
		return false
	}
	p := r.Header.Values("X-Forwarded-Proto")
	return len(p) == 1 && strings.EqualFold(strings.TrimSpace(p[0]), "https")
}

// clientAddr is the client's IP address: the peer's, or behind trusted
// proxies the last X-Forwarded-For address that is not one.
func (l *Login) clientAddr(r *http.Request) string {
	peer := hostOf(r.RemoteAddr)
	if !l.fromProxy(r.RemoteAddr) {
		return peer
	}
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(v, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		if !l.trusted(a) {
			return a.Unmap().String()
		}
	}
	return peer
}

// throttleAddr is the address a client's failures are counted under: an
// IPv6 client's /64, since one host is commonly given a whole /64 and could
// otherwise rotate through it.
func throttleAddr(addr string) string {
	a, err := netip.ParseAddr(addr)
	if err != nil || !a.Is6() || a.Is4In6() {
		return addr
	}
	return netip.PrefixFrom(a, 64).Masked().String()
}

func (l *Login) fromProxy(remote string) bool {
	a, err := netip.ParseAddr(hostOf(remote))
	return err == nil && l.trusted(a)
}

func (l *Login) trusted(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range l.proxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func hostOf(remote string) string {
	if h, _, err := net.SplitHostPort(remote); err == nil {
		return h
	}
	return remote
}

// cookie is the session cookie for r: value for maxAge seconds (-1
// clears it).
func (l *Login) cookie(r *http.Request, value string, maxAge int) *http.Cookie {
	c := &http.Cookie{Name: CookieName, Value: value, Path: "/", MaxAge: maxAge, HttpOnly: true, SameSite: http.SameSiteStrictMode}
	if l.secure(r) {
		c.Name, c.Secure = SecureCookieName, true
	}
	return c
}

// random returns idBytes from crypto/rand, base64url.
func random() string {
	b := make([]byte, idBytes)
	_, _ = rand.Read(b) // never fails (crypto/rand)
	return base64.RawURLEncoding.EncodeToString(b)
}

// key is where the store keeps the session of id.
func key(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}
