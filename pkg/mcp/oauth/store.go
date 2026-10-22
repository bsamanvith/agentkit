package oauth

import (
	"context"
	"slices"
	"sync"
	"time"
)

// TokenStore keeps connections. Implementations are safe for concurrent use.
// The vault (T042) is the real one; [MemoryStore] keeps them in memory.
type TokenStore interface {
	// Get returns the connection under k, or [ErrNotFound].
	Get(ctx context.Context, k Key) (Token, error)
	// Put stores t under k, replacing whatever is there (a new connection).
	Put(ctx context.Context, k Key, t Token) error
	// Swap replaces the connection under k with t only if the stored one
	// still has old's Version, and returns [ErrConflict] otherwise. This is
	// how two replicas refreshing at once keep one result.
	Swap(ctx context.Context, k Key, old, t Token) error
	// Delete removes the connection under k; a missing one is not an error.
	Delete(ctx context.Context, k Key) error
}

// Client is a client registered with an authorization server by dynamic
// registration, kept per tenant and issuer so every user of the tenant
// shares it.
type Client struct {
	ID        string
	Secret    Secret
	AuthStyle int // oauth2.AuthStyle
}

// ClientStore keeps dynamically registered clients.
type ClientStore interface {
	// GetClient returns the client registered for tenant at issuer, or
	// [ErrNotFound].
	GetClient(ctx context.Context, tenant, issuer string) (Client, error)
	// PutClient stores it.
	PutClient(ctx context.Context, tenant, issuer string, c Client) error
}

// Pending is a flow between [Connector.Begin] and [Connector.Complete].
type Pending struct {
	Owner     Owner
	Server    string
	ReturnTo  string
	Created   time.Time
	Expires   time.Time
	Verifier  Secret
	Resource  string
	Scopes    []string
	Issuer    string
	IssParam  bool // the issuer sends iss on the callback (RFC 9207)
	AuthURL   string
	TokenURL  string
	ClientID  string
	Secret    Secret
	AuthStyle int
	// AuthParams are the profile's extra authorization parameters.
	AuthParams map[string]string
}

// StateStore keeps pending flows by their state value.
type StateStore interface {
	// PutState stores p under state.
	PutState(ctx context.Context, state string, p Pending) error
	// TakeState returns and removes the flow under state, or [ErrNotFound]:
	// a state is used at most once.
	TakeState(ctx context.Context, state string) (Pending, error)
}

// MemoryStore is a [TokenStore], [ClientStore] and [StateStore] in memory,
// for one replica and for tests. Its zero value is ready to use.
type MemoryStore struct {
	mu      sync.Mutex
	tokens  map[Key]Token
	clients map[[2]string]Client
	states  map[string]Pending
	version int64
}

var (
	_ TokenStore  = (*MemoryStore)(nil)
	_ ClientStore = (*MemoryStore)(nil)
	_ StateStore  = (*MemoryStore)(nil)
)

func cloneToken(t Token) Token {
	t.Scopes = slices.Clone(t.Scopes)
	if t.Extra != nil {
		e := make(map[string]string, len(t.Extra))
		for k, v := range t.Extra {
			e[k] = v
		}
		t.Extra = e
	}
	return t
}

// Get implements [TokenStore].
func (m *MemoryStore) Get(_ context.Context, k Key) (Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tokens[k]
	if !ok {
		return Token{}, ErrNotFound
	}
	return cloneToken(t), nil
}

// Put implements [TokenStore].
func (m *MemoryStore) Put(_ context.Context, k Key, t Token) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.put(k, t)
	return nil
}

func (m *MemoryStore) put(k Key, t Token) {
	if m.tokens == nil {
		m.tokens = map[Key]Token{}
	}
	m.version++
	t = cloneToken(t)
	t.Version = m.version
	m.tokens[k] = t
}

// Swap implements [TokenStore].
func (m *MemoryStore) Swap(_ context.Context, k Key, old, t Token) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.tokens[k]
	if !ok || cur.Version != old.Version {
		return ErrConflict
	}
	m.put(k, t)
	return nil
}

// Delete implements [TokenStore].
func (m *MemoryStore) Delete(_ context.Context, k Key) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tokens, k)
	return nil
}

// GetClient implements [ClientStore].
func (m *MemoryStore) GetClient(_ context.Context, tenant, issuer string) (Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.clients[[2]string{tenant, issuer}]
	if !ok {
		return Client{}, ErrNotFound
	}
	return c, nil
}

// PutClient implements [ClientStore].
func (m *MemoryStore) PutClient(_ context.Context, tenant, issuer string, c Client) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.clients == nil {
		m.clients = map[[2]string]Client{}
	}
	m.clients[[2]string{tenant, issuer}] = c
	return nil
}

// PutState implements [StateStore]; it also drops flows that expired before p was created.
func (m *MemoryStore) PutState(_ context.Context, state string, p Pending) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.states == nil {
		m.states = map[string]Pending{}
	}
	// Drop flows that expired before this one began, so abandoned ones
	// don't pile up.
	for s, q := range m.states {
		if q.Expires.Before(p.Created) {
			delete(m.states, s)
		}
	}
	p.Scopes = slices.Clone(p.Scopes)
	m.states[state] = p
	return nil
}

// TakeState implements [StateStore].
func (m *MemoryStore) TakeState(_ context.Context, state string) (Pending, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.states[state]
	if !ok {
		return Pending{}, ErrNotFound
	}
	delete(m.states, state)
	return p, nil
}
