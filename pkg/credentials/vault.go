package credentials

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Errors of a vault and its backend, checked with errors.Is.
var (
	// ErrNotFound reports no credential at a key.
	ErrNotFound = errors.New("credentials: not found")
	// ErrExists reports a create (ifVersion 0) where a credential exists.
	ErrExists = errors.New("credentials: already exists")
	// ErrConflict reports a write whose ifVersion is not the current one
	// (including a credential that no longer exists): re-read and retry.
	ErrConflict = errors.New("credentials: version conflict")
	// ErrInvalidCredential reports a credential with nothing to store.
	ErrInvalidCredential = errors.New("credentials: invalid credential")
)

// Meta describes a stored credential. It never holds a secret, so it can be
// listed, logged and returned by an API.
type Meta struct {
	Slot string `json:"slot"`
	User string `json:"user,omitempty"`
	Kind Kind   `json:"kind"`
	// Version starts at 1 and grows with every write; a key's versions
	// never repeat, even after a delete.
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Expiry is when the credential stops working, when it says (an OAuth2
	// access token).
	Expiry time.Time `json:"expiry,omitzero"`
	// NeedsReconnect marks a credential that can no longer be refreshed
	// (T044): a person has to connect the account again.
	NeedsReconnect bool `json:"needs_reconnect,omitempty"`
}

// Vault stores credentials for the tenant of the context. Implementations
// are safe for concurrent use. A context with no tenant fails with
// tenancy.ErrNoTenant.
type Vault interface {
	// Get returns the credential at (user, slot), or [ErrNotFound].
	Get(ctx context.Context, user, slot string) (Credential, Meta, error)
	// Put stores cred at (user, slot). ifVersion 0 creates, failing with
	// [ErrExists] if there is one; n replaces version n, failing with
	// [ErrConflict] if the current version is another or there is none.
	// A write clears NeedsReconnect.
	Put(ctx context.Context, user, slot string, cred Credential, ifVersion int64) (Meta, error)
	// Delete removes the credential at (user, slot): any version when
	// ifVersion is 0, else only version ifVersion ([ErrConflict]). No
	// credential is [ErrNotFound].
	Delete(ctx context.Context, user, slot string, ifVersion int64) error
	// List returns the metadata of user's credentials ("" lists the
	// tenant's shared ones), by slot.
	List(ctx context.Context, user string) ([]Meta, error)
}

// Blob is what a [BlobStore] keeps at a key: the sealed bytes and the
// metadata, which the store may read.
type Blob struct {
	Meta
	Data []byte
}

// BlobStore is a vault's storage. It never sees plaintext. The tenant comes
// from the context, as for [Vault]; the vault has already checked user and
// slot. Versions, times and the compare-and-swap rules are the store's: Put
// sets Version (1 on the first write of a key, then one more than the last
// version the key ever had), CreatedAt (kept from the first write while the
// credential exists) and UpdatedAt (to the blob's UpdatedAt, or CreatedAt
// when that is later: clocks of replicas differ); it ignores the other two. Get, Put and List return copies the caller may keep.
type BlobStore interface {
	Get(ctx context.Context, user, slot string) (Blob, error)
	Put(ctx context.Context, user, slot string, b Blob, ifVersion int64) (Meta, error)
	Delete(ctx context.Context, user, slot string, ifVersion int64) error
	// List returns user's blobs by slot, without Data.
	List(ctx context.Context, user string) ([]Meta, error)
}

// VaultOption configures [NewVault].
type VaultOption func(*vault)

// WithNow sets the clock UpdatedAt is read from (default time.Now).
func WithNow(now func() time.Time) VaultOption { return func(v *vault) { v.now = now } }

// NewVault returns a vault that seals with sealer and stores in store.
func NewVault(store BlobStore, sealer *Sealer, opts ...VaultOption) Vault {
	v := &vault{store: store, sealer: sealer, now: time.Now}
	for _, o := range opts {
		o(v)
	}
	return v
}

type vault struct {
	store  BlobStore
	sealer *Sealer
	now    func() time.Time
}

func (v *vault) Get(ctx context.Context, user, slot string) (Credential, Meta, error) {
	key, err := keyOf(ctx, user, slot)
	if err != nil {
		return nil, Meta{}, err
	}
	b, err := v.store.Get(ctx, user, slot)
	if err != nil {
		return nil, Meta{}, err
	}
	plain, _, err := v.sealer.Open(key, b.Data)
	if err != nil {
		return nil, Meta{}, fmt.Errorf("credential %q: %w", slot, err)
	}
	defer clear(plain)
	cred, err := decode(plain)
	if err != nil || cred.Kind() != b.Kind {
		return nil, Meta{}, fmt.Errorf("credential %q: %w", slot, ErrCorrupt)
	}
	return cred, b.Meta, nil
}

func (v *vault) Put(ctx context.Context, user, slot string, cred Credential, ifVersion int64) (Meta, error) {
	key, err := keyOf(ctx, user, slot)
	if err != nil {
		return Meta{}, err
	}
	if err := check(cred); err != nil {
		return Meta{}, err
	}
	if ifVersion < 0 {
		return Meta{}, fmt.Errorf("%w: ifVersion %d", ErrConflict, ifVersion)
	}
	plain, err := encode(cred)
	if err != nil {
		return Meta{}, err
	}
	defer clear(plain)
	sealed, err := v.sealer.Seal(key, plain)
	if err != nil {
		return Meta{}, err
	}
	return v.store.Put(ctx, user, slot, Blob{
		Meta: Meta{Slot: slot, User: user, Kind: cred.Kind(), UpdatedAt: v.now().UTC(), Expiry: expiry(cred).UTC()},
		Data: sealed,
	}, ifVersion)
}

func (v *vault) Delete(ctx context.Context, user, slot string, ifVersion int64) error {
	if _, err := keyOf(ctx, user, slot); err != nil {
		return err
	}
	if ifVersion < 0 {
		return fmt.Errorf("%w: ifVersion %d", ErrConflict, ifVersion)
	}
	return v.store.Delete(ctx, user, slot, ifVersion)
}

func (v *vault) List(ctx context.Context, user string) ([]Meta, error) {
	if _, err := keyOf(ctx, user, "x"); err != nil {
		return nil, err
	}
	return v.store.List(ctx, user)
}
