package jwt_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/auth/jwt"
)

// Tokens of an identity provider beside API keys: the JWT authenticator goes
// first and leaves a bearer token that is not a JWT to the keys.
func Example() {
	tokens, err := jwt.New(jwt.Config{
		Issuer:   "https://id.example.com/",
		Audience: "https://agents.example.com",
		// Every token is for the tenant "acme"; scopes "mcp:<toolset>" grant
		// toolsets.
		Tenant: "acme",
	})
	if err != nil {
		panic(err)
	}
	const key = "ci-key-0123456789abcdef"
	entry, _ := auth.KeyEntry(auth.APIKey{ID: "ci", Tenant: "acme", Scopes: []string{"agent:run"}}, key)
	keys, _ := auth.NewAPIKeys([]auth.APIKey{entry})

	h := auth.Middleware(auth.Chain(tokens, keys))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := auth.From(r.Context())
		fmt.Println("authenticated by", p.Via.Kind, p.Via.ID)
	}))
	for _, bearer := range []string{key, "eyJhbGciOiJub25lIn0.e30."} {
		r := httptest.NewRequest(http.MethodGet, "/v1/runs", nil)
		r.Header.Set("Authorization", "Bearer "+bearer)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			fmt.Println(w.Code, w.Header().Get("WWW-Authenticate"))
		}
	}

	_, err = jwt.New(jwt.Config{Issuer: "https://id.example.com/", Audience: "https://agents.example.com", Algorithms: []string{"HS256"}})
	fmt.Println(err != nil)
	// Output:
	// authenticated by api_key ci
	// 401 Bearer realm="agentkit", error="invalid_token"
	// true
}
