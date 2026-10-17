package credentialstest

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// Tenants and users the suites use. Every test uses the same user and slot
// names in two tenants, so a backend that ignores the tenant fails.
const (
	tenantA = "tenant-a"
	tenantB = "tenant-b"
	user1   = "user-1"
)

var users = []string{"", user1}

func ctxFor(t *testing.T, tenant string) context.Context {
	t.Helper()
	tn, err := tenancy.NewTenant(tenant)
	if err != nil {
		t.Fatal(err)
	}
	return tenancy.With(t.Context(), tn)
}

// rig is one test's store, and the vault over it when the test has one. At
// cleanup it checks the invariants for every tenant and user.
type rig struct {
	t     *testing.T
	store credentials.BlobStore
	vault credentials.Vault // nil in a BlobStore test

	mu      sync.Mutex
	touched map[string]bool // tenants
}

func newRig(t *testing.T, store credentials.BlobStore, vault credentials.Vault) *rig {
	r := &rig{t: t, store: store, vault: vault, touched: map[string]bool{tenantA: true, tenantB: true}}
	t.Cleanup(r.checkInvariants)
	return r
}

func (r *rig) ctx(tenant string) context.Context {
	r.mu.Lock()
	r.touched[tenant] = true
	r.mu.Unlock()
	return ctxFor(r.t, tenant)
}

func (r *rig) checkInvariants() {
	t := r.t
	if t.Failed() {
		return
	}
	for tenant := range r.touched {
		ctx := ctxFor(t, tenant)
		for _, user := range users {
			metas, err := r.store.List(ctx, user)
			if err != nil {
				t.Errorf("invariant: List(%s, %q): %v", tenant, user, err)
				continue
			}
			if !slices.IsSortedFunc(metas, func(a, b credentials.Meta) int { return cmpString(a.Slot, b.Slot) }) {
				t.Errorf("invariant: List(%s, %q) not sorted by slot", tenant, user)
			}
			for i, m := range metas {
				if i > 0 && metas[i-1].Slot == m.Slot {
					t.Errorf("invariant: List(%s, %q) has slot %q twice", tenant, user, m.Slot)
				}
				if m.User != user || m.Version < 1 || m.CreatedAt.After(m.UpdatedAt) || m.Kind == "" {
					t.Errorf("invariant: List(%s, %q) entry %+v", tenant, user, m)
				}
				b, err := r.store.Get(ctx, user, m.Slot)
				if err != nil || !sameMeta(b.Meta, m) {
					t.Errorf("invariant: Get(%s, %q, %q) = %+v, %v; List says %+v", tenant, user, m.Slot, b.Meta, err, m)
				}
				if r.vault != nil {
					if _, vm, err := r.vault.Get(ctx, user, m.Slot); err != nil || !sameMeta(vm, m) {
						t.Errorf("invariant: vault Get(%s, %q, %q) = %+v, %v", tenant, user, m.Slot, vm, err)
					}
				}
			}
		}
	}
}

func cmpString(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func sameMeta(a, b credentials.Meta) bool {
	return a.Slot == b.Slot && a.User == b.User && a.Kind == b.Kind && a.Version == b.Version &&
		a.CreatedAt.Equal(b.CreatedAt) && a.UpdatedAt.Equal(b.UpdatedAt) && a.Expiry.Equal(b.Expiry) &&
		a.NeedsReconnect == b.NeedsReconnect
}

// blob is a blob with recognizable bytes, as a store test writes it.
func blob(data string, at time.Time) credentials.Blob {
	return credentials.Blob{
		Meta: credentials.Meta{Kind: credentials.KindAPIKey, UpdatedAt: at},
		Data: []byte(data),
	}
}

func is(err, target error) bool { return errors.Is(err, target) }
