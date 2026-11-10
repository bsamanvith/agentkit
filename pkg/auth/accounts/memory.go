package accounts

import (
	"context"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"
)

// MemoryStore is a [Store] in memory: everything ends with the process.
// For tests and a single process that may forget.
type MemoryStore struct {
	mu         sync.Mutex
	accounts   map[string]Account
	identities map[[2]string]Identity
	sessions   map[string]Session
	challenges map[string]Challenge
}

var _ Store = (*MemoryStore)(nil)

// NewMemoryStore returns an empty [MemoryStore].
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		accounts: map[string]Account{}, identities: map[[2]string]Identity{},
		sessions: map[string]Session{}, challenges: map[string]Challenge{},
	}
}

func cloneAccount(a Account) Account {
	a.Roles = slices.Clone(a.Roles)
	return a
}

// CreateAccount implements [Store].
func (m *MemoryStore) CreateAccount(_ context.Context, a Account) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.accounts[a.ID]; ok {
		return ErrExists
	}
	m.accounts[a.ID] = cloneAccount(a)
	return nil
}

// Account implements [Store].
func (m *MemoryStore) Account(_ context.Context, id string) (Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.accounts[id]
	if !ok {
		return Account{}, ErrNotFound
	}
	return cloneAccount(a), nil
}

// UpdateAccount implements [Store].
func (m *MemoryStore) UpdateAccount(_ context.Context, a Account) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.accounts[a.ID]; !ok {
		return ErrNotFound
	}
	m.accounts[a.ID] = cloneAccount(a)
	return nil
}

// DeleteAccount implements [Store].
func (m *MemoryStore) DeleteAccount(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.accounts[id]; !ok {
		return ErrNotFound
	}
	delete(m.accounts, id)
	maps.DeleteFunc(m.identities, func(_ [2]string, v Identity) bool { return v.Account == id })
	maps.DeleteFunc(m.sessions, func(_ string, v Session) bool { return v.Account == id })
	return nil
}

// AddIdentity implements [Store].
func (m *MemoryStore) AddIdentity(_ context.Context, id Identity) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := [2]string{id.Lane, id.Subject}
	if _, ok := m.identities[k]; ok {
		return ErrExists
	}
	m.identities[k] = id
	return nil
}

// Identity implements [Store].
func (m *MemoryStore) Identity(_ context.Context, lane, subject string) (Identity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.identities[[2]string{lane, subject}]
	if !ok {
		return Identity{}, ErrNotFound
	}
	return id, nil
}

// Identities implements [Store].
func (m *MemoryStore) Identities(_ context.Context, account string) ([]Identity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Identity{}
	for _, id := range m.identities {
		if id.Account == account {
			out = append(out, id)
		}
	}
	slices.SortFunc(out, func(a, b Identity) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		if c := strings.Compare(a.Lane, b.Lane); c != 0 {
			return c
		}
		return strings.Compare(a.Subject, b.Subject)
	})
	return out, nil
}

// UpdateIdentity implements [Store].
func (m *MemoryStore) UpdateIdentity(_ context.Context, id Identity) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := [2]string{id.Lane, id.Subject}
	have, ok := m.identities[k]
	if !ok {
		return ErrNotFound
	}
	id.Account = have.Account
	m.identities[k] = id
	return nil
}

// RemoveIdentity implements [Store].
func (m *MemoryStore) RemoveIdentity(_ context.Context, lane, subject string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := [2]string{lane, subject}
	if _, ok := m.identities[k]; !ok {
		return ErrNotFound
	}
	delete(m.identities, k)
	return nil
}

// CreateSession implements [Store].
func (m *MemoryStore) CreateSession(_ context.Context, key string, s Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[key] = s
	return nil
}

// Session implements [Store].
func (m *MemoryStore) Session(_ context.Context, key string) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[key]
	if !ok {
		return Session{}, ErrNotFound
	}
	return s, nil
}

// TouchSession implements [Store].
func (m *MemoryStore) TouchSession(_ context.Context, key string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[key]
	if !ok {
		return ErrNotFound
	}
	s.LastSeen = at
	m.sessions[key] = s
	return nil
}

// DeleteSession implements [Store].
func (m *MemoryStore) DeleteSession(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, key)
	return nil
}

// DeleteSessions implements [Store].
func (m *MemoryStore) DeleteSessions(_ context.Context, account string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	maps.DeleteFunc(m.sessions, func(_ string, v Session) bool { return v.Account == account })
	return nil
}

// ExpireSessions implements [Store].
func (m *MemoryStore) ExpireSessions(_ context.Context, idle, created time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	maps.DeleteFunc(m.sessions, func(_ string, s Session) bool { return s.LastSeen.Before(idle) || s.Created.Before(created) })
	return nil
}

// PutChallenge implements [Store].
func (m *MemoryStore) PutChallenge(_ context.Context, key string, c Challenge) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c.Data = maps.Clone(c.Data)
	m.challenges[key] = c
	return nil
}

// Challenge implements [Store].
func (m *MemoryStore) Challenge(_ context.Context, key string) (Challenge, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.challenges[key]
	if !ok {
		return Challenge{}, ErrNotFound
	}
	c.Data = maps.Clone(c.Data)
	return c, nil
}

// AttemptChallenge implements [Store].
func (m *MemoryStore) AttemptChallenge(_ context.Context, key string) (Challenge, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.challenges[key]
	if !ok {
		return Challenge{}, ErrNotFound
	}
	c.Attempts++
	m.challenges[key] = c
	c.Data = maps.Clone(c.Data)
	return c, nil
}

// DeleteChallenge implements [Store].
func (m *MemoryStore) DeleteChallenge(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.challenges[key]; !ok {
		return ErrNotFound
	}
	delete(m.challenges, key)
	return nil
}

// ExpireChallenges implements [Store].
func (m *MemoryStore) ExpireChallenges(_ context.Context, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	maps.DeleteFunc(m.challenges, func(_ string, c Challenge) bool { return c.Expires.Before(now) })
	return nil
}
