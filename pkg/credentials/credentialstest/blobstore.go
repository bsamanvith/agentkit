package credentialstest

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

var t0 = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

// RunBlobStore runs the conformance suite for a [credentials.BlobStore].
// newStore returns an empty store; each test gets its own.
func RunBlobStore(t *testing.T, newStore func(t *testing.T) credentials.BlobStore) {
	t.Helper()
	tests := []struct {
		name string
		fn   func(*testing.T, *rig)
	}{
		{"versions", blobVersions},
		{"delete", blobDelete},
		{"tenants", blobTenants},
		{"users", blobUsers},
		{"no_tenant", blobNoTenant},
		{"copies", blobCopies},
		{"one_writer_wins", blobOneWriterWins},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.fn(t, newRig(t, newStore(t), nil))
		})
	}
	t.Run("simulation", func(t *testing.T) {
		t.Parallel()
		for seed := range uint64(20) {
			Simulate(t, newStore, seed)
		}
	})
}

func put(ctx context.Context, t *testing.T, s credentials.BlobStore, user, slot string, b credentials.Blob, ifVersion int64) credentials.Meta {
	t.Helper()
	m, err := s.Put(ctx, user, slot, b, ifVersion)
	if err != nil {
		t.Fatalf("Put(%q, %q, if %d): %v", user, slot, ifVersion, err)
	}
	return m
}

// Create, replace, conflicts: versions count writes and CreatedAt stays.
func blobVersions(t *testing.T, r *rig) {
	ctx := r.ctx(tenantA)
	s := r.store
	m1 := put(ctx, t, s, "", "crm", blob("one", t0), 0)
	if m1.Version != 1 || m1.Slot != "crm" || m1.User != "" || !m1.CreatedAt.Equal(t0) || !m1.UpdatedAt.Equal(t0) {
		t.Errorf("first Put = %+v", m1)
	}
	if _, err := s.Put(ctx, "", "crm", blob("again", t0), 0); !is(err, credentials.ErrExists) {
		t.Errorf("create on an existing slot: %v, want ErrExists", err)
	}
	m2 := put(ctx, t, s, "", "crm", blob("two", t0.Add(time.Minute)), 1)
	if m2.Version != 2 || !m2.CreatedAt.Equal(t0) || !m2.UpdatedAt.Equal(t0.Add(time.Minute)) {
		t.Errorf("replace = %+v, want version 2, created %v", m2, t0)
	}
	// A writer whose clock is behind does not date the update before the
	// credential was created.
	m3 := put(ctx, t, s, "", "crm", blob("three", t0.Add(-time.Hour)), 2)
	if m3.Version != 3 || !m3.CreatedAt.Equal(t0) || !m3.UpdatedAt.Equal(t0) {
		t.Errorf("replace with an earlier clock = %+v, want updated %v", m3, t0)
	}
	for _, v := range []int64{2, 4} {
		if _, err := s.Put(ctx, "", "crm", blob("stale", t0), v); !is(err, credentials.ErrConflict) {
			t.Errorf("Put if %d on version 3: %v, want ErrConflict", v, err)
		}
	}
	if _, err := s.Put(ctx, "", "missing", blob("x", t0), 4); !is(err, credentials.ErrConflict) {
		t.Errorf("Put if 4 on no credential: %v, want ErrConflict", err)
	}
	got, err := s.Get(ctx, "", "crm")
	if err != nil || string(got.Data) != "three" || got.Version != 3 {
		t.Errorf("Get = %q v%d, %v", got.Data, got.Version, err)
	}
	if _, err := s.Get(ctx, "", "missing"); !is(err, credentials.ErrNotFound) {
		t.Errorf("Get of nothing: %v, want ErrNotFound", err)
	}
}

// Deletes, and versions that never repeat after one.
func blobDelete(t *testing.T, r *rig) {
	ctx := r.ctx(tenantA)
	s := r.store
	if err := s.Delete(ctx, "", "crm", 0); !is(err, credentials.ErrNotFound) {
		t.Errorf("Delete of nothing: %v, want ErrNotFound", err)
	}
	put(ctx, t, s, "", "crm", blob("one", t0), 0)
	put(ctx, t, s, "", "crm", blob("two", t0), 1)
	if err := s.Delete(ctx, "", "crm", 1); !is(err, credentials.ErrConflict) {
		t.Errorf("Delete if 1 of version 2: %v, want ErrConflict", err)
	}
	if err := s.Delete(ctx, "", "crm", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "", "crm"); !is(err, credentials.ErrNotFound) {
		t.Errorf("Get after Delete: %v", err)
	}
	m := put(ctx, t, s, "", "crm", blob("three", t0.Add(time.Hour)), 0)
	if m.Version <= 2 || !m.CreatedAt.Equal(t0.Add(time.Hour)) {
		t.Errorf("recreated = %+v, want a version above 2, created anew", m)
	}
	// A writer that read version 1 or 2 before the delete cannot win.
	for _, v := range []int64{1, 2} {
		if _, err := s.Put(ctx, "", "crm", blob("stale", t0), v); !is(err, credentials.ErrConflict) {
			t.Errorf("Put if %d after delete and recreate: %v, want ErrConflict", v, err)
		}
	}
	if err := s.Delete(ctx, "", "crm", 0); err != nil {
		t.Errorf("Delete any version: %v", err)
	}
}

// The same user and slot in two tenants are two credentials.
func blobTenants(t *testing.T, r *rig) {
	a, b := r.ctx(tenantA), r.ctx(tenantB)
	s := r.store
	put(a, t, s, user1, "crm", blob("a's", t0), 0)
	if _, err := s.Get(b, user1, "crm"); !is(err, credentials.ErrNotFound) {
		t.Errorf("B reads A's credential: %v", err)
	}
	if list, _ := s.List(b, user1); len(list) != 0 {
		t.Errorf("B lists %+v", list)
	}
	if _, err := s.Put(b, user1, "crm", blob("b's", t0), 1); !is(err, credentials.ErrConflict) {
		t.Errorf("B replaces A's version 1: %v, want ErrConflict", err)
	}
	if err := s.Delete(b, user1, "crm", 0); !is(err, credentials.ErrNotFound) {
		t.Errorf("B deletes A's: %v, want ErrNotFound", err)
	}
	put(b, t, s, user1, "crm", blob("b's", t0), 0)
	if got, _ := s.Get(a, user1, "crm"); string(got.Data) != "a's" || got.Version != 1 {
		t.Errorf("A's credential after B wrote its own: %q v%d", got.Data, got.Version)
	}
	if err := s.Delete(b, user1, "crm", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(a, user1, "crm"); err != nil {
		t.Errorf("A's credential after B deleted its own: %v", err)
	}
}

// A user's credentials and the tenant's shared ones are apart.
func blobUsers(t *testing.T, r *rig) {
	ctx := r.ctx(tenantA)
	s := r.store
	put(ctx, t, s, "", "crm", blob("shared", t0), 0)
	put(ctx, t, s, user1, "mail", blob("mine", t0), 0)
	put(ctx, t, s, user1, "crm", blob("mine too", t0), 0)
	shared, _ := s.List(ctx, "")
	mine, _ := s.List(ctx, user1)
	if len(shared) != 1 || shared[0].Slot != "crm" || len(mine) != 2 || mine[0].Slot != "crm" || mine[1].Slot != "mail" {
		t.Errorf("List shared = %+v, user = %+v", shared, mine)
	}
	if other, _ := s.List(ctx, "user-2"); len(other) != 0 {
		t.Errorf("another user lists %+v", other)
	}
}

func blobNoTenant(t *testing.T, r *rig) {
	ctx := t.Context()
	s := r.store
	if _, err := s.Get(ctx, "", "crm"); !is(err, tenancy.ErrNoTenant) {
		t.Errorf("Get: %v", err)
	}
	if _, err := s.Put(ctx, "", "crm", blob("x", t0), 0); !is(err, tenancy.ErrNoTenant) {
		t.Errorf("Put: %v", err)
	}
	if err := s.Delete(ctx, "", "crm", 0); !is(err, tenancy.ErrNoTenant) {
		t.Errorf("Delete: %v", err)
	}
	if _, err := s.List(ctx, ""); !is(err, tenancy.ErrNoTenant) {
		t.Errorf("List: %v", err)
	}
}

// What Put takes and Get returns are copies.
func blobCopies(t *testing.T, r *rig) {
	ctx := r.ctx(tenantA)
	data := []byte("original")
	put(ctx, t, r.store, "", "crm", credentials.Blob{Meta: credentials.Meta{Kind: credentials.KindAPIKey, UpdatedAt: t0}, Data: data}, 0)
	copy(data, "XXXXXXXX")
	got, _ := r.store.Get(ctx, "", "crm")
	copy(got.Data, "YYYYYYYY")
	again, _ := r.store.Get(ctx, "", "crm")
	if !bytes.Equal(again.Data, []byte("original")) {
		t.Errorf("stored data changed through a caller's slice: %q", again.Data)
	}
}

// Of writers racing with the same version, exactly one wins.
func blobOneWriterWins(t *testing.T, r *rig) {
	ctx := r.ctx(tenantA)
	put(ctx, t, r.store, "", "crm", blob("v1", t0), 0)
	const writers = 16
	var wg sync.WaitGroup
	results := make(chan error, writers)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.store.Put(ctx, "", "crm", blob(string(rune('a'+i)), t0), 1)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	won := 0
	for err := range results {
		switch {
		case err == nil:
			won++
		case !is(err, credentials.ErrConflict):
			t.Errorf("losing writer: %v, want ErrConflict", err)
		}
	}
	if got, _ := r.store.Get(ctx, "", "crm"); won != 1 || got.Version != 2 {
		t.Errorf("%d writers won, version %d; want exactly one, version 2", won, got.Version)
	}
}
