package auth_test

import (
	"bytes"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/auth/authtest"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// Keys used across the tests. They look random so that a 12-byte window of
// one cannot turn up in a log line by accident.
const (
	acmeKey   = "Kq7Zt2Vb9Xw4Lp8Rm3Ns6HdYc"
	acmeUser  = "Fj5Gh8Tn2Qw6Ez9Uc4Rv7BmXs"
	globexKey = "Wp3Ld8Ya5Hk2Nv7Cq9Tf4JxMb"
)

func tenant(t testing.TB, id string) tenancy.Tenant {
	t.Helper()
	tn, err := tenancy.NewTenant(id)
	if err != nil {
		t.Fatal(err)
	}
	return tn
}

// testEntries are the API keys most tests configure: two for acme (a service
// key and a user's key) and one for globex.
func testEntries(t testing.TB) []auth.APIKey {
	t.Helper()
	var out []auth.APIKey
	for _, e := range []struct {
		key string
		e   auth.APIKey
	}{
		{acmeKey, auth.APIKey{ID: "acme-ci", Tenant: "acme", Scopes: []string{"agent:run", "agent:read"}, Toolsets: []string{"crm-*", "search"}}},
		{acmeUser, auth.APIKey{ID: "acme-ana", Tenant: "acme", User: "ana@acme.example", Scopes: []string{"*"}, Toolsets: []string{"*"}}},
		{globexKey, auth.APIKey{ID: "globex-ops", Tenant: "globex", Scopes: []string{"admin:*"}, Toolsets: []string{"gmail"}}},
	} {
		got, err := auth.KeyEntry(e.e, e.key)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, got)
	}
	return out
}

// testKeys returns the authenticator of testEntries. It then scribbles on the
// entries it was given, so every test also checks that the configuration was
// copied: the principals must still be those of fresh testEntries.
func testKeys(t testing.TB) *auth.APIKeys {
	t.Helper()
	entries := testEntries(t)
	k, err := auth.NewAPIKeys(entries)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		for i := range e.Scopes {
			e.Scopes[i] = "scribbled"
		}
		for i := range e.Toolsets {
			e.Toolsets[i] = "scribbled"
		}
	}
	return k
}

// principalOf is the principal entry e grants.
func principalOf(t testing.TB, e auth.APIKey) auth.Principal {
	return auth.Principal{Tenant: tenant(t, e.Tenant), User: e.User, Scopes: e.Scopes, Toolsets: e.Toolsets, Via: auth.Credential{Kind: auth.KindAPIKey, ID: e.ID}}
}

// syncBuffer is a log sink for handlers that run concurrently.
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

// seen is what the handler behind the middleware saw.
type seen struct {
	principal auth.Principal
	ok        bool
	tenant    tenancy.Tenant
	user      string
	userOK    bool
	requestID string
	header    http.Header
}

// result is one response.
type result struct {
	status int
	header http.Header
	body   string
	called bool // whether the handler behind the middleware ran
	seen   seen
}

// rig is the middleware in front of a handler that records and logs
// everything it sees. Every response is checked against the invariants in
// check, and when the test ends no log line, response header or body, or
// header the handler saw, contains a secret.
type rig struct {
	t       testing.TB
	h       http.Handler
	log     *syncBuffer
	secrets []string

	mu      sync.Mutex
	calls   int
	seen    map[string]seen // by the rigCall header
	outputs []string        // response headers and bodies, headers the handler saw
}

// rigCall numbers each request so the handler's view can be found again.
const rigCall = "X-Rig-Call"

func newRig(t testing.TB, a auth.Authenticator, secrets []string, opts ...auth.Option) *rig {
	t.Helper()
	r := &rig{t: t, log: &syncBuffer{}, secrets: secrets, seen: map[string]seen{}}
	log := slog.New(slog.NewTextHandler(r.log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	next := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var s seen
		s.principal, s.ok = auth.From(req.Context())
		s.tenant, _ = tenancy.From(req.Context())
		s.user, s.userOK = tenancy.User(req.Context())
		s.requestID, _ = tenancy.Request(req.Context())
		s.header = req.Header.Clone()
		// A careless handler: it logs and echoes everything it was given.
		log.Info("handler", "headers", fmt.Sprint(req.Header), "principal", fmt.Sprintf("%+v", s.principal), "url", req.URL.String())
		r.mu.Lock()
		r.seen[req.Header.Get(rigCall)] = s
		r.outputs = append(r.outputs, fmt.Sprint(req.Header))
		r.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "%+v", s)
	})
	r.h = auth.Middleware(a, append([]auth.Option{auth.WithLogger(log)}, opts...)...)(next)
	t.Cleanup(r.checkLeaks)
	return r
}

// get sends a GET with the given Authorization header values (none if nil)
// and other headers, as name, value pairs.
func (r *rig) get(authz []string, headers ...string) result {
	r.t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/runs?x=1", nil)
	for _, v := range authz {
		req.Header.Add("Authorization", v)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Add(headers[i], headers[i+1])
	}
	return r.do(req)
}

func (r *rig) do(req *http.Request) result {
	r.t.Helper()
	r.mu.Lock()
	r.calls++
	call := strconv.Itoa(r.calls)
	r.mu.Unlock()
	req.Header.Set(rigCall, call)
	sentID := req.Header.Get(auth.RequestIDHeader)
	sent := req.Header.Clone()

	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)

	r.mu.Lock()
	s, called := r.seen[call]
	r.outputs = append(r.outputs, fmt.Sprint(rec.Header()), rec.Body.String())
	r.mu.Unlock()
	res := result{status: rec.Code, header: rec.Header(), body: rec.Body.String(), called: called, seen: s}
	if !maps.EqualFunc(req.Header, sent, slices.Equal) {
		r.t.Errorf("the middleware changed the caller's request headers: %v, was %v", req.Header, sent)
	}
	r.check(res, sentID)
	return res
}

// check holds for every response, whatever the request.
func (r *rig) check(res result, sentID string) {
	r.t.Helper()
	id := res.header.Get(auth.RequestIDHeader)
	if id == "" || len(id) > 128 {
		r.t.Errorf("response request id %q: want 1 to 128 bytes", id)
	}
	if sentID != "" && sentID != id && validRequestID(sentID) {
		r.t.Errorf("a valid client request id %q was replaced by %q", sentID, id)
	}
	if !res.called {
		switch res.status {
		case http.StatusUnauthorized:
			if c := res.header.Get("WWW-Authenticate"); !strings.HasPrefix(c, "Bearer realm=") {
				r.t.Errorf("401 with WWW-Authenticate %q, want a Bearer challenge", c)
			}
		case http.StatusInternalServerError:
			if c := res.header.Get("WWW-Authenticate"); c != "" {
				r.t.Errorf("500 with WWW-Authenticate %q", c)
			}
		default:
			r.t.Errorf("the handler was not called and the status is %d, want 401 or 500", res.status)
		}
		return
	}
	if res.status != http.StatusOK {
		r.t.Errorf("the handler was called and the status is %d", res.status)
	}
	s := res.seen
	if !s.ok || s.principal.Tenant.ID() == "" {
		r.t.Errorf("the handler was called with no principal, or one with no tenant: %+v", s.principal)
	}
	if s.tenant != s.principal.Tenant {
		r.t.Errorf("tenancy.From = %q, the principal's tenant is %q", s.tenant.ID(), s.principal.Tenant.ID())
	}
	if s.userOK != (s.principal.User != "") || s.user != s.principal.User {
		r.t.Errorf("tenancy.User = %q, %v; the principal's user is %q", s.user, s.userOK, s.principal.User)
	}
	if s.requestID != id {
		r.t.Errorf("tenancy.Request = %q, the response says %q", s.requestID, id)
	}
	if v := s.header.Values("Authorization"); len(v) > 0 {
		r.t.Errorf("the handler saw the Authorization header")
	}
}

func validRequestID(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, c := range s {
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !alnum && !strings.ContainsRune("._:-", c) {
			return false
		}
	}
	return true
}

func (r *rig) checkLeaks() {
	r.t.Helper()
	r.mu.Lock()
	outputs := append([]string{r.log.String()}, r.outputs...)
	r.mu.Unlock()
	for _, secret := range r.secrets {
		for _, o := range outputs {
			if authtest.Leaks(o, secret) {
				r.t.Errorf("a secret (or its hash) appears in a log line, response or handler input:\n%s", o)
			}
		}
	}
}
