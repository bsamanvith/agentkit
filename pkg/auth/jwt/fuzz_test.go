package jwt_test

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/auth"
)

// FuzzJWT sends hostile tokens: a whole token as given, and one built from a
// header, claims and signature as given (base64url-encoded here, so the
// fuzzer reaches the JSON). None may authenticate, panic, or be an internal
// error. The authenticator knows a real key, so a header naming it gets as
// far as the signature check.
func FuzzJWT(f *testing.F) {
	seed := func(raw, header, claims, sig string) { f.Add(raw, []byte(header), []byte(claims), []byte(sig)) }
	seed("", `{"alg":"ES256","kid":"key-1"}`, `{"iss":"x","exp":9999999999}`, "sig")
	seed("a.b.c", `{"alg":"none"}`, `{}`, "")
	seed("..", `{"alg":"HS256","kid":"key-1"}`, `{"aud":["a",{"b":1}]}`, "\x00")
	seed("eyJ.eyJ.", `{"alg":"ES256","kid":"key-1","crit":["exp"]}`, `{"exp":1e999}`, strings.Repeat("A", 64))
	seed(strings.Repeat("A", 6000)+".e.e", `{"alg":"ES256","kid":"`+strings.Repeat("k", 5000)+`"}`, `{"tenant":"acme"}`, "")
	seed("e.e.e", `{"alg":"ES256","jwk":{"kty":"EC","crv":"P-256","x":"AA","y":"AA"}}`, strings.Repeat(`{"a":`, 2000)+"1"+strings.Repeat("}", 2000), "")
	seed("e.e.e", `{"alg":"ES256","kid":"key-1","b64":false}`, `{"exp":-1e20,"nbf":"x","iat":null}`, "")
	seed("e.e.e", `[`, `{"scope":`+strings.Repeat("[", 3000)+`}`, "")
	seed("e.e.e", `{"alg":"ES256","kid":"key-1","jku":"http://127.0.0.1:1/x","x5u":"http://127.0.0.1:1/y"}`, `{"iss":"","aud":"","exp":4e9,"tenant":"acme"}`, "")

	fr := newFuzzRig(f)
	f.Fuzz(func(t *testing.T, raw string, header, claims, sig []byte) {
		enc := base64.RawURLEncoding
		built := enc.EncodeToString(header) + "." + enc.EncodeToString(claims) + "." + enc.EncodeToString(sig)
		for _, tok := range []string{raw, built} {
			r, err := http.NewRequest(http.MethodGet, "/mcp/crm", nil)
			if err != nil {
				t.Fatal(err)
			}
			r.Header.Set("Authorization", "Bearer "+tok)
			p, err := fr.Authenticate(r)
			switch {
			case err == nil:
				t.Fatalf("authenticated %q as %+v", tok, p)
			case !errors.Is(err, auth.ErrInvalidCredentials) && !errors.Is(err, auth.ErrNoCredentials):
				t.Fatalf("internal error for %q: %v", tok, err)
			}
		}
	})
}

// newFuzzRig is a rig for the whole fuzz target, its keys fetched once, so
// every input naming its kid gets as far as the signature. Its clock never
// moves, so no input fetches again.
func newFuzzRig(f *testing.F) auth.Authenticator {
	f.Helper()
	r := newRig(f)
	if res := r.do(r.token(nil), "/v1/runs"); res.status != http.StatusOK {
		f.Fatalf("setup token: %d", res.status)
	}
	return r.a
}
