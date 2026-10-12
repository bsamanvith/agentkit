package auth_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/auth"
)

// FuzzAuthorizationHeader sends arbitrary Authorization headers (the input
// split at '\n' into several headers; "" for none) through APIKeys and the
// middleware. Nothing panics, and a request is authenticated exactly when an
// independent restatement of the rules says so: one header, within the
// length limit, "bearer" in any case, one or more spaces, then exactly a
// configured key. The rig checks its invariants and the leak search on every
// input.
func FuzzAuthorizationHeader(f *testing.F) {
	for _, s := range []string{
		"", "Bearer " + acmeKey, "bearer " + globexKey, "BEARER   " + acmeUser, "Bearer " + acmeKey + "\nBearer " + acmeKey,
		"Bearer " + acmeKey[:10], "Bearer " + acmeKey + " ", "Bearer\t" + acmeKey, "Basic " + acmeKey, acmeKey,
		"Bearer ", "Bearer", "Bearer " + acmeKey + "\x00", "Bearer " + strings.Repeat("a", auth.MaxAuthorizationLength),
		"Bearer ünïcödé", "Bearer " + acmeKey + "," + globexKey, "Bearer " + acmeKey + ".x",
	} {
		f.Add(s)
	}
	keys := map[string]string{acmeKey: "acme", acmeUser: "acme", globexKey: "globex"}
	f.Fuzz(func(t *testing.T, raw string) {
		var values []string
		if raw != "" {
			values = strings.Split(raw, "\n")
		}
		wantTenant := ""
		if len(values) == 1 && len(values[0]) <= auth.MaxAuthorizationLength && len(values[0]) > 7 && strings.EqualFold(values[0][:7], "bearer ") {
			wantTenant = keys[strings.TrimLeft(values[0][7:], " ")]
		}

		a := testKeys(t)
		req, err := http.NewRequest(http.MethodGet, "/mcp/", nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range values {
			req.Header.Add("Authorization", v)
		}
		p, err := a.Authenticate(req)
		switch {
		case wantTenant != "" && (err != nil || p.Tenant.ID() != wantTenant):
			t.Fatalf("%q: got %q, %v; want tenant %q", raw, p.Tenant.ID(), err, wantTenant)
		case wantTenant == "" && err == nil:
			t.Fatalf("%q: authenticated as %+v", raw, p)
		case wantTenant == "" && !errors.Is(err, auth.ErrNoCredentials) && !errors.Is(err, auth.ErrInvalidCredentials):
			t.Fatalf("%q: internal error %v", raw, err)
		case wantTenant == "" && errors.Is(err, auth.ErrNoCredentials) != (len(values) == 0):
			t.Fatalf("%q: %v; ErrNoCredentials is only for no header", raw, err)
		}

		res := newRig(t, a, secrets).get(values)
		if res.called != (wantTenant != "") || (res.called && res.seen.tenant.ID() != wantTenant) {
			t.Fatalf("%q through the middleware: status %d, tenant %q; want tenant %q", raw, res.status, res.seen.tenant.ID(), wantTenant)
		}
	})
}
