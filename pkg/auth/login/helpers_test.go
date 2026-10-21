package login_test

import (
	"bytes"
	"crypto/tls"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/auth/authtest"
	"github.com/bsamanvith/agentkit/pkg/auth/login"
	"github.com/bsamanvith/agentkit/pkg/auth/login/logintest"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

// The test users' passwords. Each is a canary: the rig fails a test whose
// logs hold any of them.
const (
	alicePassword = "alice-CANARY-pw-3f9e2a7c"
	rootPassword  = "root-CANARY-pw-8b1d4e6f"
	bobPassword   = "bob-CANARY-pw-5c7a0e2d"
)

var passwords = []string{alicePassword, rootPassword, bobPassword}

var start = time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)

// rig is a Login of three users (alice and root of tenant acme, bob of
// globex) on a fake clock and the counting hasher. When the test ends it
// checks that no log line (debug level) holds a password or a session id it
// handed out.
type rig struct {
	t      *testing.T
	l      *login.Login
	clk    *clocktest.Fake
	hasher *logintest.Hasher
	logs   *syncBuffer

	mu  sync.Mutex
	ids []string // session ids handed out
}

func testUsers(h login.Hasher) []login.User {
	p := login.DefaultParams
	return []login.User{
		{Username: "alice", PasswordHash: logintest.PHC(h, alicePassword, p), Tenant: "acme", Roles: []string{login.RoleUser}},
		{Username: "root", PasswordHash: logintest.PHC(h, rootPassword, p), Tenant: "acme", Roles: []string{login.RoleAdmin}, Toolsets: []string{"crm", "mail-*"}},
		{Username: "bob", PasswordHash: logintest.PHC(h, bobPassword, p), Tenant: "globex", Roles: []string{login.RoleUser, login.RoleAdmin}},
	}
}

func newRig(t *testing.T, opts ...login.Option) *rig {
	t.Helper()
	r := &rig{t: t, clk: clocktest.NewFake(start), hasher: &logintest.Hasher{}, logs: &syncBuffer{}}
	log := slog.New(slog.NewJSONHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	base := []login.Option{login.WithClock(r.clk), login.WithHasher(r.hasher.Hash), login.WithLogger(log)}
	l, err := login.New(testUsers(r.hasher.Hash), append(base, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	r.l = l
	t.Cleanup(r.check)
	return r
}

// check is the invariant every test ends with: no secret in the logs.
func (r *rig) check() {
	logs := r.logs.String()
	for _, p := range passwords {
		if authtest.Leaks(logs, p) {
			r.t.Errorf("a password is in the logs:\n%s", logs)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range r.ids {
		if authtest.Leaks(logs, id) {
			r.t.Errorf("a session id is in the logs:\n%s", logs)
		}
	}
}

// req is a request from addr (a "host:port") with header pairs.
func req(method, addr string, header ...string) *http.Request {
	r := httptest.NewRequest(method, "http://agents.example.com/v1/runs", nil)
	r.RemoteAddr = addr
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Add(header[i], header[i+1])
	}
	return r
}

// tlsReq is req over TLS.
func tlsReq(method, addr string, header ...string) *http.Request {
	r := req(method, addr, header...)
	r.TLS = &tls.ConnectionState{}
	return r
}

const addr1, addr2 = "203.0.113.7:51000", "198.51.100.9:42000"

// signIn signs in from addr over plain HTTP.
func (r *rig) signIn(user, password, addr string) (*http.Cookie, error) {
	return r.signInReq(req(http.MethodPost, addr), user, password)
}

func (r *rig) signInReq(q *http.Request, user, password string) (*http.Cookie, error) {
	c, err := r.l.SignIn(q, user, password)
	if err != nil {
		for _, p := range passwords {
			if authtest.Leaks(err.Error(), p) {
				r.t.Errorf("SignIn's error holds a password: %v", err)
			}
		}
		return nil, err
	}
	r.mu.Lock()
	r.ids = append(r.ids, c.Value)
	r.mu.Unlock()
	return c, nil
}

// session signs user in and returns the cookie.
func (r *rig) session(user, password string) *http.Cookie {
	r.t.Helper()
	c, err := r.signIn(user, password, addr1)
	if err != nil {
		r.t.Fatalf("signing in %s: %v", user, err)
	}
	return c
}

// with returns a request from addr1 carrying the session cookie c.
func with(c *http.Cookie, method string, header ...string) *http.Request {
	q := req(method, addr1, header...)
	q.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	return q
}

// authenticate authenticates q, checking the error class.
func (r *rig) authenticate(q *http.Request) (auth.Principal, error) {
	r.t.Helper()
	p, err := r.l.Authenticate(q)
	if err != nil && !errors.Is(err, auth.ErrNoCredentials) && !errors.Is(err, auth.ErrInvalidCredentials) {
		r.t.Errorf("Authenticate: an internal error %v", err)
	}
	return p, err
}

func prefix(s string) netip.Prefix { return netip.MustParsePrefix(s) }

// syncBuffer is a bytes.Buffer safe for concurrent writes (the logger's).
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
