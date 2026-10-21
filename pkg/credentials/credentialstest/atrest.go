package credentialstest

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/credentials"
)

// AtRestSlot is the slot [CheckAtRest] writes, and so a string the raw
// bytes it reads must contain: proof that they are the store's bytes.
const AtRestSlot = "atrestslot"

// CheckAtRest is the canary test of a backend that keeps its blobs
// somewhere a person could read: a file, a database. It stores every kind of
// credential, each carrying the suite's canary in its secrets, through a
// vault over store, in two tenants and for a user and the tenant; then raw
// returns every byte the backend keeps (the file, the database file and its
// journal, a dump of the rows). The canary must be in none of them, and the
// slot name must be (or raw read nothing).
func CheckAtRest(t *testing.T, store credentials.BlobStore, raw func(t *testing.T) []byte) {
	t.Helper()
	r := newVaultRig(t, store)
	for _, tenant := range []string{tenantA, tenantB} {
		ctx := r.ctx(tenant)
		for _, user := range users {
			for i, c := range allKinds() {
				slot := fmt.Sprintf("%s%d", AtRestSlot, i)
				m, err := r.vault.Put(ctx, user, slot, c, 0)
				if err != nil {
					t.Fatalf("Put(%s, %q, %s): %v", tenant, user, slot, err)
				}
				if _, err := r.vault.Put(ctx, user, slot, c, m.Version); err != nil {
					t.Fatalf("replace (%s, %q, %s): %v", tenant, user, slot, err)
				}
			}
			// A deleted credential leaves no plaintext behind either.
			if err := r.vault.Delete(ctx, user, AtRestSlot+"0", 0); err != nil {
				t.Fatal(err)
			}
		}
	}
	b := raw(t)
	if !bytes.Contains(b, []byte(AtRestSlot)) {
		t.Fatalf("the raw bytes (%d) do not hold the slot name %q: they are not the store's", len(b), AtRestSlot)
	}
	// Every secret holds the whole canary, so searching for it finds any.
	if i := bytes.Index(b, []byte(canary)); i >= 0 {
		t.Fatalf("the canary is at rest at byte %d: %q", i, b[max(0, i-40):min(len(b), i+80)])
	}
}
