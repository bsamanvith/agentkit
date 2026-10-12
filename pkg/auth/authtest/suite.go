package authtest

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/auth"
)

// Subject is an authenticator under test and credentials it accepts.
type Subject struct {
	Authenticator auth.Authenticator
	// Credentials are at least two valid credentials, for at least two
	// tenants, with distinct secrets.
	Credentials []Credential
}

// Credential is one valid credential of a [Subject].
type Credential struct {
	Name string
	// Secret is the credential's secret value: a bearer token, a session id.
	// The suite presents altered copies of it, and checks that no error
	// contains it. At least 4 bytes.
	Secret string
	// Apply puts a credential with the given secret on a request (see
	// [Bearer]). The suite calls it with Secret and with altered copies.
	Apply func(r *http.Request, secret string)
	// Want is the principal the credential gives.
	Want auth.Principal
}

// spoofHeaders are headers a client might send to pick a tenant or user. An
// authenticator never reads them.
var spoofHeaders = []string{
	"X-Tenant", "X-Tenant-Id", "X-Agentkit-Tenant", "Tenant",
	"X-User", "X-User-Id", "X-Agentkit-User", "X-Forwarded-User", "X-Remote-User", "Remote-User", "X-Auth-Request-User",
}

// RunAuthenticator checks that an authenticator behaves as auth.Authenticator
// documents: each credential gives exactly its principal; no credential gives
// auth.ErrNoCredentials; an altered secret or a hostile Authorization header
// never authenticates; client headers naming a tenant or user change nothing;
// principals share no memory; the request is not changed; errors never
// contain a secret; concurrent calls are safe. newSubject is called once per
// subtest.
func RunAuthenticator(t *testing.T, newSubject func(t *testing.T) Subject) {
	t.Helper()
	sub := func(name string, f func(t *testing.T, s Subject)) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := newSubject(t)
			checkSubject(t, s)
			f(t, s)
		})
	}

	sub("ValidCredentialGivesItsPrincipal", func(t *testing.T, s Subject) {
		for _, c := range s.Credentials {
			r := request()
			c.Apply(r, c.Secret)
			if p, err := call(t, s, r); err != nil || !Equal(p, c.Want) {
				t.Errorf("%s: got %+v, %v; want %+v", c.Name, p, err, c.Want)
			}
		}
	})

	sub("NoCredentials", func(t *testing.T, s Subject) {
		r := request()
		for _, h := range spoofHeaders {
			r.Header.Set(h, s.Credentials[0].Want.Tenant.ID())
		}
		if p, err := call(t, s, r); !errors.Is(err, auth.ErrNoCredentials) {
			t.Errorf("a request with no credentials: got %+v, %v; want ErrNoCredentials", p, err)
		}
	})

	sub("WrongSecretIsInvalid", func(t *testing.T, s Subject) {
		for _, c := range s.Credentials {
			for _, wrong := range flips(c.Secret) {
				r := request()
				c.Apply(r, wrong)
				if p, err := call(t, s, r); !errors.Is(err, auth.ErrInvalidCredentials) {
					t.Errorf("%s with one byte changed (%q): got %+v, %v; want ErrInvalidCredentials", c.Name, diffAt(c.Secret, wrong), p, err)
				}
			}
		}
	})

	sub("AlteredSecretNeverAuthenticates", func(t *testing.T, s Subject) {
		for _, c := range s.Credentials {
			for name, altered := range alterations(c.Secret) {
				r := request()
				c.Apply(r, altered)
				if p, err := call(t, s, r); err == nil {
					t.Errorf("%s, %s: authenticated as %+v", c.Name, name, p)
				}
			}
		}
	})

	sub("TenantHeadersAreIgnored", func(t *testing.T, s Subject) {
		for i, c := range s.Credentials {
			other := s.Credentials[(i+1)%len(s.Credentials)].Want
			r := request()
			c.Apply(r, c.Secret)
			for _, h := range spoofHeaders {
				r.Header.Set(h, other.Tenant.ID())
				if strings.Contains(strings.ToLower(h), "user") && other.User != "" {
					r.Header.Set(h, other.User)
				}
			}
			if p, err := call(t, s, r); err != nil || !Equal(p, c.Want) {
				t.Errorf("%s with headers naming tenant %q: got %+v, %v; want %+v", c.Name, other.Tenant.ID(), p, err, c.Want)
			}
		}
	})

	sub("PrincipalsDoNotShareMemory", func(t *testing.T, s Subject) {
		for _, c := range s.Credentials {
			for range 2 {
				r := request()
				c.Apply(r, c.Secret)
				p, err := call(t, s, r)
				if err != nil || !Equal(p, c.Want) {
					t.Fatalf("%s: got %+v, %v; want %+v (a caller changed an earlier principal?)", c.Name, p, err, c.Want)
				}
				for i := range p.Scopes {
					p.Scopes[i] = "scribbled"
				}
				for i := range p.Toolsets {
					p.Toolsets[i] = "scribbled"
				}
			}
		}
	})

	sub("RequestIsNotChanged", func(t *testing.T, s Subject) {
		for _, c := range s.Credentials {
			r := request()
			c.Apply(r, c.Secret)
			before := r.Clone(r.Context())
			_, _ = call(t, s, r)
			if r.Method != before.Method || r.URL.String() != before.URL.String() || !maps.EqualFunc(r.Header, before.Header, slices.Equal) {
				t.Errorf("%s: Authenticate changed the request: %v %v %v, was %v %v %v", c.Name, r.Method, r.URL, r.Header, before.Method, before.URL, before.Header)
			}
		}
	})

	sub("HostileHeadersNeverAuthenticate", func(t *testing.T, s Subject) {
		for _, c := range s.Credentials {
			for name, values := range hostile(c.Secret) {
				r := request()
				for _, v := range values {
					r.Header.Add("Authorization", v)
				}
				if p, err := call(t, s, r); err == nil {
					t.Errorf("%s, %s: authenticated as %+v", c.Name, name, p)
				}
			}
		}
	})

	sub("ConcurrentCalls", func(t *testing.T, s Subject) {
		var wg sync.WaitGroup
		for range 8 {
			for _, c := range s.Credentials {
				wg.Go(func() {
					r := request()
					c.Apply(r, c.Secret)
					if p, err := s.Authenticator.Authenticate(r); err != nil || !Equal(p, c.Want) {
						t.Errorf("%s, concurrently: got %+v, %v; want %+v", c.Name, p, err, c.Want)
					}
				})
			}
		}
		wg.Wait()
	})
}

func request() *http.Request { return httptest.NewRequest(http.MethodGet, "/v1/runs", nil) }

// checkSubject fails the test if s cannot show what the suite checks.
func checkSubject(t *testing.T, s Subject) {
	t.Helper()
	tenants, secrets := map[string]bool{}, map[string]bool{}
	for _, c := range s.Credentials {
		if c.Want.Tenant.ID() == "" || len(c.Secret) < 4 || c.Apply == nil || secrets[c.Secret] {
			t.Fatalf("credential %s: needs a tenant, Apply and a distinct secret of at least 4 bytes", c.Name)
		}
		tenants[c.Want.Tenant.ID()], secrets[c.Secret] = true, true
	}
	if s.Authenticator == nil || len(tenants) < 2 {
		t.Fatalf("a subject needs an authenticator and credentials for at least two tenants")
	}
}

// call authenticates r and checks what holds for every call: an error is one
// of the two documented classes, a principal has a tenant, and no error
// contains a secret of s.
func call(t *testing.T, s Subject, r *http.Request) (auth.Principal, error) {
	t.Helper()
	p, err := s.Authenticator.Authenticate(r)
	if err != nil {
		if !errors.Is(err, auth.ErrNoCredentials) && !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Errorf("Authenticate returned an internal error for a client mistake: %v", err)
		}
		for _, c := range s.Credentials {
			if Leaks(err.Error(), c.Secret) {
				t.Errorf("an error contains (part of) the secret of %s", c.Name)
			}
		}
		return p, err
	}
	if p.Tenant.ID() == "" {
		t.Errorf("Authenticate returned a principal with no tenant: %+v", p)
	}
	return p, nil
}

// Leaks reports whether s contains secret, 12 bytes of it in a row, or its
// SHA-256 in hex: what a log line, error or response must never have.
func Leaks(s, secret string) bool {
	sum := sha256.Sum256([]byte(secret))
	if strings.Contains(strings.ToLower(s), hex.EncodeToString(sum[:])) {
		return true
	}
	n := min(12, len(secret))
	for i := 0; i+n <= len(secret); i++ {
		if strings.Contains(s, secret[i:i+n]) {
			return true
		}
	}
	return false
}

// Equal reports whether two principals are the same.
func Equal(a, b auth.Principal) bool {
	return a.Tenant == b.Tenant && a.User == b.User && a.Via == b.Via &&
		slices.Equal(a.Scopes, b.Scopes) && slices.Equal(a.Toolsets, b.Toolsets)
}

const b64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

// flip changes one byte of a secret into another that stays valid in the
// same places (base64url digits move by 32, so the change is in the high
// bits a lenient base64 decoder never ignores).
func flip(c byte) byte {
	if i := strings.IndexByte(b64, c); i >= 0 {
		return b64[(i+32)%64]
	}
	return c ^ 1
}

// flips returns secret with its first, middle and last letter or digit
// changed: still well-formed, just wrong.
func flips(secret string) []string {
	var alnum []int
	for i := range len(secret) {
		if c := secret[i]; strings.IndexByte(b64[:62], c) >= 0 {
			alnum = append(alnum, i)
		}
	}
	if len(alnum) == 0 {
		alnum = []int{0}
	}
	var out []string
	for _, i := range []int{alnum[0], alnum[len(alnum)/2], alnum[len(alnum)-1]} {
		b := []byte(secret)
		b[i] = flip(b[i])
		if w := string(b); !slices.Contains(out, w) {
			out = append(out, w)
		}
	}
	return out
}

// alterations are copies of secret that are not it: prefixes, extensions,
// and the empty secret.
func alterations(secret string) map[string]string {
	out := map[string]string{
		"last byte dropped":  secret[:len(secret)-1],
		"first byte dropped": secret[1:],
		"half":               secret[:len(secret)/2],
		"byte appended":      secret + "A",
		"dot suffix":         secret + ".x",
		"dash suffix":        secret + "-x",
		"colon suffix":       secret + ":x",
		"doubled":            secret + secret,
		"case swapped":       swapCase(secret),
		"empty":              "",
	}
	maps.DeleteFunc(out, func(_, v string) bool { return v == secret })
	return out
}

// hostile are Authorization headers carrying the secret in forms that must
// not be accepted.
func hostile(secret string) map[string][]string {
	return map[string][]string{
		"two headers":           {"Bearer " + secret, "Bearer " + secret},
		"two tokens":            {"Bearer " + secret + " " + secret},
		"comma list":            {"Bearer " + secret + ", Bearer " + secret},
		"oversized":             {"Bearer " + secret + strings.Repeat("A", auth.MaxAuthorizationLength)},
		"basic":                 {"Basic " + base64.StdEncoding.EncodeToString([]byte("user:"+secret))},
		"basic raw":             {"Basic " + secret},
		"no scheme":             {secret},
		"other scheme":          {"Token " + secret},
		"tab separator":         {"Bearer\t" + secret},
		"scheme glued":          {"Bearer" + secret},
		"trailing NUL":          {"Bearer " + secret + "\x00"},
		"header injection":      {"Bearer " + secret + "\r\nX-Tenant: x"},
		"bearer only":           {"Bearer"},
		"bearer and space only": {"Bearer "},
		"empty":                 {""},
	}
}

func swapCase(s string) string {
	b := []byte(s)
	for i, c := range b {
		switch {
		case c >= 'a' && c <= 'z':
			b[i] = c - 'a' + 'A'
		case c >= 'A' && c <= 'Z':
			b[i] = c - 'A' + 'a'
		}
	}
	return string(b)
}

// diffAt describes where wrong differs from secret, without either value.
func diffAt(secret, wrong string) string {
	for i := range min(len(secret), len(wrong)) {
		if secret[i] != wrong[i] {
			return "byte " + strconv.Itoa(i)
		}
	}
	return "length"
}
