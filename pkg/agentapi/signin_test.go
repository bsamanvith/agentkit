package agentapi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/auth/authtest"
	"github.com/bsamanvith/agentkit/pkg/auth/login"
	"github.com/bsamanvith/agentkit/pkg/auth/login/logintest"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/mcp/servers"
	"github.com/bsamanvith/agentkit/pkg/netguard"
	"github.com/bsamanvith/agentkit/pkg/store/scoped"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// The sign-in rig's users' passwords, and its key. Each is a canary: no
// response or log line may hold one.
const (
	userPassword  = "user-CANARY-pw-6a1f3c9e"
	adminPassword = "admin-CANARY-pw-0d8b7e24"
	signinKey     = "key-signin-rig-0123456789"
	systemPrompt  = "SYSTEM-PROMPT-for-admins-only"
)

// signinRig is an agent API with password sign-in (users "ann", a user, and
// "root", an admin, of one tenant), a bearer key of that tenant with every
// scope, and the toolset admin. Its requests send no credentials unless a
// test adds them. When the test ends it checks that no response it saw, and
// no log line, holds a password or a session id.
type signinRig struct {
	t      *testing.T
	ts     *httptest.Server
	clk    *clocktest.Fake
	hasher *logintest.Hasher
	logs   *syncBuffer
	seen   bytes.Buffer // every response's headers and body
	ids    []string     // session ids handed out
}

func newSigninRig(t *testing.T) *signinRig {
	t.Helper()
	r := &signinRig{t: t, clk: clocktest.NewFake(time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)), hasher: &logintest.Hasher{}, logs: &syncBuffer{}}
	ten, err := tenancy.NewTenant(fmt.Sprintf("s%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewJSONHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	l, err := login.New([]login.User{
		{Username: "ann", PasswordHash: logintest.PHC(r.hasher.Hash, userPassword, login.DefaultParams), Tenant: ten.ID(), Roles: []string{login.RoleUser}},
		{Username: "root", PasswordHash: logintest.PHC(r.hasher.Hash, adminPassword, login.DefaultParams), Tenant: ten.ID(), Roles: []string{login.RoleAdmin}},
	}, login.WithHasher(r.hasher.Hash), login.WithClock(r.clk), login.WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	keys := authtest.NewFake(map[string]auth.Principal{signinKey: {Tenant: ten, Scopes: []string{"*"}, Toolsets: []string{"*"}, Via: auth.Credential{Kind: auth.KindAPIKey, ID: "k"}}})
	st := scoped.Memory(nil)
	g, err := netguard.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := servers.New(st, servers.WithGuard(g))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	reg, err := tool.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	h, err := harness.New(echo(t), reg)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := agentapi.NewAgents([]agentapi.Agent{{Name: "helper", System: systemPrompt, Harness: h}},
		agentapi.WithStore(st), agentapi.WithKeepAlive(0), agentapi.WithAuthenticator(keys), agentapi.WithLogin(l),
		agentapi.WithToolsetAdmin(mgr), agentapi.WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/v1/", srv.Handler())
	r.ts = httptest.NewServer(mux)
	t.Cleanup(r.ts.Close)
	t.Cleanup(func() { _ = srv.Shutdown(t.Context()) })
	t.Cleanup(r.check)
	return r
}

func (r *signinRig) check() {
	logs, seen := r.logs.String(), r.seen.String()
	for _, s := range append([]string{userPassword, adminPassword, signinKey}, r.ids...) {
		if authtest.Leaks(logs, s) {
			r.t.Errorf("a secret is in the logs:\n%s", logs)
		}
	}
	for _, s := range []string{userPassword, adminPassword, signinKey} {
		if authtest.Leaks(seen, s) {
			r.t.Errorf("a secret is in a response:\n%s", seen)
		}
	}
}

// do sends a request with the given headers ("Name", "value", ...) and
// returns the response, its body read.
func (r *signinRig) do(method, path, body string, header ...string) (*http.Response, []byte) {
	r.t.Helper()
	req, err := http.NewRequestWithContext(r.t.Context(), method, r.ts.URL+path, strings.NewReader(body))
	if err != nil {
		r.t.Fatal(err)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Add(header[i], header[i+1])
	}
	resp, err := r.ts.Client().Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		r.t.Fatal(err)
	}
	fmt.Fprintf(&r.seen, "%v\n%s\n", resp.Header, b)
	return resp, b
}

// session is a signed-in user's cookie and CSRF token.
type session struct {
	cookie *http.Cookie
	csrf   string
}

// headers are a session's, for a request that also sends the Origin and
// token (full) or only the cookie.
func (s session) headers(r *signinRig, full bool) []string {
	h := []string{"Cookie", s.cookie.Name + "=" + s.cookie.Value}
	if full {
		h = append(h, "Origin", r.ts.URL, login.CSRFHeader, s.csrf)
	}
	return h
}

func (r *signinRig) signIn(user, password string) session {
	r.t.Helper()
	resp, body := r.do("POST", "/v1/auth/login", fmt.Sprintf(`{"username":%q,"password":%q}`, user, password), "Origin", r.ts.URL)
	cs := resp.Cookies()
	if resp.StatusCode != http.StatusNoContent || len(cs) != 1 || len(body) != 0 {
		r.t.Fatalf("signing in %s: %d %s %v", user, resp.StatusCode, body, cs)
	}
	r.ids = append(r.ids, cs[0].Value)
	s := session{cookie: cs[0]}
	me := r.me(s.headers(r, false)...)
	s.csrf = me.CSRF
	return s
}

func (r *signinRig) me(header ...string) wire.Me {
	r.t.Helper()
	resp, body := r.do("GET", "/v1/auth/me", "", header...)
	var m wire.Me
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &m) != nil {
		r.t.Fatalf("GET /v1/auth/me: %d %s", resp.StatusCode, body)
	}
	return m
}

// Login and logout over HTTP: GET /v1/auth/config offers both methods; the
// right password sets the cookie and /me names the user; a wrong password and
// an unknown user get the same 401; logout ends the session on the server.
func TestSignIn(t *testing.T) {
	t.Parallel()
	r := newSigninRig(t)
	resp, body := r.do("GET", "/v1/auth/config", "")
	var cfg wire.AuthConfig
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &cfg) != nil || !slices.Equal(cfg.Methods, []string{"password", "token"}) {
		t.Errorf("GET /v1/auth/config: %d %s", resp.StatusCode, body)
	}

	s := r.signIn("ann", userPassword)
	c := s.cookie
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Secure || c.Name != login.CookieName {
		t.Errorf("the cookie over plain HTTP: %s", c)
	}
	me := r.me(s.headers(r, false)...)
	if me.Username != "ann" || !slices.Equal(me.Roles, []string{"user"}) || me.Credential != login.KindSession || me.CSRF == "" ||
		!slices.Equal(me.Scopes, []string{"agent:run", "agent:read"}) || me.Tenant == "" {
		t.Errorf("/me: %+v", me)
	}
	if bm := r.me("Authorization", "Bearer "+signinKey); bm.Credential != auth.KindAPIKey || bm.CSRF != "" || len(bm.Roles) != 0 || bm.Tenant != me.Tenant {
		t.Errorf("/me with a key: %+v", bm)
	}

	wrong, wrongBody := r.do("POST", "/v1/auth/login", `{"username":"ann","password":"not-the-password"}`)
	unknown, unknownBody := r.do("POST", "/v1/auth/login", `{"username":"nobody","password":"not-the-password"}`)
	if wrong.StatusCode != http.StatusUnauthorized || !bytes.Equal(wrongBody, unknownBody) || unknown.StatusCode != wrong.StatusCode ||
		apiCode(wrongBody) != agentapi.CodeUnauthorized || len(wrong.Cookies())+len(unknown.Cookies()) != 0 {
		t.Errorf("a wrong password: %d %s; an unknown user: %d %s; want the same 401", wrong.StatusCode, wrongBody, unknown.StatusCode, unknownBody)
	}
	for _, bad := range []string{`{"username":"ann"}`, `{"username":"","password":"x"}`, `{"username":"ann","password":"x","extra":1}`, `not json`, `{"username":"ann","password":"` + strings.Repeat("x", 5000) + `"}`} {
		if resp, body := r.do("POST", "/v1/auth/login", bad); resp.StatusCode != http.StatusBadRequest || apiCode(body) != agentapi.CodeInvalidRequest {
			t.Errorf("sign-in with %.40q: %d %s, want 400", bad, resp.StatusCode, body)
		}
	}

	// Logout needs the CSRF check, like any unsafe request.
	if resp, body := r.do("POST", "/v1/auth/logout", "", s.headers(r, false)...); resp.StatusCode != http.StatusForbidden || apiCode(body) != agentapi.CodeCSRF {
		t.Errorf("logout without the CSRF token: %d %s, want 403 %s", resp.StatusCode, body, agentapi.CodeCSRF)
	}
	resp, body = r.do("POST", "/v1/auth/logout", "", s.headers(r, true)...)
	cs := resp.Cookies()
	if resp.StatusCode != http.StatusNoContent || len(cs) != 1 || cs[0].Name != c.Name || cs[0].MaxAge >= 0 {
		t.Errorf("logout: %d %s %v, want 204 clearing the cookie", resp.StatusCode, body, cs)
	}
	// The old cookie no longer works anywhere.
	for _, p := range []string{"/v1/auth/me", "/v1/agents"} {
		if resp, body := r.do("GET", p, "", s.headers(r, false)...); resp.StatusCode != http.StatusUnauthorized || apiCode(body) != agentapi.CodeUnauthorized {
			t.Errorf("GET %s after logout: %d %s, want 401", p, resp.StatusCode, body)
		}
	}
}

// CSRF: a cookie-authenticated POST without the CSRF header, or from a
// foreign Origin, is 403; with both it is served; a bearer POST needs
// neither, even with a cookie beside it.
func TestCSRF(t *testing.T) {
	t.Parallel()
	r := newSigninRig(t)
	s := r.signIn("ann", userPassword)
	cookie := s.headers(r, false)
	for name, h := range map[string][]string{
		"no header, no Origin":    cookie,
		"no header":               append(slices.Clone(cookie), "Origin", r.ts.URL),
		"no Origin":               append(slices.Clone(cookie), login.CSRFHeader, s.csrf),
		"a foreign Origin":        append(slices.Clone(cookie), "Origin", "https://evil.example", login.CSRFHeader, s.csrf),
		"a wrong token":           append(slices.Clone(cookie), "Origin", r.ts.URL, login.CSRFHeader, s.csrf+"x"),
		"the session id as token": append(slices.Clone(cookie), "Origin", r.ts.URL, login.CSRFHeader, s.cookie.Value),
	} {
		for _, rt := range []string{"POST /v1/conversations", "DELETE /v1/conversations/x", "POST /v1/runs/x/stop"} {
			method, path, _ := strings.Cut(rt, " ")
			if resp, body := r.do(method, path, `{}`, h...); resp.StatusCode != http.StatusForbidden || apiCode(body) != agentapi.CodeCSRF {
				t.Errorf("%s, %s: %d %s, want 403 %s", rt, name, resp.StatusCode, body, agentapi.CodeCSRF)
			}
		}
	}
	if resp, body := r.do("POST", "/v1/conversations", `{}`, s.headers(r, true)...); resp.StatusCode != http.StatusCreated {
		t.Errorf("a POST with the Origin and token: %d %s, want 201", resp.StatusCode, body)
	}
	if resp, body := r.do("GET", "/v1/conversations", "", cookie...); resp.StatusCode != http.StatusOK {
		t.Errorf("a GET with only the cookie: %d %s, want 200", resp.StatusCode, body)
	}
	// Bearer requests are unaffected: no Origin, no token, a foreign Origin,
	// or a session's cookie beside the key (the key decides).
	for name, h := range map[string][]string{
		"bearer":                    {"Authorization", "Bearer " + signinKey},
		"bearer, foreign Origin":    {"Authorization", "Bearer " + signinKey, "Origin", "https://evil.example"},
		"bearer and a session":      append([]string{"Authorization", "Bearer " + signinKey}, cookie...),
		"bearer and a stale cookie": {"Authorization", "Bearer " + signinKey, "Cookie", login.CookieName + "=" + strings.Repeat("A", 43)},
	} {
		if resp, body := r.do("POST", "/v1/conversations", `{}`, h...); resp.StatusCode != http.StatusCreated {
			t.Errorf("%s: %d %s, want 201", name, resp.StatusCode, body)
		}
	}
}

// Roles: a user gets no system prompt and 403 on the admin endpoints; an
// admin gets both.
func TestRoles(t *testing.T) {
	t.Parallel()
	r := newSigninRig(t)
	user, admin := r.signIn("ann", userPassword), r.signIn("root", adminPassword)
	agent := func(s session) wire.Agent {
		resp, body := r.do("GET", "/v1/agents/helper", "", s.headers(r, false)...)
		var a wire.Agent
		if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &a) != nil {
			t.Fatalf("GET /v1/agents/helper: %d %s", resp.StatusCode, body)
		}
		return a
	}
	if a := agent(user); a.System != "" {
		t.Errorf("a user sees the system prompt %q", a.System)
	}
	if a := agent(admin); a.System != systemPrompt {
		t.Errorf("an admin sees the system prompt %q, want %q", a.System, systemPrompt)
	}
	if me := r.me(admin.headers(r, false)...); !slices.Equal(me.Roles, []string{"admin"}) {
		t.Errorf("the admin's /me: %+v", me)
	}
	add := `{"type":"mcp","name":"linear","url":"https://10.0.0.1/mcp"}`
	for _, rt := range []string{"POST /v1/toolsets", "GET /v1/toolsets/linear/review", "PATCH /v1/toolsets/linear", "DELETE /v1/toolsets/linear"} {
		method, path, _ := strings.Cut(rt, " ")
		resp, body := r.do(method, path, add, user.headers(r, true)...)
		if resp.StatusCode != http.StatusForbidden || apiCode(body) != agentapi.CodeForbidden {
			t.Errorf("a user: %s = %d %s, want 403 forbidden", rt, resp.StatusCode, body)
		}
		// The admin passes the scope check: the toolset admin answers (the
		// address is refused, or there is no such toolset).
		if resp, body := r.do(method, path, add, admin.headers(r, true)...); resp.StatusCode == http.StatusForbidden {
			t.Errorf("an admin: %s = %d %s, want it served", rt, resp.StatusCode, body)
		}
	}
	// Both may run agents and read their conversations.
	for _, s := range []session{user, admin} {
		if resp, body := r.do("POST", "/v1/conversations", `{}`, s.headers(r, true)...); resp.StatusCode != http.StatusCreated {
			t.Errorf("POST /v1/conversations: %d %s", resp.StatusCode, body)
		}
	}
}

// Throttling over HTTP: 429 with Retry-After after 10 failures, the same for
// an unknown user, and not for another user.
func TestSignInThrottled(t *testing.T) {
	t.Parallel()
	r := newSigninRig(t)
	for range 10 {
		r.do("POST", "/v1/auth/login", `{"username":"ann","password":"wrong-password"}`)
	}
	resp, body := r.do("POST", "/v1/auth/login", fmt.Sprintf(`{"username":"ann","password":%q}`, userPassword))
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") != "1" || apiCode(body) != agentapi.CodeTooManyAttempts {
		t.Errorf("after 10 failures: %d %v %s, want 429 with Retry-After 1", resp.StatusCode, resp.Header, body)
	}
	r.signIn("root", adminPassword)
	r.clk.Advance(time.Second)
	r.signIn("ann", userPassword)
}

// FuzzLogin sends arbitrary bodies to POST /v1/auth/login: the server never
// panics, never sets a cookie or authenticates, and answers one of the
// documented statuses in the API's error format.
func FuzzLogin(f *testing.F) {
	for _, seed := range []string{
		`{"username":"ann","password":"wrong"}`,
		`{"username":"nobody","password":"x"}`,
		`{"username":"ann","password":""}`,
		`{"username":"ann"}`,
		`{"username":"ann","password":"x","role":"admin"}`,
		`{"username":"ann","password":"x"}{}`,
		`{"username":["ann"],"password":"x"}`,
		`{"username":"ann\u0000","password":"x"}`,
		`{"username":"` + strings.Repeat("a", 5000) + `","password":"x"}`,
		`null`, `[]`, ``, `{`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		r := newSigninRig(t)
		resp, got := r.do("POST", "/v1/auth/login", string(body))
		if !slices.Contains([]int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusTooManyRequests}, resp.StatusCode) || apiCode(got) == "" {
			t.Fatalf("%q: %d %s", body, resp.StatusCode, got)
		}
		if len(resp.Cookies()) != 0 {
			t.Fatalf("%q: a cookie was set", body)
		}
	})
}
