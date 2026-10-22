package login_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/auth/authtest"
	"github.com/bsamanvith/agentkit/pkg/auth/login"
)

// recording is a Store that keeps everything it is handed, to search.
type recording struct {
	login.Store
	mu   sync.Mutex
	seen []string
}

func (s *recording) note(v ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, fmt.Sprintf("%+v", v))
}

func (s *recording) Create(ctx context.Context, key string, v login.Session) error {
	s.note(key, v)
	return s.Store.Create(ctx, key, v)
}

func (s *recording) Get(ctx context.Context, key string) (login.Session, error) {
	s.note(key)
	return s.Store.Get(ctx, key)
}

func (s *recording) Touch(ctx context.Context, key string, at time.Time) error {
	s.note(key)
	return s.Store.Touch(ctx, key, at)
}

func (s *recording) Delete(ctx context.Context, key string) error {
	s.note(key)
	return s.Store.Delete(ctx, key)
}

// Canary: no password, and no session id, reaches a log line (the rig checks
// at debug level), an error (signIn checks every one), or the store; a
// printed Login shows neither.
func TestCanary(t *testing.T) {
	t.Parallel()
	st := &recording{Store: login.NewMemoryStore(0)}
	r := newRig(t, login.WithStore(st))
	var ids []string
	for _, try := range [][2]string{
		{"alice", alicePassword},
		{"alice", rootPassword},
		{"root", rootPassword},
		{alicePassword, alicePassword},
		{"nobody", bobPassword},
		{"bob", strings.ToUpper(bobPassword)},
		{"bob", bobPassword},
	} {
		c, err := r.signIn(try[0], try[1], addr1)
		if err == nil {
			ids = append(ids, c.Value)
			q := with(c, http.MethodPost, "Origin", "https://evil.example")
			if _, err := r.authenticate(q); err != nil {
				t.Fatal(err)
			}
			if err := r.l.CheckCSRF(q); err != nil && (authtest.Leaks(err.Error(), c.Value) || authtest.Leaks(err.Error(), try[1])) {
				t.Errorf("CheckCSRF's error holds a secret: %v", err)
			}
			if _, err := r.l.SignOut(q); err != nil {
				t.Fatal(err)
			}
			if _, err := r.authenticate(q); err != nil && authtest.Leaks(err.Error(), c.Value) {
				t.Errorf("Authenticate's error holds the session id: %v", err)
			}
		}
	}
	// A throttled attempt.
	for range 11 {
		_, _ = r.signIn("alice", rootPassword, addr2)
	}
	if len(ids) != 3 {
		t.Fatalf("%d sessions, want 3", len(ids))
	}
	st.mu.Lock()
	stored := strings.Join(st.seen, "\n")
	st.mu.Unlock()
	printed := fmt.Sprintf("%v %+v %#v", r.l, r.l, r.l)
	for _, s := range append(append([]string{}, passwords...), ids...) {
		if authtest.Leaks(printed, s) {
			t.Errorf("a printed Login holds a secret")
		}
	}
	for _, p := range passwords {
		if authtest.Leaks(stored, p) {
			t.Errorf("the store was handed a password:\n%s", stored)
		}
	}
	// The store keys sessions by the SHA-256 of their ids (what Leaks also
	// looks for), so only the ids themselves are searched for there.
	for _, id := range ids {
		for i := 0; i+12 <= len(id); i++ {
			if strings.Contains(stored, id[i:i+12]) {
				t.Fatalf("the store was handed a session id:\n%s", stored)
			}
		}
	}
}
