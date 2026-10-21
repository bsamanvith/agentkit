package login

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrNoSession is the error of a [Store] that has no session under a key.
var ErrNoSession = errors.New("no such session")

// Session is what a [Store] keeps of a session.
type Session struct {
	Username string
	// CSRF is the token an unsafe request of the session sends in
	// [CSRFHeader].
	CSRF string
	// Created is when the user signed in; LastSeen when the session last
	// authenticated a request (to within a minute).
	Created, LastSeen time.Time
}

// Store keeps sessions under keys (the SHA-256 of a session id, in hex: the
// store never sees an id). The default is [NewMemoryStore]; one on the
// database can replace it, so sessions outlive a restart and are shared by
// replicas. It is called concurrently.
type Store interface {
	// Create stores a session under key, which is new.
	Create(ctx context.Context, key string, s Session) error
	// Get returns the session under key, or an error matching ErrNoSession.
	Get(ctx context.Context, key string) (Session, error)
	// Touch sets the LastSeen of the session under key, or returns an error
	// matching ErrNoSession.
	Touch(ctx context.Context, key string, at time.Time) error
	// Delete removes the session under key; a missing one is not an error.
	Delete(ctx context.Context, key string) error
	// Expire removes every session last seen before idle or created before
	// created.
	Expire(ctx context.Context, idle, created time.Time) error
}

// MemoryStore is a [Store] in memory: sessions end with the process. It
// holds at most a fixed number of sessions, forgetting the least recently
// seen to make room.
type MemoryStore struct {
	mu       sync.Mutex
	max      int
	sessions map[string]Session
}

// NewMemoryStore returns an empty [MemoryStore] of at most max sessions
// (10,000 if max <= 0).
func NewMemoryStore(max int) *MemoryStore {
	if max <= 0 {
		max = 10_000
	}
	return &MemoryStore{max: max, sessions: map[string]Session{}}
}

// Create implements [Store].
func (m *MemoryStore) Create(_ context.Context, key string, s Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[key]; !ok && len(m.sessions) >= m.max {
		oldest := ""
		for k, v := range m.sessions {
			if oldest == "" || v.LastSeen.Before(m.sessions[oldest].LastSeen) {
				oldest = k
			}
		}
		delete(m.sessions, oldest)
	}
	m.sessions[key] = s
	return nil
}

// Get implements [Store].
func (m *MemoryStore) Get(_ context.Context, key string) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[key]
	if !ok {
		return Session{}, ErrNoSession
	}
	return s, nil
}

// Touch implements [Store].
func (m *MemoryStore) Touch(_ context.Context, key string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[key]
	if !ok {
		return ErrNoSession
	}
	s.LastSeen = at
	m.sessions[key] = s
	return nil
}

// Delete implements [Store].
func (m *MemoryStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, key)
	return nil
}

// Expire implements [Store].
func (m *MemoryStore) Expire(_ context.Context, idle, created time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, s := range m.sessions {
		if s.LastSeen.Before(idle) || s.Created.Before(created) {
			delete(m.sessions, k)
		}
	}
	return nil
}
