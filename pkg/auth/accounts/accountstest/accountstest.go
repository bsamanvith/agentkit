// Package accountstest has the conformance suite every accounts.Store
// passes ([RunStore]) and a fake accounts.Sender that keeps what it sends
// ([Outbox]).
package accountstest

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/bsamanvith/agentkit/pkg/auth/accounts"
)

// Message is one message an [Outbox] was given.
type Message struct{ To, Text string }

// Outbox is an accounts.Sender that keeps the messages, for a test to read
// the code from. Fail makes the next sends fail. Safe for concurrent use.
type Outbox struct {
	mu   sync.Mutex
	msgs []Message
	fail error
}

var _ accounts.Sender = (*Outbox)(nil)

// Send implements accounts.Sender.
func (o *Outbox) Send(_ context.Context, to, text string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.fail != nil {
		return o.fail
	}
	o.msgs = append(o.msgs, Message{to, text})
	return nil
}

// Fail makes sends fail with err (nil to work again).
func (o *Outbox) Fail(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.fail = err
}

// Messages returns what was sent, oldest first.
func (o *Outbox) Messages() []Message {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]Message(nil), o.msgs...)
}

var digits = regexp.MustCompile(`\b\d{6}\b`)

// LastCode returns the six-digit code in the last message sent to to, or
// fails the test.
func (o *Outbox) LastCode(t testing.TB, to string) string {
	t.Helper()
	msgs := o.Messages()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].To == to {
			if c := digits.FindString(msgs[i].Text); c != "" {
				return c
			}
		}
	}
	t.Fatalf("no code was sent to %s (%d messages)", to, len(msgs))
	return ""
}

// RunStore runs the conformance suite on stores newStore makes, a fresh
// empty one per subtest.
func RunStore(t *testing.T, newStore func(t *testing.T) accounts.Store) {
	t0 := time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC)
	ctx := context.Background()
	acc := func(id string) accounts.Account {
		return accounts.Account{ID: id, Name: "Lakshmi " + id, Phone: "+919876543210", Roles: []string{"partner"}, CreatedAt: t0, UpdatedAt: t0}
	}
	ident := func(lane, subject, account string, at time.Time) accounts.Identity {
		return accounts.Identity{Lane: lane, Subject: subject, Account: account, Email: subject + "@example.com", EmailVerified: true, Name: "L", CreatedAt: at, LastUsedAt: at}
	}

	t.Run("Accounts", func(t *testing.T) {
		s := newStore(t)
		a := acc("a1")
		must(t, s.CreateAccount(ctx, a))
		if err := s.CreateAccount(ctx, a); !errors.Is(err, accounts.ErrExists) {
			t.Fatalf("CreateAccount twice: %v, want ErrExists", err)
		}
		got, err := s.Account(ctx, "a1")
		must(t, err)
		equal(t, "Account", got, a)
		a.Name, a.Roles = "Lakshmi Devi", []string{"admin", "partner"}
		must(t, s.UpdateAccount(ctx, a))
		got, err = s.Account(ctx, "a1")
		must(t, err)
		equal(t, "updated Account", got, a)
		if _, err := s.Account(ctx, "nobody"); !errors.Is(err, accounts.ErrNotFound) {
			t.Errorf("Account(missing): %v, want ErrNotFound", err)
		}
		if err := s.UpdateAccount(ctx, acc("nobody")); !errors.Is(err, accounts.ErrNotFound) {
			t.Errorf("UpdateAccount(missing): %v, want ErrNotFound", err)
		}
		if err := s.DeleteAccount(ctx, "nobody"); !errors.Is(err, accounts.ErrNotFound) {
			t.Errorf("DeleteAccount(missing): %v, want ErrNotFound", err)
		}
	})

	t.Run("Identities", func(t *testing.T) {
		s := newStore(t)
		must(t, s.CreateAccount(ctx, acc("a1")))
		must(t, s.CreateAccount(ctx, acc("a2")))
		g := ident("google", "sub-1", "a1", t0)
		p := ident("password", "lakshmi", "a1", t0.Add(time.Minute))
		p.Secret = "$argon2id$hash"
		must(t, s.AddIdentity(ctx, p))
		must(t, s.AddIdentity(ctx, g))
		// The same subject in another lane is another identity.
		must(t, s.AddIdentity(ctx, ident("phone", "sub-1", "a2", t0)))
		if err := s.AddIdentity(ctx, ident("google", "sub-1", "a2", t0)); !errors.Is(err, accounts.ErrExists) {
			t.Fatalf("AddIdentity of a taken lane and subject: %v, want ErrExists", err)
		}
		got, err := s.Identity(ctx, "password", "lakshmi")
		must(t, err)
		equal(t, "Identity keeps its secret", got, p)
		list, err := s.Identities(ctx, "a1")
		must(t, err)
		equal(t, "Identities oldest first", list, []accounts.Identity{g, p})
		none, err := s.Identities(ctx, "nobody")
		must(t, err)
		if len(none) != 0 {
			t.Errorf("Identities(no account) = %v, want none", none)
		}

		upd := g
		upd.Account, upd.Email, upd.LastUsedAt = "a2", "new@example.com", t0.Add(time.Hour)
		must(t, s.UpdateIdentity(ctx, upd))
		got, err = s.Identity(ctx, "google", "sub-1")
		must(t, err)
		upd.Account = "a1"
		equal(t, "UpdateIdentity changes all but the account", got, upd)
		if err := s.UpdateIdentity(ctx, ident("google", "nope", "a1", t0)); !errors.Is(err, accounts.ErrNotFound) {
			t.Errorf("UpdateIdentity(missing): %v, want ErrNotFound", err)
		}
		must(t, s.RemoveIdentity(ctx, "google", "sub-1"))
		if _, err := s.Identity(ctx, "google", "sub-1"); !errors.Is(err, accounts.ErrNotFound) {
			t.Errorf("Identity after RemoveIdentity: %v, want ErrNotFound", err)
		}
		if err := s.RemoveIdentity(ctx, "google", "sub-1"); !errors.Is(err, accounts.ErrNotFound) {
			t.Errorf("RemoveIdentity twice: %v, want ErrNotFound", err)
		}
	})

	t.Run("Sessions", func(t *testing.T) {
		s := newStore(t)
		sess := accounts.Session{Account: "a1", Lane: "google", CSRF: "tok", Created: t0, LastSeen: t0}
		must(t, s.CreateSession(ctx, "k1", sess))
		must(t, s.CreateSession(ctx, "k2", accounts.Session{Account: "a1", Created: t0, LastSeen: t0.Add(time.Hour)}))
		must(t, s.CreateSession(ctx, "k3", accounts.Session{Account: "a2", Created: t0.Add(-48 * time.Hour), LastSeen: t0.Add(time.Hour)}))
		got, err := s.Session(ctx, "k1")
		must(t, err)
		equal(t, "Session", got, sess)
		must(t, s.TouchSession(ctx, "k1", t0.Add(2*time.Hour)))
		got, err = s.Session(ctx, "k1")
		must(t, err)
		if !got.LastSeen.Equal(t0.Add(2 * time.Hour)) {
			t.Errorf("LastSeen after TouchSession = %v", got.LastSeen)
		}
		if err := s.TouchSession(ctx, "nope", t0); !errors.Is(err, accounts.ErrNotFound) {
			t.Errorf("TouchSession(missing): %v, want ErrNotFound", err)
		}
		// k2 was last seen at t0+1h, so idle t0+90m expires it; k3 was
		// created two days ago.
		must(t, s.ExpireSessions(ctx, t0.Add(90*time.Minute), t0.Add(-24*time.Hour)))
		for k, want := range map[string]bool{"k1": true, "k2": false, "k3": false} {
			_, err := s.Session(ctx, k)
			if (err == nil) != want {
				t.Errorf("after ExpireSessions, Session(%s): %v, want kept %v", k, err, want)
			}
		}
		must(t, s.DeleteSession(ctx, "k1"))
		must(t, s.DeleteSession(ctx, "k1"))
		if _, err := s.Session(ctx, "k1"); !errors.Is(err, accounts.ErrNotFound) {
			t.Errorf("Session after DeleteSession: %v, want ErrNotFound", err)
		}
		must(t, s.CreateSession(ctx, "k4", sess))
		must(t, s.CreateSession(ctx, "k5", sess))
		must(t, s.CreateSession(ctx, "k6", accounts.Session{Account: "a2", Created: t0, LastSeen: t0}))
		must(t, s.DeleteSessions(ctx, "a1"))
		for k, want := range map[string]bool{"k4": false, "k5": false, "k6": true} {
			_, err := s.Session(ctx, k)
			if (err == nil) != want {
				t.Errorf("after DeleteSessions(a1), Session(%s): %v, want kept %v", k, err, want)
			}
		}
	})

	t.Run("DeleteAccountTakesItsIdentitiesAndSessions", func(t *testing.T) {
		s := newStore(t)
		must(t, s.CreateAccount(ctx, acc("a1")))
		must(t, s.CreateAccount(ctx, acc("a2")))
		must(t, s.AddIdentity(ctx, ident("google", "1", "a1", t0)))
		must(t, s.AddIdentity(ctx, ident("google", "2", "a2", t0)))
		must(t, s.CreateSession(ctx, "k1", accounts.Session{Account: "a1", Created: t0, LastSeen: t0}))
		must(t, s.CreateSession(ctx, "k2", accounts.Session{Account: "a2", Created: t0, LastSeen: t0}))
		must(t, s.DeleteAccount(ctx, "a1"))
		if _, err := s.Account(ctx, "a1"); !errors.Is(err, accounts.ErrNotFound) {
			t.Errorf("Account after DeleteAccount: %v", err)
		}
		if _, err := s.Identity(ctx, "google", "1"); !errors.Is(err, accounts.ErrNotFound) {
			t.Errorf("its identity after DeleteAccount: %v, want ErrNotFound", err)
		}
		if _, err := s.Session(ctx, "k1"); !errors.Is(err, accounts.ErrNotFound) {
			t.Errorf("its session after DeleteAccount: %v, want ErrNotFound", err)
		}
		if _, err := s.Identity(ctx, "google", "2"); err != nil {
			t.Errorf("another account's identity after DeleteAccount: %v", err)
		}
		if _, err := s.Session(ctx, "k2"); err != nil {
			t.Errorf("another account's session after DeleteAccount: %v", err)
		}
	})

	t.Run("Challenges", func(t *testing.T) {
		s := newStore(t)
		c := accounts.Challenge{Purpose: "signin", Hash: "h", Data: map[string]string{"account": "a1"}, Expires: t0.Add(10 * time.Minute)}
		must(t, s.PutChallenge(ctx, "c1", c))
		got, err := s.Challenge(ctx, "c1")
		must(t, err)
		equal(t, "Challenge", got, c)
		for want := 1; want <= 3; want++ {
			got, err := s.AttemptChallenge(ctx, "c1")
			must(t, err)
			if got.Attempts != want || got.Purpose != "signin" || got.Data["account"] != "a1" {
				t.Fatalf("AttemptChallenge #%d = %+v", want, got)
			}
		}
		if _, err := s.AttemptChallenge(ctx, "nope"); !errors.Is(err, accounts.ErrNotFound) {
			t.Errorf("AttemptChallenge(missing): %v, want ErrNotFound", err)
		}
		// Put replaces, attempts and all.
		must(t, s.PutChallenge(ctx, "c1", c))
		got, err = s.Challenge(ctx, "c1")
		must(t, err)
		if got.Attempts != 0 {
			t.Errorf("Attempts after PutChallenge replaced it = %d", got.Attempts)
		}
		must(t, s.PutChallenge(ctx, "old", accounts.Challenge{Purpose: "otp", Expires: t0.Add(-time.Second)}))
		must(t, s.ExpireChallenges(ctx, t0))
		if _, err := s.Challenge(ctx, "old"); !errors.Is(err, accounts.ErrNotFound) {
			t.Errorf("expired challenge after ExpireChallenges: %v", err)
		}
		if _, err := s.Challenge(ctx, "c1"); err != nil {
			t.Errorf("live challenge after ExpireChallenges: %v", err)
		}
	})

	t.Run("ConcurrentAttemptsAreEachCounted", func(t *testing.T) {
		s := newStore(t)
		must(t, s.PutChallenge(ctx, "c", accounts.Challenge{Purpose: "otp", Expires: t0.Add(time.Hour)}))
		const n = 20
		seen := make(chan int, n)
		var wg sync.WaitGroup
		for range n {
			wg.Go(func() {
				c, err := s.AttemptChallenge(ctx, "c")
				if err != nil {
					t.Error(err)
					return
				}
				seen <- c.Attempts
			})
		}
		wg.Wait()
		close(seen)
		counts := map[int]bool{}
		for a := range seen {
			if counts[a] {
				t.Errorf("two attempts both saw count %d", a)
			}
			counts[a] = true
		}
		if len(counts) != n {
			t.Errorf("%d distinct counts, want %d", len(counts), n)
		}
	})

	t.Run("OnlyOneDeleteOfAChallengeSucceeds", func(t *testing.T) {
		s := newStore(t)
		must(t, s.PutChallenge(ctx, "c", accounts.Challenge{Purpose: "signin", Expires: t0.Add(time.Hour)}))
		const n = 10
		var wg sync.WaitGroup
		var mu sync.Mutex
		ok := 0
		for range n {
			wg.Go(func() {
				err := s.DeleteChallenge(ctx, "c")
				switch {
				case err == nil:
					mu.Lock()
					ok++
					mu.Unlock()
				case !errors.Is(err, accounts.ErrNotFound):
					t.Error(err)
				}
			})
		}
		wg.Wait()
		if ok != 1 {
			t.Errorf("%d deletes succeeded, want 1", ok)
		}
	})
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func equal(t *testing.T, what string, got, want any) {
	t.Helper()
	if d := cmp.Diff(want, got); d != "" {
		t.Errorf("%s (-want +got):\n%s", what, d)
	}
}

// String makes failures readable.
func (m Message) String() string { return fmt.Sprintf("to %s: %q", m.To, m.Text) }
