// Package logintest has helpers for tests of code that uses package login: a
// cheap [Hasher] that stands in for argon2id and keeps a clock of the work it
// was asked to do, and [RunStore], the conformance suite of a login.Store.
package logintest

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/auth/login"
)

// Hasher is a login.Hasher (pass [Hasher.Hash]) that derives keys with
// SHA-256 in place of argon2id, so a test can sign in thousands of times. Its
// clock advances by what argon2id would cost (memory × passes, one
// nanosecond per KiB pass), so a test can compare the work two sign-ins did
// without measuring wall time. It is safe for concurrent use.
type Hasher struct {
	mu      sync.Mutex
	elapsed time.Duration
	calls   int
}

// Hash has the signature of argon2.IDKey.
func (h *Hasher) Hash(password, salt []byte, t, m uint32, p uint8, keyLen uint32) []byte {
	h.mu.Lock()
	h.elapsed += time.Duration(uint64(m) * uint64(t))
	h.calls++
	h.mu.Unlock()
	var params [9]byte
	binary.BigEndian.PutUint32(params[0:], t)
	binary.BigEndian.PutUint32(params[4:], m)
	params[8] = p
	var out []byte
	for i := byte(0); uint32(len(out)) < keyLen; i++ {
		d := sha256.New()
		d.Write([]byte{i})
		d.Write(params[:])
		d.Write(salt)
		d.Write(password)
		out = d.Sum(out)
	}
	return out[:keyLen]
}

// Elapsed is the hasher's clock: the work of every Hash call so far.
func (h *Hasher) Elapsed() time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.elapsed
}

// Calls is the number of Hash calls so far.
func (h *Hasher) Calls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls
}

// PHC returns the hash of password made with hash (such as [Hasher.Hash])
// and p, in the PHC form a login.User's PasswordHash takes. The salt comes
// from the password, so the same password always gives the same hash.
func PHC(hash login.Hasher, password string, p login.Params) string {
	sum := sha256.Sum256([]byte("salt:" + password))
	salt := sum[:16]
	key := hash([]byte(password), salt, p.Time, p.Memory, p.Threads, 32)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", p.Memory, p.Time, p.Threads, b64.EncodeToString(salt), b64.EncodeToString(key))
}

// RunStore checks that a login.Store behaves as the interface documents.
// newStore is called once per subtest and returns an empty store.
func RunStore(t *testing.T, newStore func(t *testing.T) login.Store) {
	t.Helper()
	at := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	s1 := login.Session{Username: "alice", CSRF: "csrf-1", Created: at, LastSeen: at}
	s2 := login.Session{Username: "bob", CSRF: "csrf-2", Created: at.Add(time.Hour), LastSeen: at.Add(2 * time.Hour)}
	sub := func(name string, f func(t *testing.T, ctx context.Context, s login.Store)) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f(t, t.Context(), newStore(t))
		})
	}

	sub("CreateThenGet", func(t *testing.T, ctx context.Context, s login.Store) {
		must(t, s.Create(ctx, "k1", s1))
		must(t, s.Create(ctx, "k2", s2))
		for k, want := range map[string]login.Session{"k1": s1, "k2": s2} {
			if got, err := s.Get(ctx, k); err != nil || !same(got, want) {
				t.Errorf("Get(%s) = %+v, %v; want %+v", k, got, err, want)
			}
		}
	})

	sub("MissingIsErrNoSession", func(t *testing.T, ctx context.Context, s login.Store) {
		if _, err := s.Get(ctx, "nope"); !errors.Is(err, login.ErrNoSession) {
			t.Errorf("Get of a missing key: %v, want ErrNoSession", err)
		}
		if err := s.Touch(ctx, "nope", at); !errors.Is(err, login.ErrNoSession) {
			t.Errorf("Touch of a missing key: %v, want ErrNoSession", err)
		}
		if err := s.Delete(ctx, "nope"); err != nil {
			t.Errorf("Delete of a missing key: %v, want nil", err)
		}
	})

	sub("TouchSetsLastSeenOnly", func(t *testing.T, ctx context.Context, s login.Store) {
		must(t, s.Create(ctx, "k1", s1))
		later := at.Add(3 * time.Hour)
		must(t, s.Touch(ctx, "k1", later))
		want := s1
		want.LastSeen = later
		if got, err := s.Get(ctx, "k1"); err != nil || !same(got, want) {
			t.Errorf("after Touch: %+v, %v; want %+v", got, err, want)
		}
	})

	sub("DeleteRemovesOnlyItsKey", func(t *testing.T, ctx context.Context, s login.Store) {
		must(t, s.Create(ctx, "k1", s1))
		must(t, s.Create(ctx, "k2", s2))
		must(t, s.Delete(ctx, "k1"))
		if _, err := s.Get(ctx, "k1"); !errors.Is(err, login.ErrNoSession) {
			t.Errorf("Get after Delete: %v, want ErrNoSession", err)
		}
		if _, err := s.Get(ctx, "k2"); err != nil {
			t.Errorf("another session after Delete: %v", err)
		}
	})

	sub("ExpireByIdleAndAge", func(t *testing.T, ctx context.Context, s login.Store) {
		must(t, s.Create(ctx, "k1", s1)) // seen at, created at
		must(t, s.Create(ctx, "k2", s2)) // seen at+2h, created at+1h
		// Neither is idle before at, nor created before at: both stay.
		must(t, s.Expire(ctx, at, at))
		for _, k := range []string{"k1", "k2"} {
			if _, err := s.Get(ctx, k); err != nil {
				t.Errorf("%s expired too early: %v", k, err)
			}
		}
		// k1 was last seen before at+1h.
		must(t, s.Expire(ctx, at.Add(time.Hour), at))
		if _, err := s.Get(ctx, "k1"); !errors.Is(err, login.ErrNoSession) {
			t.Errorf("an idle session was kept: %v", err)
		}
		// k2 was created before at+90m, though seen since.
		must(t, s.Expire(ctx, at, at.Add(90*time.Minute)))
		if _, err := s.Get(ctx, "k2"); !errors.Is(err, login.ErrNoSession) {
			t.Errorf("an old session was kept: %v", err)
		}
	})

	sub("ConcurrentUse", func(t *testing.T, ctx context.Context, s login.Store) {
		var wg sync.WaitGroup
		for i := range 16 {
			wg.Go(func() {
				k := fmt.Sprintf("k%d", i)
				if err := s.Create(ctx, k, s1); err != nil {
					t.Error(err)
					return
				}
				if err := s.Touch(ctx, k, at.Add(time.Minute)); err != nil {
					t.Error(err)
				}
				if _, err := s.Get(ctx, k); err != nil {
					t.Error(err)
				}
				if err := s.Delete(ctx, k); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
	})
}

func same(a, b login.Session) bool {
	return a.Username == b.Username && a.CSRF == b.CSRF && a.Created.Equal(b.Created) && a.LastSeen.Equal(b.LastSeen)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
