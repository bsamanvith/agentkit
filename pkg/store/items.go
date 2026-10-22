package store

import (
	"context"
	"encoding/json"
	"time"
)

// Item limits: the largest [ItemVersion.Data] a store keeps, and the
// longest [ItemVersion.Message] and [ItemVersion.Author], in bytes.
const (
	MaxItemBytes        = 1 << 20
	MaxItemMessageBytes = 4096
)

// ItemVersion is one version of a versioned item of a tenant (T075): an
// agent definition, say, as JSON the store does not interpret. A version
// never changes once saved; only whether it was published does.
//
// Rules, enforced by SaveItem ([ErrInvalid] otherwise): Kind matches
// ^[a-z][a-z0-9_]{0,31}$; Name matches ^[a-zA-Z0-9_-]{1,64}$; Data is valid
// JSON in UTF-8 (see Text in the package documentation), not empty, of at
// most [MaxItemBytes]; Author and Message are text of at most
// [MaxItemMessageBytes] each.
type ItemVersion struct {
	// Kind says what the item is ("agent"); Name names it, unique per kind
	// in its tenant.
	Kind, Name string
	// Version is set by the store: one more than the item's
	// [Item.Latest], so 1 for a new item. A number is never given twice,
	// even after a draft is deleted.
	Version int64
	// Data is the version's JSON, opaque to the store and kept byte for
	// byte.
	Data json.RawMessage
	// Author is who saved the version and Message why, for the record.
	Author, Message string
	// CreatedAt is set by the store when the version is saved.
	CreatedAt time.Time
	// PublishedAt is set by the store the first time the version is
	// published, and never changes after; zero for a draft that never was.
	// A version that was ever published cannot be deleted: runs may have
	// used it.
	PublishedAt time.Time
}

// Item is a versioned item: which of its versions is published, and the
// last number given.
type Item struct {
	Kind, Name string
	// Latest is the highest version number given so far.
	Latest int64
	// Published is the version that runs, 0 for an item with none yet.
	Published int64
	// CreatedAt is when the item's first version was saved; UpdatedAt when
	// a version was last saved or published.
	CreatedAt, UpdatedAt time.Time
}

// ItemStore keeps versioned items per tenant: each save is a new, immutable
// version, and one version per item is published. Saves are
// compare-and-swap on [Item.Latest], so two admins editing one item cannot
// lose each other's change.
//
// Implementations must be safe for concurrent use, must honor ctx, must scope
// every call by the tenant in ctx, and must not keep or share memory with
// values passed in or returned. Run storetest.RunItemStore against every
// implementation.
type ItemStore interface {
	// SaveItem stores v as the next version of its item: a new item when
	// ifLatest is 0 ([ErrConflict] if the item exists), else an item whose
	// Latest is ifLatest ([ErrNotFound] if there is none, [ErrConflict] if
	// its Latest is another). It returns the version as stored. A version
	// that breaks the rules on [ItemVersion], or a negative ifLatest, is
	// [ErrInvalid].
	SaveItem(ctx context.Context, v ItemVersion, ifLatest int64) (ItemVersion, error)
	// PublishItem makes version the item's published one, and returns the
	// item. A version that does not exist is [ErrNotFound]. Publishing the
	// published version again changes nothing.
	PublishItem(ctx context.Context, kind, name string, version int64) (Item, error)
	// GetItem returns an item, or [ErrNotFound].
	GetItem(ctx context.Context, kind, name string) (Item, error)
	// ListItems returns every item of a kind of the tenant, sorted by name.
	ListItems(ctx context.Context, kind string) ([]Item, error)
	// GetItemVersion returns a version of an item, or [ErrNotFound].
	GetItemVersion(ctx context.Context, kind, name string, version int64) (ItemVersion, error)
	// ListItemVersions returns every version of an item, oldest first, or
	// [ErrNotFound] for an item that does not exist.
	ListItemVersions(ctx context.Context, kind, name string) ([]ItemVersion, error)
	// DeleteItemVersion removes a draft: a version that was never
	// published ([ErrConflict] for one that was; [ErrNotFound] for one
	// that does not exist). An item left with no version is removed with
	// it, and its name is free again.
	DeleteItemVersion(ctx context.Context, kind, name string, version int64) error
}
