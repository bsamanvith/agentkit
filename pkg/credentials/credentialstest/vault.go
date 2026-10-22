package credentialstest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// RunVault runs [credentials.NewVault] over a backend from newStore with a
// test key ring, and checks what the vault adds: values round trip, only
// ciphertext reaches the store, blobs are bound to their tenant, user and
// slot, keys rotate, and compare-and-swap and tenant isolation hold through
// the vault.
func RunVault(t *testing.T, newStore func(t *testing.T) credentials.BlobStore) {
	t.Helper()
	tests := []struct {
		name string
		fn   func(*testing.T, *vaultRig)
	}{
		{"round_trip", vaultRoundTrip},
		{"at_rest", vaultAtRest},
		{"binding", vaultBinding},
		{"bit_flips", vaultBitFlips},
		{"rotation", vaultRotation},
		{"one_writer_wins", vaultOneWriterWins},
		{"tenants", vaultTenants},
		{"listing", vaultListing},
		{"invalid", vaultInvalid},
		{"needs_reconnect", vaultNeedsReconnect},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.fn(t, newVaultRig(t, newStore(t)))
		})
	}
}

// canary is in every secret the vault suite stores.
const canary = "credentialstest-canary-7f3a9c"

// vaultRig records what the vault hands the store.
type vaultRig struct {
	*rig
	rec  *recording
	ring func(ids ...string) *credentials.KeyRing
}

func newVaultRig(t *testing.T, store credentials.BlobStore) *vaultRig {
	keys := map[string][]byte{}
	for _, id := range []string{"k1", "k2", "k3"} {
		k := make([]byte, credentials.KeySize)
		_, _ = rand.Read(k)
		keys[id] = k
	}
	ring := func(ids ...string) *credentials.KeyRing {
		sub := map[string][]byte{}
		for _, id := range ids {
			sub[id] = keys[id]
		}
		r, err := credentials.NewKeyRing(ids[0], sub)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	rec := &recording{BlobStore: store}
	v := credentials.NewVault(rec, credentials.NewSealer(ring("k1")))
	return &vaultRig{rig: newRig(t, rec, v), rec: rec, ring: ring}
}

// vaultWith is a vault over the same store with another key ring.
func (r *vaultRig) vaultWith(ids ...string) credentials.Vault {
	return credentials.NewVault(r.rec, credentials.NewSealer(r.ring(ids...)))
}

// recording is a store that keeps every byte the vault gives it.
type recording struct {
	credentials.BlobStore
	mu    sync.Mutex
	saw   [][]byte
	blobs int
}

func (r *recording) Put(ctx context.Context, user, slot string, b credentials.Blob, ifVersion int64) (credentials.Meta, error) {
	meta, _ := json.Marshal(b.Meta)
	r.mu.Lock()
	r.saw = append(r.saw, bytes.Clone(b.Data), meta, []byte(user), []byte(slot))
	r.blobs++
	r.mu.Unlock()
	return r.BlobStore.Put(ctx, user, slot, b, ifVersion)
}

func allKinds() []credentials.Credential {
	s := credentials.NewSecret
	return []credentials.Credential{
		credentials.APIKey{Key: s("key-" + canary)},
		credentials.Basic{User: "ann", Password: s("pw-" + canary)},
		credentials.OAuth2{
			AccessToken: s("at-" + canary), RefreshToken: s("rt-" + canary),
			Expiry: t0.Add(time.Hour), Scopes: []string{"read", "write"}, TokenType: "Bearer",
		},
		credentials.ClientCredentials{ClientID: "client", ClientSecret: s("cs-" + canary), TokenURL: "https://auth.example/token", Scopes: []string{"api"}},
	}
}

// reveal flattens a credential, secrets included, to compare two.
func reveal(c credentials.Credential) string {
	switch c := c.(type) {
	case credentials.APIKey:
		return "api:" + c.Key.Reveal()
	case credentials.Basic:
		return "basic:" + c.User + ":" + c.Password.Reveal()
	case credentials.OAuth2:
		return fmt.Sprintf("oauth2:%s:%s:%s:%v:%s", c.AccessToken.Reveal(), c.RefreshToken.Reveal(), c.Expiry.UTC().Format(time.RFC3339Nano), c.Scopes, c.TokenType)
	case credentials.ClientCredentials:
		return fmt.Sprintf("cc:%s:%s:%s:%v", c.ClientID, c.ClientSecret.Reveal(), c.TokenURL, c.Scopes)
	}
	return fmt.Sprintf("%T", c)
}

func vaultRoundTrip(t *testing.T, r *vaultRig) {
	ctx := r.ctx(tenantA)
	for i, c := range allKinds() {
		slot := fmt.Sprintf("slot-%d", i)
		m, err := r.vault.Put(ctx, user1, slot, c, 0)
		if err != nil {
			t.Fatalf("Put %s: %v", c.Kind(), err)
		}
		got, gm, err := r.vault.Get(ctx, user1, slot)
		if err != nil || reveal(got) != reveal(c) || !sameMeta(gm, m) || gm.Kind != c.Kind() {
			t.Errorf("%s: Get = %s %+v, %v; want %s %+v", c.Kind(), reveal(got), gm, err, reveal(c), m)
		}
		if o, ok := c.(credentials.OAuth2); ok && !gm.Expiry.Equal(o.Expiry) {
			t.Errorf("oauth2 meta expiry = %v, want %v", gm.Expiry, o.Expiry)
		}
	}
	if _, _, err := r.vault.Get(ctx, user1, "nothing"); !is(err, credentials.ErrNotFound) {
		t.Errorf("Get of nothing: %v", err)
	}
}

// The store sees ciphertext only: no secret in any byte it is given.
func vaultAtRest(t *testing.T, r *vaultRig) {
	ctx := r.ctx(tenantA)
	for i, c := range allKinds() {
		m, err := r.vault.Put(ctx, user1, fmt.Sprintf("s%d", i), c, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.vault.Put(ctx, user1, fmt.Sprintf("s%d", i), c, m.Version); err != nil {
			t.Fatal(err)
		}
	}
	r.rec.mu.Lock()
	saw, blobs := r.rec.saw, r.rec.blobs
	r.rec.mu.Unlock()
	if blobs == 0 {
		t.Fatal("the vault wrote nothing")
	}
	for _, b := range saw {
		if bytes.Contains(b, []byte(canary)) || bytes.Contains(b, []byte("ann")) && bytes.Contains(b, []byte("pw-")) {
			t.Fatalf("the store was given plaintext: %q", b)
		}
	}
	// Two seals of the same value differ (a fresh nonce each time).
	first, _ := r.rec.Get(ctx, user1, "s0")
	m, _ := r.vault.Put(ctx, user1, "s0", allKinds()[0], first.Version)
	second, _ := r.rec.Get(ctx, user1, "s0")
	if m.Version == first.Version || bytes.Equal(first.Data, second.Data) {
		t.Error("sealing the same value twice gave the same bytes")
	}
}

// A blob copied to another tenant, user or slot does not open there.
func vaultBinding(t *testing.T, r *vaultRig) {
	a, b := r.ctx(tenantA), r.ctx(tenantB)
	if _, err := r.vault.Put(a, user1, "crm", allKinds()[0], 0); err != nil {
		t.Fatal(err)
	}
	stolen, err := r.store.Get(a, user1, "crm")
	if err != nil {
		t.Fatal(err)
	}
	for _, to := range []struct {
		name       string
		tenant     string
		user, slot string
	}{
		{"another tenant", tenantB, user1, "crm"},
		{"another user", tenantA, "user-2", "crm"},
		{"the tenant's shared slot", tenantA, "", "crm"},
		{"another slot", tenantA, user1, "mail"},
	} {
		ctx := a
		if to.tenant == tenantB {
			ctx = b
		}
		if _, err := r.rec.BlobStore.Put(ctx, to.user, to.slot, stolen, 0); err != nil {
			t.Fatalf("%s: copying the blob: %v", to.name, err)
		}
		if c, _, err := r.vault.Get(ctx, to.user, to.slot); !is(err, credentials.ErrCorrupt) {
			t.Errorf("%s: Get = %v, %v; want ErrCorrupt", to.name, c, err)
		}
		if err := r.rec.Delete(ctx, to.user, to.slot, 0); err != nil {
			t.Fatal(err)
		}
	}
	// The kind in the metadata must match the sealed one.
	wrong := stolen
	wrong.Kind = credentials.KindBasic
	if _, err := r.rec.BlobStore.Put(a, user1, "crm", wrong, stolen.Version); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.vault.Get(a, user1, "crm"); !is(err, credentials.ErrCorrupt) {
		t.Errorf("metadata kind changed: %v, want ErrCorrupt", err)
	}
	if err := r.rec.Delete(a, user1, "crm", 0); err != nil {
		t.Fatal(err)
	}
}

// Any single flipped bit makes the blob not open (in the key id, it may
// name a key the ring does not have, and so may the id length).
func vaultBitFlips(t *testing.T, r *vaultRig) {
	ctx := r.ctx(tenantA)
	if _, err := r.vault.Put(ctx, "", "crm", allKinds()[0], 0); err != nil {
		t.Fatal(err)
	}
	orig, _ := r.store.Get(ctx, "", "crm")
	idEnd := 2 + int(orig.Data[1])
	version := orig.Version
	for i := range len(orig.Data) * 8 {
		b := orig
		b.Data = bytes.Clone(orig.Data)
		b.Data[i/8] ^= 1 << (i % 8)
		m, err := r.rec.BlobStore.Put(ctx, "", "crm", b, version)
		if err != nil {
			t.Fatal(err)
		}
		version = m.Version
		_, _, err = r.vault.Get(ctx, "", "crm")
		// The id length (byte 1) and the id may come to name another key.
		ok := is(err, credentials.ErrCorrupt) || i/8 >= 1 && i/8 < idEnd && is(err, credentials.ErrUnknownKey)
		if !ok {
			t.Fatalf("bit %d flipped: %v, want ErrCorrupt", i, err)
		}
	}
	b := orig
	m, _ := r.rec.BlobStore.Put(ctx, "", "crm", b, version)
	for _, n := range []int{0, 1, 2, 10, len(orig.Data) - 1} {
		b.Data = orig.Data[:n]
		m, _ = r.rec.BlobStore.Put(ctx, "", "crm", b, m.Version)
		if _, _, err := r.vault.Get(ctx, "", "crm"); !is(err, credentials.ErrCorrupt) {
			t.Errorf("truncated to %d bytes: %v", n, err)
		}
	}
	b.Data = orig.Data
	_, _ = r.rec.BlobStore.Put(ctx, "", "crm", b, m.Version)
}

// Old keys still open; Reseal moves blobs to the current key.
func vaultRotation(t *testing.T, r *vaultRig) {
	ctx := r.ctx(tenantA)
	cred := allKinds()[2]
	if _, err := r.vault.Put(ctx, "", "crm", cred, 0); err != nil { // under k1
		t.Fatal(err)
	}
	rotated := r.vaultWith("k2", "k1")
	if got, _, err := rotated.Get(ctx, "", "crm"); err != nil || reveal(got) != reveal(cred) {
		t.Fatalf("ring k2,k1 opening a k1 blob: %v", err)
	}
	b, _ := r.store.Get(ctx, "", "crm")
	key := credentials.Key{Tenant: tenancy.MustFrom(ctx), Slot: "crm"}
	resealed, err := credentials.NewSealer(r.ring("k2", "k1")).Reseal(key, b.Data)
	if err != nil {
		t.Fatal(err)
	}
	if _, id, err := credentials.NewSealer(r.ring("k2")).Open(key, resealed); err != nil || id != "k2" {
		t.Fatalf("resealed blob opens under %q, %v; want k2", id, err)
	}
	b.Data = resealed
	if _, err := r.rec.BlobStore.Put(ctx, "", "crm", b, b.Version); err != nil {
		t.Fatal(err)
	}
	if got, _, err := r.vaultWith("k2").Get(ctx, "", "crm"); err != nil || reveal(got) != reveal(cred) {
		t.Errorf("after k1 is removed: %v", err)
	}
	if _, _, err := r.vaultWith("k3").Get(ctx, "", "crm"); !is(err, credentials.ErrUnknownKey) {
		t.Errorf("a ring without the blob's key: %v, want ErrUnknownKey", err)
	}
	// Leave it readable by the rig's own vault (k1) for the invariants.
	if err := r.vault.Delete(ctx, "", "crm", 0); err != nil {
		t.Fatal(err)
	}
}

func vaultOneWriterWins(t *testing.T, r *vaultRig) {
	ctx := r.ctx(tenantA)
	m, err := r.vault.Put(ctx, user1, "crm", allKinds()[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.vault.Put(ctx, user1, "crm", credentials.APIKey{Key: credentials.NewSecret(fmt.Sprintf("k%d-%s", i, canary))}, m.Version)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	won := 0
	for err := range errs {
		if err == nil {
			won++
		} else if !is(err, credentials.ErrConflict) {
			t.Errorf("losing writer: %v", err)
		}
	}
	if won != 1 {
		t.Errorf("%d writers won, want 1", won)
	}
	if _, err := r.vault.Put(ctx, user1, "crm", allKinds()[0], 0); !is(err, credentials.ErrExists) {
		t.Errorf("create-only on an existing slot: %v, want ErrExists", err)
	}
}

func vaultTenants(t *testing.T, r *vaultRig) {
	a, b := r.ctx(tenantA), r.ctx(tenantB)
	m, err := r.vault.Put(a, user1, "crm", allKinds()[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.vault.Get(b, user1, "crm"); !is(err, credentials.ErrNotFound) {
		t.Errorf("B gets A's: %v", err)
	}
	if list, _ := r.vault.List(b, user1); len(list) != 0 {
		t.Errorf("B lists %+v", list)
	}
	if _, err := r.vault.Put(b, user1, "crm", allKinds()[1], m.Version); !is(err, credentials.ErrConflict) {
		t.Errorf("B overwrites A's: %v", err)
	}
	if err := r.vault.Delete(b, user1, "crm", 0); !is(err, credentials.ErrNotFound) {
		t.Errorf("B deletes A's: %v", err)
	}
	if _, _, err := r.vault.Get(a, user1, "crm"); err != nil {
		t.Errorf("A's credential after B's attempts: %v", err)
	}
	none := t.Context()
	if _, _, err := r.vault.Get(none, user1, "crm"); !is(err, tenancy.ErrNoTenant) {
		t.Errorf("Get with no tenant: %v", err)
	}
	if _, err := r.vault.Put(none, user1, "crm", allKinds()[0], 0); !is(err, tenancy.ErrNoTenant) {
		t.Errorf("Put with no tenant: %v", err)
	}
	if err := r.vault.Delete(none, user1, "crm", 0); !is(err, tenancy.ErrNoTenant) {
		t.Errorf("Delete with no tenant: %v", err)
	}
	if _, err := r.vault.List(none, user1); !is(err, tenancy.ErrNoTenant) {
		t.Errorf("List with no tenant: %v", err)
	}
}

// List returns metadata, which carries no secret even as JSON.
func vaultListing(t *testing.T, r *vaultRig) {
	ctx := r.ctx(tenantA)
	for i, c := range allKinds() {
		if _, err := r.vault.Put(ctx, user1, fmt.Sprintf("s%d", i), c, 0); err != nil {
			t.Fatal(err)
		}
	}
	metas, err := r.vault.List(ctx, user1)
	if err != nil || len(metas) != len(allKinds()) {
		t.Fatalf("List = %+v, %v", metas, err)
	}
	js, err := json.Marshal(metas)
	if err != nil || strings.Contains(string(js), canary) {
		t.Errorf("metadata JSON %s, %v", js, err)
	}
}

func vaultInvalid(t *testing.T, r *vaultRig) {
	ctx := r.ctx(tenantA)
	for _, slot := range []string{"", "has space", "../x", strings.Repeat("a", 65)} {
		if _, err := r.vault.Put(ctx, "", slot, allKinds()[0], 0); !is(err, credentials.ErrInvalidKey) {
			t.Errorf("slot %q: %v, want ErrInvalidKey", slot, err)
		}
	}
	if _, err := r.vault.Put(ctx, "a\x00b", "crm", allKinds()[0], 0); !is(err, credentials.ErrInvalidKey) {
		t.Errorf("user with NUL: %v", err)
	}
	for _, c := range []credentials.Credential{nil, credentials.APIKey{}, credentials.OAuth2{}, credentials.ClientCredentials{ClientID: "x"}} {
		if _, err := r.vault.Put(ctx, "", "crm", c, 0); !is(err, credentials.ErrInvalidCredential) {
			t.Errorf("%#v: %v, want ErrInvalidCredential", c, err)
		}
	}
	if _, err := r.vault.Put(ctx, "", "crm", allKinds()[0], -1); err == nil {
		t.Error("ifVersion -1 accepted")
	}
}

// MarkNeedsReconnect marks one version, keeps the value, and the next write
// clears the mark.
func vaultNeedsReconnect(t *testing.T, r *vaultRig) {
	ctx := r.ctx(tenantA)
	cred := allKinds()[2]
	if _, err := r.vault.MarkNeedsReconnect(ctx, user1, "mail", 1); !is(err, credentials.ErrNotFound) {
		t.Errorf("mark of nothing: %v, want ErrNotFound", err)
	}
	m1, err := r.vault.Put(ctx, user1, "mail", cred, 0)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := r.vault.Put(ctx, user1, "mail", cred, m1.Version)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []int64{0, m1.Version, m2.Version + 1} {
		if _, err := r.vault.MarkNeedsReconnect(ctx, user1, "mail", v); !is(err, credentials.ErrConflict) {
			t.Errorf("mark if %d of version %d: %v, want ErrConflict", v, m2.Version, err)
		}
	}
	if _, err := r.vault.MarkNeedsReconnect(r.ctx(tenantB), user1, "mail", m2.Version); !is(err, credentials.ErrNotFound) {
		t.Errorf("B marks A's: %v, want ErrNotFound", err)
	}
	m3, err := r.vault.MarkNeedsReconnect(ctx, user1, "mail", m2.Version)
	if err != nil || !m3.NeedsReconnect || m3.Version <= m2.Version || !m3.CreatedAt.Equal(m1.CreatedAt) || m3.Kind != cred.Kind() {
		t.Fatalf("mark = %+v, %v", m3, err)
	}
	got, gm, err := r.vault.Get(ctx, user1, "mail")
	if err != nil || reveal(got) != reveal(cred) || !sameMeta(gm, m3) {
		t.Errorf("Get after the mark = %s %+v, %v", reveal(got), gm, err)
	}
	if list, _ := r.vault.List(ctx, user1); len(list) != 1 || !list[0].NeedsReconnect {
		t.Errorf("List after the mark = %+v", list)
	}
	if _, err := r.vault.MarkNeedsReconnect(ctx, user1, "mail", m2.Version); !is(err, credentials.ErrConflict) {
		t.Errorf("a second mark of the version before: %v, want ErrConflict", err)
	}
	m4, err := r.vault.Put(ctx, user1, "mail", cred, m3.Version)
	if err != nil || m4.NeedsReconnect {
		t.Errorf("write after the mark = %+v, %v; want the mark cleared", m4, err)
	}
	if _, gm, _ := r.vault.Get(ctx, user1, "mail"); gm.NeedsReconnect {
		t.Error("Get still says NeedsReconnect after a write")
	}
	if _, err := r.vault.MarkNeedsReconnect(ctx, user1, "bad slot", 1); !is(err, credentials.ErrInvalidKey) {
		t.Errorf("mark of a bad slot: %v", err)
	}
	if _, err := r.vault.MarkNeedsReconnect(t.Context(), user1, "mail", m4.Version); !is(err, tenancy.ErrNoTenant) {
		t.Errorf("mark with no tenant: %v", err)
	}
}
