package auth_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/auth/authtest"
)

// TestCanaryKeyNeverLeaks sends a canary key every way a client might and
// the configuration every wrong way, and searches every log line (debug
// level, the careless handler's included), error string, response header
// and body for the key and its hash. The search itself is the rig's: it runs
// after every test.
func TestCanaryKeyNeverLeaks(t *testing.T) {
	t.Parallel()
	const canary = "CANARY-9b7e2f4d1c8a6e3f5b0d"
	entries := append(testEntries(t), auth.APIKey{ID: "canary", Hash: auth.HashKey(canary), Tenant: "acme", Scopes: []string{"*"}})
	keys, err := auth.NewAPIKeys(entries)
	if err != nil {
		t.Fatal(err)
	}
	all := append([]string{canary}, secrets...)

	var errs []string
	for _, r := range []*rig{newRig(t, keys, all), newRig(t, auth.Chain(keys), all, auth.WithResourceMetadata("https://example.test/meta"))} {
		for _, authz := range [][]string{
			{"Bearer " + canary},
			{"bearer   " + canary},
			{"Bearer " + canary[:len(canary)-1]},
			{"Bearer " + canary + "0"},
			{"Bearer " + strings.ToLower(canary)},
			{"Basic " + canary},
			{canary},
			{"Bearer " + canary, "Bearer " + canary},
			{"Bearer " + canary + strings.Repeat("-", auth.MaxAuthorizationLength)},
			{"Bearer " + canary + "\x00"},
		} {
			r.get(authz, "Cookie", "theme=dark", auth.RequestIDHeader, "canary-test")
			req, err := http.NewRequest(http.MethodPost, "/mcp/crm", nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, v := range authz {
				req.Header.Add("Authorization", v)
			}
			if _, err := keys.Authenticate(req); err != nil {
				errs = append(errs, err.Error(), fmt.Sprintf("%+v", err))
			}
		}
	}

	// Configuration errors about the canary's entry.
	for _, bad := range []func(e *auth.APIKey){
		func(e *auth.APIKey) { e.Tenant = "" },
		func(e *auth.APIKey) { e.Hash = e.Hash[:len(e.Hash)-1] },
		func(e *auth.APIKey) { e.Hash += "0" },
		func(e *auth.APIKey) { e.ID = "acme-ci" },
		func(e *auth.APIKey) { e.Toolsets = []string{"["} },
	} {
		es := append(testEntries(t), auth.APIKey{ID: "canary", Hash: auth.HashKey(canary), Tenant: "acme"})
		bad(&es[len(es)-1])
		if _, err := auth.NewAPIKeys(es); err != nil {
			errs = append(errs, err.Error())
		}
	}
	dup := append(testEntries(t), auth.APIKey{ID: "canary", Hash: auth.HashKey(canary), Tenant: "acme"}, auth.APIKey{ID: "canary2", Hash: auth.HashKey(canary), Tenant: "globex"})
	if _, err := auth.NewAPIKeys(dup); err != nil {
		errs = append(errs, err.Error())
	}
	for _, short := range []string{canary[:15], canary + " "} {
		if _, err := auth.KeyEntry(auth.APIKey{ID: "canary", Tenant: "acme"}, short); err != nil {
			errs = append(errs, err.Error())
		}
	}
	errs = append(errs, keys.String(), fmt.Sprint(keys), fmt.Sprintf("%v %+v %#v", keys, keys, keys))

	if len(errs) < 10 {
		t.Fatalf("only %d errors collected: the test is not exercising the error paths", len(errs))
	}
	for _, e := range errs {
		if authtest.Leaks(e, canary) {
			t.Errorf("an error or description contains the canary key or its hash: %s", e)
		}
	}
}
