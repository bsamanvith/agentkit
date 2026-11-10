package accounts

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// CSRFHeader carries a session's CSRF token on unsafe requests.
const CSRFHeader = "X-CSRF-Token"

// touchEvery is how stale a session's LastSeen may get before a request
// updates it.
const touchEvery = time.Minute

// idBytes is the length of a session id and a CSRF token: 256 bits.
const idBytes = 32

// Me is the account of a request's session.
type Me struct {
	Account Account
	// Lane is the lane the session signed in with.
	Lane string
	// CSRF is the token the session's unsafe requests send in [CSRFHeader].
	CSRF string
}

// StartSession starts a session for account, signed in through lane, and
// returns its cookie to set on the response. A session the browser already
// had ends.
func (s *Service) StartSession(r *http.Request, account, lane string) (*http.Cookie, error) {
	ctx := r.Context()
	if old, err := s.sessionID(r); err == nil {
		if err := s.store.DeleteSession(ctx, key(old)); err != nil {
			return nil, fmt.Errorf("accounts: ending the previous session: %w", err)
		}
	}
	s.expire(ctx)
	now := s.clock.Now()
	id := random()
	if err := s.store.CreateSession(ctx, key(id), Session{Account: account, Lane: lane, CSRF: random(), Created: now, LastSeen: now}); err != nil {
		return nil, fmt.Errorf("accounts: storing the session: %w", err)
	}
	s.log.LogAttrs(ctx, slog.LevelInfo, "accounts: signed in", slog.String("lane", lane), slog.String("account", account), slog.String("addr", s.clientAddr(r)))
	return s.cookie(r, id, int(s.absolute/time.Second)), nil
}

// SignOut ends the request's session, if any, and returns the cookie that
// clears it.
func (s *Service) SignOut(r *http.Request) (*http.Cookie, error) {
	if id, err := s.sessionID(r); err == nil {
		if err := s.store.DeleteSession(r.Context(), key(id)); err != nil {
			return nil, fmt.Errorf("accounts: deleting the session: %w", err)
		}
	}
	return s.cookie(r, "", -1), nil
}

// SignOutAll ends every session of account: after a lost phone, or a
// removed identity.
func (s *Service) SignOutAll(ctx context.Context, account string) error {
	return s.store.DeleteSessions(ctx, account)
}

// Me returns the request's session and account, or an error matching
// [ErrNoSession] when it has none (no cookie, an unknown or expired one, or
// a deleted account).
func (s *Service) Me(r *http.Request) (Me, error) {
	id, err := s.sessionID(r)
	if err != nil {
		return Me{}, err
	}
	ctx, k, now := r.Context(), key(id), s.clock.Now()
	sess, err := s.store.Session(ctx, k)
	switch {
	case errors.Is(err, ErrNotFound):
		return Me{}, fmt.Errorf("%w: unknown session", ErrNoSession)
	case err != nil:
		return Me{}, fmt.Errorf("accounts: reading the session: %w", err)
	}
	if now.Sub(sess.LastSeen) >= s.idle || now.Sub(sess.Created) >= s.absolute {
		_ = s.store.DeleteSession(ctx, k)
		return Me{}, fmt.Errorf("%w: the session expired", ErrNoSession)
	}
	acc, err := s.store.Account(ctx, sess.Account)
	switch {
	case errors.Is(err, ErrNotFound):
		_ = s.store.DeleteSession(ctx, k)
		return Me{}, fmt.Errorf("%w: the account is gone", ErrNoSession)
	case err != nil:
		return Me{}, err
	}
	if now.Sub(sess.LastSeen) >= touchEvery {
		if err := s.store.TouchSession(ctx, k, now); err != nil && !errors.Is(err, ErrNotFound) {
			return Me{}, fmt.Errorf("accounts: updating the session: %w", err)
		}
	}
	return Me{Account: acc, Lane: sess.Lane, CSRF: sess.CSRF}, nil
}

// CheckCSRF returns nil if r may act for its session: a safe method, or an
// Origin naming this server and the session's token in [CSRFHeader].
// Otherwise an error matching [ErrCSRF], or [ErrNoSession].
func (s *Service) CheckCSRF(r *http.Request) error {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return nil
	}
	if !s.SameOrigin(r) {
		return fmt.Errorf("%w: Origin is missing or not this server", ErrCSRF)
	}
	me, err := s.Me(r)
	if err != nil {
		return err
	}
	got := r.Header.Values(CSRFHeader)
	if len(got) != 1 || subtle.ConstantTimeCompare([]byte(got[0]), []byte(me.CSRF)) != 1 {
		return fmt.Errorf("%w: %s is missing or wrong", ErrCSRF, CSRFHeader)
	}
	return nil
}

// sessionID is the request's session id: its secure cookie's value, or
// over plain HTTP its other one.
func (s *Service) sessionID(r *http.Request) (string, error) {
	var vals []string
	for _, c := range r.CookiesNamed("__Host-" + s.cookieName) {
		vals = append(vals, c.Value)
	}
	if !s.Secure(r) {
		for _, c := range r.CookiesNamed(s.cookieName) {
			vals = append(vals, c.Value)
		}
	}
	switch {
	case len(vals) == 0:
		return "", fmt.Errorf("%w: no session cookie", ErrNoSession)
	case len(vals) > 1:
		return "", fmt.Errorf("%w: %d session cookies", ErrNoSession, len(vals))
	}
	if b, err := base64.RawURLEncoding.Strict().DecodeString(vals[0]); err != nil || len(b) != idBytes {
		return "", fmt.Errorf("%w: a malformed session cookie", ErrNoSession)
	}
	return vals[0], nil
}

// cookie is the session cookie for r: value for maxAge seconds (-1 clears).
func (s *Service) cookie(r *http.Request, value string, maxAge int) *http.Cookie {
	c := &http.Cookie{Name: s.cookieName, Value: value, Path: "/", MaxAge: maxAge, HttpOnly: true, SameSite: http.SameSiteStrictMode}
	if s.Secure(r) {
		c.Name, c.Secure = "__Host-"+s.cookieName, true
	}
	return c
}

// SameOrigin reports whether r has one Origin header naming its own
// scheme and Host.
func (s *Service) SameOrigin(r *http.Request) bool {
	o := r.Header.Values("Origin")
	if len(o) != 1 || r.Host == "" {
		return false
	}
	scheme := "http"
	if s.Secure(r) {
		scheme = "https"
	}
	return strings.EqualFold(o[0], scheme+"://"+r.Host)
}

// Secure reports whether r came over TLS, to this server or to a trusted
// proxy in front of it.
func (s *Service) Secure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if !s.fromProxy(r.RemoteAddr) {
		return false
	}
	p := r.Header.Values("X-Forwarded-Proto")
	return len(p) == 1 && strings.EqualFold(strings.TrimSpace(p[0]), "https")
}

// clientAddr is the client's IP address: the peer's, or behind trusted
// proxies the last X-Forwarded-For address that is not one. An IPv6
// client counts as its /64.
func (s *Service) clientAddr(r *http.Request) string {
	addr := hostOf(r.RemoteAddr)
	if s.fromProxy(r.RemoteAddr) {
		var hops []string
		for _, v := range r.Header.Values("X-Forwarded-For") {
			hops = append(hops, strings.Split(v, ",")...)
		}
		for i := len(hops) - 1; i >= 0; i-- {
			a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
			if err != nil {
				break
			}
			if !s.trusted(a) {
				addr = a.Unmap().String()
				break
			}
		}
	}
	if a, err := netip.ParseAddr(addr); err == nil && a.Is6() && !a.Is4In6() {
		return netip.PrefixFrom(a, 64).Masked().String()
	}
	return addr
}

func (s *Service) fromProxy(remote string) bool {
	a, err := netip.ParseAddr(hostOf(remote))
	return err == nil && s.trusted(a)
}

func (s *Service) trusted(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range s.proxies {
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

// random is idBytes from crypto/rand in base64url.
func random() string {
	b := make([]byte, idBytes)
	_, _ = rand.Read(b) // never fails (crypto/rand)
	return base64.RawURLEncoding.EncodeToString(b)
}

// key is where the store keeps the session of id.
func key(id string) string { return sha(id) }
