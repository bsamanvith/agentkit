package store

import (
	"context"
	"encoding/json"
	"time"
)

// MaxToolsetBytes is the largest [ToolsetRecord.Data] a store keeps.
const MaxToolsetBytes = 1 << 20

// ToolsetRecord is a toolset added at run time (T096), as a tenant's data
// rather than the config file: what the toolset is and the review of its
// tools, as JSON the store does not interpret.
//
// Rules, enforced by PutToolset ([ErrInvalid] otherwise): Name matches
// ^[a-zA-Z0-9_-]{1,64}$; Data is valid JSON in UTF-8 (see Text in the
// package documentation), not empty, of at most [MaxToolsetBytes].
type ToolsetRecord struct {
	// Name is the toolset's name, unique in its tenant.
	Name string
	// Data is the toolset's JSON, opaque to the store and kept byte for byte.
	Data json.RawMessage
	// Version is set by the store: 1 when the toolset is created, one more
	// on each replace. The value passed to PutToolset is ignored.
	Version int64
	// CreatedAt and UpdatedAt are set by the store: when the toolset was
	// created, and when it was last written.
	CreatedAt, UpdatedAt time.Time
}

// ToolsetStore keeps the toolsets added at run time, per tenant. Writes are
// compare-and-swap on [ToolsetRecord.Version], so two admins editing one
// toolset cannot lose each other's change.
//
// Implementations must be safe for concurrent use, must honor ctx, must scope
// every call by the tenant in ctx, and must not keep or share memory with
// values passed in or returned. Run storetest.RunToolsetStore against every
// implementation.
type ToolsetStore interface {
	// PutToolset creates the toolset when ifVersion is 0, failing with
	// [ErrConflict] if one has its name, or replaces version ifVersion of
	// it, failing with [ErrNotFound] if there is none and [ErrConflict] if
	// its version is another. It returns the toolset as stored. A record
	// that breaks the rules on [ToolsetRecord], or a negative ifVersion, is
	// [ErrInvalid].
	PutToolset(ctx context.Context, t ToolsetRecord, ifVersion int64) (ToolsetRecord, error)
	// GetToolset returns the toolset, or [ErrNotFound].
	GetToolset(ctx context.Context, name string) (ToolsetRecord, error)
	// ListToolsets returns every toolset of the tenant, sorted by name.
	ListToolsets(ctx context.Context) ([]ToolsetRecord, error)
	// DeleteToolset removes the toolset: whatever its version when
	// ifVersion is 0, else only version ifVersion ([ErrConflict] for
	// another). A toolset that does not exist is [ErrNotFound].
	DeleteToolset(ctx context.Context, name string, ifVersion int64) error
}
