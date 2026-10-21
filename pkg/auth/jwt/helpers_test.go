package jwt_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/auth/authtest"
	"github.com/bsamanvith/agentkit/pkg/auth/jwt"
	"github.com/bsamanvith/agentkit/pkg/auth/jwt/jwttest"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// audience is the resource URL every rig's authenticator has.
const audience = "https://agents.example.com"

// t0 is when every rig's clock starts.
var t0 = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// rig is an authenticator in front of a handler, with a fake identity
// provider (one ES256 key, kid in rig.kid) and a fake clock. When the test
// ends it checks that no token it signed appears in a log line.
type rig struct {
	t      testing.TB
	iss    *jwttest.Issuer
	clk    *clocktest.Fake
	a      *jwt.Authenticator
	kid    string
	h      http.Handler
	logs   *syncBuffer
	mu     sync.Mutex
	tokens []string
	last   auth.Principal // what the handler last got
}

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

// newRig returns a rig whose configuration is the default one changed by
// mods.
func newRig(t testing.TB, mods ...func(*jwt.Config)) *rig {
	t.Helper()
	iss := jwttest.NewIssuer(t)
	r := &rig{t: t, iss: iss, clk: clocktest.NewFake(t0), kid: iss.AddKey(t, "ES256"), logs: &syncBuffer{}}
	cfg := jwt.Config{Issuer: iss.URL(), Audience: audience, JWKSURL: iss.JWKSURL()}
	for _, m := range mods {
		m(&cfg)
	}
	log := slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	a, err := jwt.New(cfg, jwt.WithClock(r.clk), jwt.WithHTTPClient(iss.Client()), jwt.WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	r.a = a
	r.h = auth.Middleware(a, auth.WithLogger(log))(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		p, ok := auth.From(req.Context())
		tn, _ := tenancy.From(req.Context())
		if !ok || tn != p.Tenant || req.Header.Get("Authorization") != "" {
			t.Errorf("the handler got principal %+v (%v), tenant %v, Authorization %q", p, ok, tn, req.Header.Get("Authorization"))
		}
		r.mu.Lock()
		r.last = p
		r.mu.Unlock()
	}))
	t.Cleanup(func() {
		logs := r.logs.String()
		r.mu.Lock()
		defer r.mu.Unlock()
		for _, tok := range r.tokens {
			if authtest.Leaks(logs, tok) {
				t.Errorf("a log line holds (part of) a token")
			}
		}
	})
	return r
}

// claims are valid claims for the rig at its clock's time, changed by over:
// a nil value removes the claim.
func (r *rig) claims(over map[string]any) map[string]any {
	now := r.clk.Now().Unix()
	c := map[string]any{
		"iss": r.iss.URL(), "aud": audience, "sub": "ann",
		"tenant": "acme", "scope": "agent:run agent:read mcp:crm",
		"iat": now, "nbf": now, "exp": now + 3600,
	}
	for k, v := range over {
		if v == nil {
			delete(c, k)
		} else {
			c[k] = v
		}
	}
	return c
}

// track records a token, for the leak check.
func (r *rig) track(tok string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokens = append(r.tokens, tok)
	return tok
}

// token is a token of the rig's key with claims(over).
func (r *rig) token(over map[string]any) string {
	return r.track(r.iss.Sign(r.t, r.kid, r.claims(over)))
}

// result is what a request through the middleware got.
type result struct {
	status    int
	challenge string
	p         auth.Principal
}

// do sends tok as a bearer token to path through the middleware.
func (r *rig) do(tok, path string) result {
	r.t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	w := httptest.NewRecorder()
	r.h.ServeHTTP(w, req)
	res := result{status: w.Code, challenge: w.Header().Get("WWW-Authenticate")}
	if w.Code == http.StatusOK {
		r.mu.Lock()
		res.p = r.last
		r.mu.Unlock()
	} else if strings.Contains(w.Body.String(), tok) && tok != "" {
		r.t.Errorf("the response body holds the token")
	}
	return res
}

// wantInvalid fails the test unless res is a 401 for an invalid token.
func wantInvalid(t *testing.T, name string, res result) {
	t.Helper()
	if res.status != http.StatusUnauthorized || !strings.Contains(res.challenge, `error="invalid_token"`) {
		t.Errorf("%s: got %d %q, want 401 with error=\"invalid_token\"", name, res.status, res.challenge)
	}
}

func mustTenant(t testing.TB, id string) tenancy.Tenant {
	t.Helper()
	tn, err := tenancy.NewTenant(id)
	if err != nil {
		t.Fatal(err)
	}
	return tn
}

// hs256 returns a token with header and claims, HMAC-SHA256 signed with key.
func hs256(header, claims map[string]any, key []byte) string {
	unsigned := jwttest.Compact(header, claims, nil)
	input := strings.TrimSuffix(unsigned, ".")
	m := hmac.New(sha256.New, key)
	m.Write([]byte(input))
	return input + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func with(m map[string]any, kv ...any) map[string]any {
	out := maps.Clone(m)
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i].(string)] = kv[i+1]
	}
	return out
}
