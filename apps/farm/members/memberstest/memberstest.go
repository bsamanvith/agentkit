// Package memberstest has an in-memory members.Store ([Memory]) and the
// conformance suite every store passes ([RunStore]).
package memberstest

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/bsamanvith/agentkit/apps/farm/members"
)

// Memory is a members.Store in memory. The zero value is ready to use.
type Memory struct {
	mu       sync.Mutex
	holdings map[string]members.Holding
	members  []members.Member
}

var _ members.Store = (*Memory)(nil)

// CreateHolding implements members.Store.
func (m *Memory) CreateHolding(_ context.Context, h members.Holding) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.holdings == nil {
		m.holdings = map[string]members.Holding{}
	}
	if _, ok := m.holdings[h.ID]; ok {
		return members.ErrExists
	}
	m.holdings[h.ID] = h
	return nil
}

// Holding implements members.Store.
func (m *Memory) Holding(_ context.Context, id string) (members.Holding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.holdings[id]
	if !ok {
		return members.Holding{}, members.ErrNotFound
	}
	return h, nil
}

// UpdateHolding implements members.Store.
func (m *Memory) UpdateHolding(_ context.Context, h members.Holding) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.holdings[h.ID]; !ok {
		return members.ErrNotFound
	}
	m.holdings[h.ID] = h
	return nil
}

// CountHoldings implements members.Store.
func (m *Memory) CountHoldings(context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.holdings), nil
}

// PutMember implements members.Store.
func (m *Memory) PutMember(_ context.Context, mem members.Member) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, x := range m.members {
		if x.Holding == mem.Holding && x.Account == mem.Account {
			m.members[i] = mem
			return nil
		}
	}
	m.members = append(m.members, mem)
	return nil
}

// Member implements members.Store.
func (m *Memory) Member(_ context.Context, holding, account string) (members.Member, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range m.members {
		if x.Holding == holding && x.Account == account {
			return x, nil
		}
	}
	return members.Member{}, members.ErrNotFound
}

func (m *Memory) filter(keep func(members.Member) bool) []members.Member {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []members.Member{}
	for _, x := range m.members {
		if keep(x) {
			out = append(out, x)
		}
	}
	slices.SortStableFunc(out, func(a, b members.Member) int { return a.AddedAt.Compare(b.AddedAt) })
	return out
}

// Members implements members.Store.
func (m *Memory) Members(_ context.Context, holding string) ([]members.Member, error) {
	return m.filter(func(x members.Member) bool { return x.Holding == holding }), nil
}

// Memberships implements members.Store.
func (m *Memory) Memberships(_ context.Context, account string) ([]members.Member, error) {
	return m.filter(func(x members.Member) bool { return x.Account == account }), nil
}

// RemoveMember implements members.Store.
func (m *Memory) RemoveMember(_ context.Context, holding, account string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, x := range m.members {
		if x.Holding == holding && x.Account == account {
			m.members = slices.Delete(m.members, i, i+1)
			return nil
		}
	}
	return members.ErrNotFound
}

// RunStore runs the conformance suite on stores newStore makes, a fresh
// empty one per subtest.
func RunStore(t *testing.T, newStore func(t *testing.T) members.Store) {
	ctx := context.Background()
	t0 := time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC)

	t.Run("Holdings", func(t *testing.T) {
		s := newStore(t)
		if n, err := s.CountHoldings(ctx); err != nil || n != 0 {
			t.Fatalf("CountHoldings of an empty store = %d, %v", n, err)
		}
		h := members.Holding{ID: "h1", Name: "Ravi's farm", CreatedAt: t0}
		must(t, s.CreateHolding(ctx, h))
		if err := s.CreateHolding(ctx, h); !errors.Is(err, members.ErrExists) {
			t.Errorf("CreateHolding twice: %v, want ErrExists", err)
		}
		got, err := s.Holding(ctx, "h1")
		must(t, err)
		equal(t, "Holding", got, h)
		h.Name = "Home farm"
		must(t, s.UpdateHolding(ctx, h))
		got, _ = s.Holding(ctx, "h1")
		equal(t, "updated Holding", got, h)
		if err := s.UpdateHolding(ctx, members.Holding{ID: "nope"}); !errors.Is(err, members.ErrNotFound) {
			t.Errorf("UpdateHolding(missing): %v", err)
		}
		if _, err := s.Holding(ctx, "nope"); !errors.Is(err, members.ErrNotFound) {
			t.Errorf("Holding(missing): %v", err)
		}
		must(t, s.CreateHolding(ctx, members.Holding{ID: "h2", Name: "B", CreatedAt: t0}))
		if n, _ := s.CountHoldings(ctx); n != 2 {
			t.Errorf("CountHoldings = %d, want 2", n)
		}
	})

	t.Run("Members", func(t *testing.T) {
		s := newStore(t)
		owner := members.Member{Holding: "h1", Account: "a1", Role: members.Owner, AddedAt: t0}
		fam := members.Member{Holding: "h1", Account: "a2", Role: members.Family, AddedBy: "a1", AddedAt: t0.Add(time.Minute)}
		other := members.Member{Holding: "h2", Account: "a2", Role: members.Owner, AddedAt: t0.Add(-time.Minute)}
		must(t, s.PutMember(ctx, fam))
		must(t, s.PutMember(ctx, owner))
		must(t, s.PutMember(ctx, other))
		got, err := s.Member(ctx, "h1", "a2")
		must(t, err)
		equal(t, "Member", got, fam)
		list, _ := s.Members(ctx, "h1")
		equal(t, "Members first added first", list, []members.Member{owner, fam})
		mine, _ := s.Memberships(ctx, "a2")
		equal(t, "Memberships first added first", mine, []members.Member{other, fam})
		fam.Role = members.Worker
		must(t, s.PutMember(ctx, fam))
		got, _ = s.Member(ctx, "h1", "a2")
		equal(t, "Member after a new role", got, fam)
		if list, _ := s.Members(ctx, "h1"); len(list) != 2 {
			t.Errorf("PutMember of an existing member added a row: %v", list)
		}
		must(t, s.RemoveMember(ctx, "h1", "a2"))
		if _, err := s.Member(ctx, "h1", "a2"); !errors.Is(err, members.ErrNotFound) {
			t.Errorf("Member after RemoveMember: %v", err)
		}
		if err := s.RemoveMember(ctx, "h1", "a2"); !errors.Is(err, members.ErrNotFound) {
			t.Errorf("RemoveMember twice: %v", err)
		}
		if _, err := s.Member(ctx, "h2", "a2"); err != nil {
			t.Errorf("the account's other membership went too: %v", err)
		}
		if none, err := s.Members(ctx, "nope"); err != nil || len(none) != 0 {
			t.Errorf("Members(no holding) = %v, %v", none, err)
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
