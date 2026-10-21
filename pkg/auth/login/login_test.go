package login_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/auth/authtest"
	"github.com/bsamanvith/agentkit/pkg/auth/login"
	"github.com/bsamanvith/agentkit/pkg/auth/login/logintest"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// sha256Hex is where a Store keeps the session of id.
func sha256Hex(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

func tenant(t *testing.T, id string) tenancy.Tenant {
	t.Helper()
	tn, err := tenancy.NewTenant(id)
	if err != nil {
		t.Fatal(err)
	}
	return tn
}

// The session cookie passes the suite every authenticator passes.
func TestConformance(t *testing.T) {
	t.Parallel()
	authtest.RunAuthenticator(t, func(t *testing.T) authtest.Subject {
		r := newRig(t)
		apply := func(q *http.Request, secret string) {
			q.AddCookie(&http.Cookie{Name: login.CookieName, Value: secret})
		}
		alice, bob := r.session("alice", alicePassword), r.session("bob", bobPassword)
		return authtest.Subject{Authenticator: r.l, Credentials: []authtest.Credential{
			{Name: "alice", Secret: alice.Value, Apply: apply, Want: auth.Principal{
				Tenant: tenant(t, "acme"), User: "alice", Scopes: []string{"agent:run", "agent:read"}, Toolsets: []string{"*"},
				Via: auth.Credential{Kind: login.KindSession, ID: "alice"},
			}},
			{Name: "bob", Secret: bob.Value, Apply: apply, Want: auth.Principal{
				Tenant: tenant(t, "globex"), User: "bob", Scopes: []string{"agent:run", "agent:read", "agent:admin", "admin:*"}, Toolsets: []string{"*"},
				Via: auth.Credential{Kind: login.KindSession, ID: "bob"},
			}},
		}}
	})
}

// Login and logout: the right password gives a session whose principal and
// /me information are the user's; a wrong password and an unknown user fail
// alike; signing out ends the session on the server.
func TestSignInAndOut(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	c := r.session("root", rootPassword)
	p, err := r.authenticate(with(c, http.MethodGet))
	want := auth.Principal{
		Tenant: tenant(t, "acme"), User: "root", Scopes: []string{"agent:run", "agent:read", "agent:admin", "admin:*"},
		Toolsets: []string{"crm", "mail-*"}, Via: auth.Credential{Kind: login.KindSession, ID: "root"},
	}
	if err != nil || !authtest.Equal(p, want) {
		t.Fatalf("the session's principal: %+v, %v; want %+v", p, err, want)
	}
	info, err := r.l.Session(with(c, http.MethodGet))
	if err != nil || info.Username != "root" || !slices.Equal(info.Roles, []string{login.RoleAdmin}) || len(info.CSRF) < 22 || info.CSRF == c.Value {
		t.Errorf("Session: %+v, %v", info, err)
	}

	for name, try := range map[string][2]string{
		"wrong password":    {"root", alicePassword},
		"unknown user":      {"nobody", rootPassword},
		"case of the name":  {"Root", rootPassword},
		"empty password":    {"root", ""},
		"password too long": {"root", rootPassword + strings.Repeat("x", login.MaxPasswordLength)},
	} {
		c, err := r.signIn(try[0], try[1], addr2)
		if !errors.Is(err, login.ErrBadLogin) || c != nil || err.Error() != login.ErrBadLogin.Error() {
			t.Errorf("%s: %v, %v; want exactly ErrBadLogin", name, c, err)
		}
	}

	out, err := r.l.SignOut(with(c, http.MethodPost))
	if err != nil || out.Name != c.Name || out.Value != "" || out.MaxAge >= 0 || out.Path != "/" {
		t.Errorf("SignOut's cookie: %+v, %v; want one that clears the session cookie", out, err)
	}
	if _, err := r.authenticate(with(c, http.MethodGet)); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("the session after sign-out: %v, want ErrInvalidCredentials", err)
	}
	if _, err := r.l.Session(with(c, http.MethodGet)); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("Session after sign-out: %v, want ErrInvalidCredentials", err)
	}
	// Signing out without a session only clears the cookie.
	if out, err := r.l.SignOut(req(http.MethodPost, addr1)); err != nil || out.MaxAge >= 0 {
		t.Errorf("SignOut with no session: %+v, %v", out, err)
	}
}

// An unknown user's attempt does the same work as a known user's wrong one:
// the hasher's clock moves by the same amount for each, whichever order they
// come in, and the answer is the same error.
func TestFailedSignInsCostTheSame(t *testing.T) {
	t.Parallel()
	for name, users := range map[string]func(h login.Hasher) []login.User{
		"same parameters": testUsers,
		"a costlier user": func(h login.Hasher) []login.User {
			us := testUsers(h)
			us[1].PasswordHash = logintest.PHC(h, rootPassword, login.Params{Memory: 64 * 1024, Time: 3, Threads: 4})
			return us
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := &logintest.Hasher{}
			clk := clocktest.NewFake(start)
			l, err := login.New(users(h.Hash), login.WithHasher(h.Hash), login.WithClock(clk))
			if err != nil {
				t.Fatal(err)
			}
			cost := func(user string) time.Duration {
				// An hour apart, so earlier failures are forgotten: no throttling.
				clk.Advance(time.Hour)
				before := h.Elapsed()
				if _, err := l.SignIn(req(http.MethodPost, addr1), user, "a-wrong-password"); !errors.Is(err, login.ErrBadLogin) {
					t.Fatalf("%s: %v", user, err)
				}
				return h.Elapsed() - before
			}
			var known, unknown []time.Duration
			rng := rand.New(rand.NewPCG(1, 2))
			for i := range 200 {
				if rng.IntN(2) == 0 {
					known = append(known, cost([]string{"alice", "root", "bob"}[i%3]))
				} else {
					unknown = append(unknown, cost(fmt.Sprintf("ghost%d", i)))
				}
			}
			// The bound is zero: an unknown user costs what the costliest
			// known one does, and with one set of parameters every attempt
			// costs exactly the same.
			if slices.Max(unknown) < slices.Max(known) || slices.Min(unknown) < slices.Max(known) {
				t.Errorf("an unknown user cost %v to %v, a known one up to %v", slices.Min(unknown), slices.Max(unknown), slices.Max(known))
			}
			if name == "same parameters" && (slices.Min(known) != slices.Max(unknown) || slices.Max(known) != slices.Min(unknown)) {
				t.Errorf("costs differ: known %v to %v, unknown %v to %v", slices.Min(known), slices.Max(known), slices.Min(unknown), slices.Max(unknown))
			}
		})
	}
}

// Cookie: HttpOnly, SameSite=Strict, path /, for the absolute lifetime;
// Secure with the __Host- name over TLS, or behind a trusted proxy that
// says https; the session id 256 bits from crypto/rand.
func TestCookie(t *testing.T) {
	t.Parallel()
	r := newRig(t, login.WithTrustedProxies(prefix("10.0.0.0/8")))
	check := func(name string, q *http.Request, secure bool) *http.Cookie {
		t.Helper()
		c, err := r.signInReq(q, "alice", alicePassword)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		wantName := login.CookieName
		if secure {
			wantName = login.SecureCookieName
		}
		if c.Name != wantName || c.Secure != secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" ||
			c.Domain != "" || c.MaxAge != int(login.AbsoluteTimeout/time.Second) {
			t.Errorf("%s: cookie %s", name, c.String())
		}
		b, err := base64.RawURLEncoding.DecodeString(c.Value)
		if err != nil || len(b) < 16 {
			t.Errorf("%s: the session id is %d bytes (%v), want at least 16", name, len(b), err)
		}
		return c
	}
	check("plain HTTP", req(http.MethodPost, addr1), false)
	check("TLS", tlsReq(http.MethodPost, addr1), true)
	check("a trusted proxy's https", req(http.MethodPost, "10.1.2.3:443", "X-Forwarded-Proto", "https"), true)
	check("a trusted proxy's http", req(http.MethodPost, "10.1.2.3:443", "X-Forwarded-Proto", "http"), false)
	check("an untrusted peer's https", req(http.MethodPost, addr1, "X-Forwarded-Proto", "https"), false)

	// The secure cookie is accepted anywhere; the plain one only over plain
	// HTTP, where a network attacker could have set it anyway.
	sec := check("TLS again", tlsReq(http.MethodPost, addr1), true)
	q := tlsReq(http.MethodGet, addr1)
	q.AddCookie(&http.Cookie{Name: login.SecureCookieName, Value: sec.Value})
	if _, err := r.authenticate(q); err != nil {
		t.Errorf("the secure cookie over TLS: %v", err)
	}
	plain := check("plain again", req(http.MethodPost, addr1), false)
	q = tlsReq(http.MethodGet, addr1)
	q.AddCookie(&http.Cookie{Name: login.CookieName, Value: plain.Value})
	if _, err := r.authenticate(q); !errors.Is(err, auth.ErrNoCredentials) {
		t.Errorf("the plain cookie over TLS: %v, want it ignored", err)
	}

	// Two session cookies are refused, not one picked.
	q = with(plain, http.MethodGet)
	q.AddCookie(&http.Cookie{Name: login.SecureCookieName, Value: sec.Value})
	if _, err := r.authenticate(q); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("two session cookies: %v, want ErrInvalidCredentials", err)
	}

	// Ids are distinct, and not the CSRF token.
	seen := map[string]bool{}
	for range 200 {
		c := r.session("alice", alicePassword)
		info, err := r.l.Session(with(c, http.MethodGet))
		if err != nil || seen[c.Value] || seen[info.CSRF] || c.Value == info.CSRF {
			t.Fatalf("a session id or CSRF token repeats (%v)", err)
		}
		seen[c.Value], seen[info.CSRF] = true, true
	}
}

// Sessions end 12 hours after their last use, and 7 days after sign-in
// however much they are used.
func TestSessionLifetimes(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	idle := r.session("alice", alicePassword)
	active := r.session("bob", bobPassword)
	// A request on active every 11h59m, until the last is less than that
	// before 7 days.
	for !r.clk.Now().Add(login.IdleTimeout - time.Minute).After(start.Add(login.AbsoluteTimeout - time.Second)) {
		r.clk.Advance(login.IdleTimeout - time.Minute)
		if _, err := r.authenticate(with(active, http.MethodGet)); err != nil {
			t.Fatalf("an active session at %v: %v", r.clk.Now().Sub(start), err)
		}
	}
	if _, err := r.authenticate(with(idle, http.MethodGet)); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("a session idle 12h: %v, want ErrInvalidCredentials", err)
	}
	r.clk.Advance(login.AbsoluteTimeout - r.clk.Now().Sub(start) - time.Second)
	if _, err := r.authenticate(with(active, http.MethodGet)); err != nil {
		t.Errorf("an active session a second before 7 days: %v", err)
	}
	r.clk.Advance(time.Second)
	if _, err := r.authenticate(with(active, http.MethodGet)); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("an active session at 7 days: %v, want ErrInvalidCredentials", err)
	}

	// Exactly at the idle timeout it has ended.
	s := r.session("alice", alicePassword)
	r.clk.Advance(login.IdleTimeout - time.Nanosecond)
	if _, err := r.authenticate(with(s, http.MethodGet)); err != nil {
		t.Errorf("just under 12h idle: %v", err)
	}
	s2 := r.session("alice", alicePassword)
	r.clk.Advance(login.IdleTimeout)
	if _, err := r.authenticate(with(s2, http.MethodGet)); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("exactly 12h idle: %v, want ErrInvalidCredentials", err)
	}
}

// CSRF: an unsafe request needs this server's Origin and the session's
// token; a safe one needs neither; a sign-in from another origin is refused.
func TestCheckCSRF(t *testing.T) {
	t.Parallel()
	r := newRig(t, login.WithTrustedProxies(prefix("10.0.0.0/8")))
	c := r.session("alice", alicePassword)
	info, err := r.l.Session(with(c, http.MethodGet))
	if err != nil {
		t.Fatal(err)
	}
	other := r.session("bob", bobPassword)
	otherInfo, err := r.l.Session(with(other, http.MethodGet))
	if err != nil {
		t.Fatal(err)
	}
	self := "http://agents.example.com"
	for _, tc := range []struct {
		name   string
		method string
		header []string
		ok     bool
	}{
		{"GET with nothing", http.MethodGet, nil, true},
		{"HEAD with nothing", http.MethodHead, nil, true},
		{"OPTIONS with nothing", http.MethodOptions, nil, true},
		{"POST with both", http.MethodPost, []string{"Origin", self, login.CSRFHeader, info.CSRF}, true},
		{"DELETE with both", http.MethodDelete, []string{"Origin", self, login.CSRFHeader, info.CSRF}, true},
		{"POST, Origin's case", http.MethodPost, []string{"Origin", "HTTP://Agents.Example.com", login.CSRFHeader, info.CSRF}, true},
		{"POST with no header", http.MethodPost, []string{"Origin", self}, false},
		{"POST with no Origin", http.MethodPost, []string{login.CSRFHeader, info.CSRF}, false},
		{"POST with nothing", http.MethodPost, nil, false},
		{"POST from another site", http.MethodPost, []string{"Origin", "https://evil.example", login.CSRFHeader, info.CSRF}, false},
		{"POST from a sibling port", http.MethodPost, []string{"Origin", self + ":8443", login.CSRFHeader, info.CSRF}, false},
		{"POST, https Origin over http", http.MethodPost, []string{"Origin", "https://agents.example.com", login.CSRFHeader, info.CSRF}, false},
		{"POST, Origin null", http.MethodPost, []string{"Origin", "null", login.CSRFHeader, info.CSRF}, false},
		{"POST, two Origins", http.MethodPost, []string{"Origin", self, "Origin", self, login.CSRFHeader, info.CSRF}, false},
		{"POST, another session's token", http.MethodPost, []string{"Origin", self, login.CSRFHeader, otherInfo.CSRF}, false},
		{"POST, the token cut", http.MethodPost, []string{"Origin", self, login.CSRFHeader, info.CSRF[:len(info.CSRF)-1]}, false},
		{"POST, two tokens", http.MethodPost, []string{"Origin", self, login.CSRFHeader, info.CSRF, login.CSRFHeader, info.CSRF}, false},
		{"POST, the session id as token", http.MethodPost, []string{"Origin", self, login.CSRFHeader, c.Value}, false},
		{"PUT with no header", http.MethodPut, []string{"Origin", self}, false},
		{"PATCH with no header", http.MethodPatch, []string{"Origin", self}, false},
	} {
		err := r.l.CheckCSRF(with(c, tc.method, tc.header...))
		if tc.ok && err != nil || !tc.ok && !errors.Is(err, login.ErrCSRF) {
			t.Errorf("%s: %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
	// Behind a trusted proxy saying https, the origin is https.
	q := req(http.MethodPost, "10.0.0.1:1", "X-Forwarded-Proto", "https", "Origin", "https://agents.example.com", login.CSRFHeader, info.CSRF)
	q.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	if err := r.l.CheckCSRF(q); !errors.Is(err, auth.ErrNoCredentials) {
		// The plain cookie is not read over https: no session.
		t.Errorf("the plain cookie behind an https proxy: %v, want ErrNoCredentials", err)
	}
	sec, err := r.signInReq(req(http.MethodPost, "10.0.0.1:1", "X-Forwarded-Proto", "https"), "alice", alicePassword)
	if err != nil {
		t.Fatal(err)
	}
	secInfo, err := r.l.Session(func() *http.Request {
		q := req(http.MethodGet, "10.0.0.1:1", "X-Forwarded-Proto", "https")
		q.AddCookie(&http.Cookie{Name: sec.Name, Value: sec.Value})
		return q
	}())
	if err != nil {
		t.Fatal(err)
	}
	q = req(http.MethodPost, "10.0.0.1:1", "X-Forwarded-Proto", "https", "Origin", "https://agents.example.com", login.CSRFHeader, secInfo.CSRF)
	q.AddCookie(&http.Cookie{Name: sec.Name, Value: sec.Value})
	if err := r.l.CheckCSRF(q); err != nil {
		t.Errorf("https behind a trusted proxy: %v", err)
	}

	// A sign-in may come with no Origin (a script), or this server's, never
	// another's.
	if _, err := r.signInReq(req(http.MethodPost, addr1, "Origin", "https://evil.example"), "alice", alicePassword); !errors.Is(err, login.ErrCSRF) {
		t.Errorf("a sign-in from another site: %v, want ErrCSRF", err)
	}
	if _, err := r.signInReq(req(http.MethodPost, addr1, "Origin", self), "alice", alicePassword); err != nil {
		t.Errorf("a sign-in from this server's page: %v", err)
	}
}

// Throttling: after 10 failures for a username the next attempt waits, even
// with the right password, and the wait doubles with each further failure;
// other users are not affected. An address is held back after 100.
func TestThrottling(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	for i := range 10 {
		if _, err := r.signIn("alice", "wrong-password", addr1); !errors.Is(err, login.ErrBadLogin) {
			t.Fatalf("failure %d: %v", i+1, err)
		}
	}
	calls := r.hasher.Calls()
	var th *login.ThrottledError
	_, err := r.signIn("alice", alicePassword, addr2)
	if !errors.As(err, &th) || th.RetryAfter != time.Second || th.RetryAfterSeconds() != 1 {
		t.Fatalf("the 11th attempt, from another address and right: %v, want a 1s wait", err)
	}
	if r.hasher.Calls() != calls {
		t.Errorf("a throttled attempt checked the password")
	}
	// Other users, from the same address too, are not held back.
	if _, err := r.signIn("bob", bobPassword, addr1); err != nil {
		t.Errorf("bob from the same address: %v", err)
	}
	// An unknown name is throttled the same way (no hint it does not exist).
	for range 10 {
		_, _ = r.signIn("ghost", "wrong-password", addr2)
	}
	if _, err := r.signIn("ghost", "wrong-password", addr2); !errors.As(err, &th) {
		t.Errorf("an unknown name after 10 failures: %v, want throttled", err)
	}

	// The wait doubles with each failure past the threshold.
	for _, wait := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second} {
		r.clk.Advance(wait - time.Millisecond)
		if _, err := r.signIn("alice", "wrong-password", addr1); !errors.As(err, &th) || th.RetryAfter != time.Millisecond {
			t.Fatalf("a millisecond before the %v wait ends: %v", wait, err)
		}
		r.clk.Advance(time.Millisecond)
		if _, err := r.signIn("alice", "wrong-password", addr1); !errors.Is(err, login.ErrBadLogin) {
			t.Fatalf("when the %v wait ends: %v, want the password checked", wait, err)
		}
	}
	// The cap.
	for range 20 {
		r.clk.Advance(time.Hour - time.Second)
		_, _ = r.signIn("alice", "wrong-password", addr1)
	}
	if _, err := r.signIn("alice", "wrong-password", addr1); !errors.As(err, &th) || th.RetryAfter > 15*time.Minute {
		t.Errorf("after many failures: %v, want a wait of at most 15 minutes", err)
	}
	// A success resets the count.
	r.clk.Advance(15 * time.Minute)
	if _, err := r.signIn("alice", alicePassword, addr1); err != nil {
		t.Fatalf("the right password once the wait is over: %v", err)
	}
	for i := range 9 {
		if _, err := r.signIn("alice", "wrong-password", addr1); !errors.Is(err, login.ErrBadLogin) {
			t.Fatalf("after a success, failure %d: %v, want ErrBadLogin (the count starts again)", i+1, err)
		}
	}

	// An address that fails 100 times, across names, is held back for every
	// name; another address is not.
	const busy = "192.0.2.50:1000"
	for i := range 100 {
		_, _ = r.signIn(fmt.Sprintf("guess%d", i), "wrong-password", busy)
	}
	if _, err := r.signIn("bob", bobPassword, busy); !errors.As(err, &th) {
		t.Errorf("bob from an address with 100 failures: %v, want throttled", err)
	}
	if _, err := r.signIn("bob", bobPassword, addr2); err != nil {
		t.Errorf("bob from another address: %v", err)
	}
}

// Behind a trusted proxy, the client's address is the last untrusted one of
// X-Forwarded-For; from any other peer the header is ignored.
func TestClientAddress(t *testing.T) {
	t.Parallel()
	r := newRig(t, login.WithTrustedProxies(prefix("10.0.0.0/8")))
	fail := func(peer, xff string) {
		for i := range 100 {
			q := req(http.MethodPost, peer, "X-Forwarded-For", xff)
			_, _ = r.signInReq(q, fmt.Sprintf("u%d", i), "wrong-password")
		}
	}
	// Through the proxy, a client spoofing a first hop: counted against its
	// own address, 203.0.113.1, not the spoofed one.
	fail("10.0.0.1:1", "198.51.100.200, 203.0.113.1")
	var th *login.ThrottledError
	if _, err := r.signInReq(req(http.MethodPost, "10.0.0.2:1", "X-Forwarded-For", "203.0.113.1, 10.9.9.9"), "bob", bobPassword); !errors.As(err, &th) {
		t.Errorf("the client's own address through another proxy: %v, want throttled", err)
	}
	if _, err := r.signInReq(req(http.MethodPost, "10.0.0.2:1", "X-Forwarded-For", "198.51.100.200"), "bob", bobPassword); err != nil {
		t.Errorf("the spoofed address: %v, want it not throttled", err)
	}
	// From an untrusted peer, X-Forwarded-For is ignored.
	if _, err := r.signInReq(req(http.MethodPost, "192.0.2.77:1", "X-Forwarded-For", "203.0.113.1"), "bob", bobPassword); err != nil {
		t.Errorf("an untrusted peer naming the throttled address: %v", err)
	}
}

// Configuration errors name the user, never the hash.
func TestConfigErrors(t *testing.T) {
	t.Parallel()
	h := &logintest.Hasher{}
	good := logintest.PHC(h.Hash, alicePassword, login.DefaultParams)
	salt, key, _ := strings.Cut(strings.TrimPrefix(good, "$argon2id$v=19$m=19456,t=2,p=1$"), "$")
	for name, u := range map[string]login.User{
		"empty username":     {Username: "", PasswordHash: good, Tenant: "acme", Roles: []string{"user"}},
		"space in username":  {Username: "a b", PasswordHash: good, Tenant: "acme", Roles: []string{"user"}},
		"long username":      {Username: strings.Repeat("a", 65), PasswordHash: good, Tenant: "acme", Roles: []string{"user"}},
		"no hash":            {Username: "u", Tenant: "acme", Roles: []string{"user"}},
		"argon2i":            {Username: "u", PasswordHash: strings.Replace(good, "argon2id", "argon2i", 1), Tenant: "acme", Roles: []string{"user"}},
		"bcrypt":             {Username: "u", PasswordHash: "$2b$12$" + salt, Tenant: "acme", Roles: []string{"user"}},
		"version 16":         {Username: "u", PasswordHash: strings.Replace(good, "v=19", "v=16", 1), Tenant: "acme", Roles: []string{"user"}},
		"no p":               {Username: "u", PasswordHash: strings.Replace(good, ",p=1", "", 1), Tenant: "acme", Roles: []string{"user"}},
		"m twice":            {Username: "u", PasswordHash: strings.Replace(good, "t=2", "m=2", 1), Tenant: "acme", Roles: []string{"user"}},
		"m too large":        {Username: "u", PasswordHash: strings.Replace(good, "m=19456", "m=2097152", 1), Tenant: "acme", Roles: []string{"user"}},
		"t zero":             {Username: "u", PasswordHash: strings.Replace(good, "t=2", "t=0", 1), Tenant: "acme", Roles: []string{"user"}},
		"m with a sign":      {Username: "u", PasswordHash: strings.Replace(good, "m=19456", "m=+19456", 1), Tenant: "acme", Roles: []string{"user"}},
		"padded salt":        {Username: "u", PasswordHash: strings.Replace(good, salt, salt+"==", 1), Tenant: "acme", Roles: []string{"user"}},
		"short key":          {Username: "u", PasswordHash: strings.Replace(good, key, key[:10], 1), Tenant: "acme", Roles: []string{"user"}},
		"trailing part":      {Username: "u", PasswordHash: good + "$x", Tenant: "acme", Roles: []string{"user"}},
		"no tenant":          {Username: "u", PasswordHash: good, Roles: []string{"user"}},
		"bad tenant":         {Username: "u", PasswordHash: good, Tenant: "a/b", Roles: []string{"user"}},
		"no roles":           {Username: "u", PasswordHash: good, Tenant: "acme"},
		"unknown role":       {Username: "u", PasswordHash: good, Tenant: "acme", Roles: []string{"owner"}},
		"role twice":         {Username: "u", PasswordHash: good, Tenant: "acme", Roles: []string{"user", "user"}},
		"bad toolset":        {Username: "u", PasswordHash: good, Tenant: "acme", Roles: []string{"user"}, Toolsets: []string{"["}},
		"empty toolset name": {Username: "u", PasswordHash: good, Tenant: "acme", Roles: []string{"user"}, Toolsets: []string{""}},
	} {
		_, err := login.New([]login.User{u})
		var ce *login.ConfigError
		if !errors.As(err, &ce) || ce.Username != u.Username {
			t.Errorf("%s: %v, want a ConfigError naming %q", name, err, u.Username)
			continue
		}
		if strings.Contains(err.Error(), key) || strings.Contains(err.Error(), salt) || strings.Contains(err.Error(), u.PasswordHash) && u.PasswordHash != "" {
			t.Errorf("%s: the error holds the hash: %v", name, err)
		}
	}
	dup := []login.User{
		{Username: "u", PasswordHash: good, Tenant: "acme", Roles: []string{"user"}},
		{Username: "u", PasswordHash: good, Tenant: "globex", Roles: []string{"admin"}},
	}
	if _, err := login.New(dup); err == nil {
		t.Error("a username listed twice was accepted")
	}
	if _, err := login.New([]login.User{{Username: "a.b_c-d@e+f", PasswordHash: good, Tenant: "acme", Roles: []string{"admin", "user"}}}); err != nil {
		t.Errorf("a good user: %v", err)
	}
}

// HashPassword makes an argon2id hash that the real hasher accepts.
func TestHashPassword(t *testing.T) {
	t.Parallel()
	h, err := login.HashPassword(alicePassword)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") || strings.Contains(h, alicePassword) {
		t.Errorf("HashPassword = %q", h)
	}
	h2, err := login.HashPassword(alicePassword)
	if err != nil || h2 == h {
		t.Errorf("two hashes of one password are the same (no salt?)")
	}
	l, err := login.New([]login.User{{Username: "alice", PasswordHash: h, Tenant: "acme", Roles: []string{"user"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.SignIn(req(http.MethodPost, addr1), "alice", alicePassword); err != nil {
		t.Errorf("the right password with argon2id: %v", err)
	}
	if _, err := l.SignIn(req(http.MethodPost, addr1), "alice", alicePassword+"x"); !errors.Is(err, login.ErrBadLogin) {
		t.Errorf("a wrong password with argon2id: %v", err)
	}
	for _, bad := range []string{"", "short", strings.Repeat("x", login.MaxPasswordLength+1)} {
		if _, err := login.HashPassword(bad); !errors.Is(err, login.ErrPasswordLength) {
			t.Errorf("HashPassword of %d bytes: %v", len(bad), err)
		}
	}
}

// Roles grant their scopes, and nothing else.
func TestScopesOf(t *testing.T) {
	t.Parallel()
	user := login.ScopesOf([]string{login.RoleUser})
	admin := login.ScopesOf([]string{login.RoleAdmin})
	p := func(s []string) auth.Principal { return auth.Principal{Scopes: s} }
	for _, s := range []string{"agent:run", "agent:read"} {
		if !p(user).HasScope(s) || !p(admin).HasScope(s) {
			t.Errorf("%s: user %v, admin %v", s, user, admin)
		}
	}
	for _, s := range []string{"agent:admin", "admin:agents", "admin:credentials"} {
		if p(user).HasScope(s) || !p(admin).HasScope(s) {
			t.Errorf("%s: a user has it (%v) or an admin lacks it (%v)", s, user, admin)
		}
	}
	if p(admin).HasScope("*") || p(admin).HasScope("other:thing") || len(login.ScopesOf(nil)) != 0 {
		t.Errorf("roles grant more than their scopes: %v, %v", admin, login.ScopesOf(nil))
	}
}

func TestMemoryStore(t *testing.T) {
	t.Parallel()
	logintest.RunStore(t, func(*testing.T) login.Store { return login.NewMemoryStore(0) })
}

// A full memory store forgets the least recently seen session.
func TestMemoryStoreEvicts(t *testing.T) {
	t.Parallel()
	s := login.NewMemoryStore(2)
	ctx := t.Context()
	for i, k := range []string{"a", "b"} {
		at := start.Add(time.Duration(i) * time.Hour)
		if err := s.Create(ctx, k, login.Session{Username: k, Created: at, LastSeen: at}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Touch(ctx, "a", start.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, "c", login.Session{Username: "c", Created: start, LastSeen: start.Add(3 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "b"); !errors.Is(err, login.ErrNoSession) {
		t.Errorf("the least recently seen session was kept: %v", err)
	}
	for _, k := range []string{"a", "c"} {
		if _, err := s.Get(ctx, k); err != nil {
			t.Errorf("%s: %v", k, err)
		}
	}
}

// Sign-in sweeps expired sessions out of the store.
func TestExpiredSessionsAreSwept(t *testing.T) {
	t.Parallel()
	st := login.NewMemoryStore(0)
	r := newRig(t, login.WithStore(st))
	old := r.session("alice", alicePassword)
	r.clk.Advance(login.IdleTimeout + time.Minute)
	r.session("bob", bobPassword)
	sum := sha256Hex(old.Value)
	if _, err := st.Get(t.Context(), sum); !errors.Is(err, login.ErrNoSession) {
		t.Errorf("an expired session is still stored: %v", err)
	}
}
