package auth_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// The middleware authenticates every request; the handler reads the
// principal and its tenant from the context.
func Example() {
	// Configuration holds only the key's hash: auth.HashKey(key), made once.
	keys, err := auth.NewAPIKeys([]auth.APIKey{{
		ID:       "ci",
		Hash:     auth.HashKey("ak-example-0123456789abcdef"),
		Tenant:   "acme",
		Scopes:   []string{"agent:run"},
		Toolsets: []string{"crm-*"},
	}})
	if err != nil {
		fmt.Println(err)
		return
	}
	h := auth.Middleware(keys)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := auth.From(r.Context())
		t, _ := tenancy.From(r.Context())
		fmt.Println("tenant:", t.ID(), "via:", p.Via.ID, "crm-salesforce:", p.AllowsToolset("crm-salesforce"), "gmail:", p.AllowsToolset("gmail"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/runs", nil)
	req.Header.Set("Authorization", "Bearer ak-example-0123456789abcdef")
	h.ServeHTTP(httptest.NewRecorder(), req)

	req = httptest.NewRequest(http.MethodGet, "/v1/runs", nil)
	req.Header.Set("Authorization", "Bearer wrong-key-0123456789abcdef")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	fmt.Println(rec.Code, rec.Header().Get("WWW-Authenticate"))
	// Output:
	// tenant: acme via: ci crm-salesforce: true gmail: false
	// 401 Bearer realm="agentkit", error="invalid_token"
}
