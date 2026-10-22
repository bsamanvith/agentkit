package memory

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"
	"unicode"
	"unicode/utf8"
)

// Index is a search store: many items, found by a query. It is for recall
// over past conversations, documents and facts, where a [Store] is for a few
// notes read whole. Like a Store, every call is scoped by the tenant (and,
// with [ScopeUser], the user) in ctx. Implementations must be safe for
// concurrent use.
type Index interface {
	// Put adds an item, or replaces the one with its ID. It fails with
	// ErrInvalid for an item [CheckItem] refuses, and with ErrLimit when a
	// new item would pass the scope's MaxItems.
	Put(ctx context.Context, it Item) error
	// Search returns at most q.K items that match q, best first: by score,
	// then the newest, then by ID, so ties come in a stable order. A mode
	// the backend lacks is ErrModeUnsupported, never results of another
	// mode. In ModeKeyword, scores are BM25 over the scope's own items
	// (pkg/memory/internal/keyword), so every backend returns the same
	// results.
	Search(ctx context.Context, q Query) ([]Result, error)
	// Get returns the item with the ID, or ErrItemNotFound.
	Get(ctx context.Context, id string) (Item, error)
	// Delete removes the item with the ID, or fails with ErrItemNotFound.
	Delete(ctx context.Context, id string) error
	// Modes lists the modes the backend searches in, its default first.
	Modes() []Mode
}

// Item is one thing an Index holds.
type Item struct {
	// ID names the item in its scope: 1 to MaxIDBytes bytes of UTF-8,
	// with no control characters.
	ID string
	// Text is what is searched and recalled: non-empty UTF-8, at most the
	// index's MaxTextBytes.
	Text string
	// Metadata are exact-match labels a query can filter on (source,
	// conversation, kind): at most MaxMetadataPairs, each key 1 to
	// MaxMetadataKeyBytes bytes and each value at most
	// MaxMetadataValueBytes. Empty metadata reads back as nil.
	Metadata map[string]string
	// Time is when the item was written or happened; zero for unknown.
	// It reads back in UTC, to the nanosecond, without a monotonic
	// reading.
	Time time.Time
}

// Clone returns a copy of it that shares no memory with it.
func (it Item) Clone() Item {
	if len(it.Metadata) == 0 {
		it.Metadata = nil
	} else {
		it.Metadata = maps.Clone(it.Metadata)
	}
	return it
}

// Mode is how a query is matched.
type Mode string

// The modes.
const (
	// ModeKeyword matches the words and quoted phrases of the query
	// ([ParseKeywordQuery]) and ranks by BM25.
	ModeKeyword Mode = "keyword"
	// ModeSemantic matches by meaning, through embeddings (T060).
	ModeSemantic Mode = "semantic"
	// ModeHybrid fuses the keyword and the semantic rankings (T060).
	ModeHybrid Mode = "hybrid"
)

// Query is a search.
type Query struct {
	// Text is what to look for: at most MaxQueryBytes of UTF-8. In
	// ModeKeyword, see [ParseKeywordQuery] for its syntax.
	Text string
	// K is how many results at most: 0 for DefaultK, at most MaxK.
	K int
	// Filter keeps only items whose metadata has every one of its keys
	// with exactly its value (no case folding, no prefixes). Nil or empty
	// keeps every item.
	Filter map[string]string
	// Mode is how to match; empty for the backend's default (the first of
	// its Modes).
	Mode Mode
}

// Result is one item a search found.
type Result struct {
	Item Item
	// Score ranks the results: higher is better. Scores compare only
	// within one search.
	Score float64
	// Mode is the mode that found the item.
	Mode Mode
	// Highlights are the parts of the query the item matched: its phrases
	// (their words joined by a space), then its words, each in the
	// query's order. Nil when the mode does not say.
	Highlights []string
}

// The limits of items and queries.
const (
	MaxIDBytes            = 128
	MaxMetadataPairs      = 16
	MaxMetadataKeyBytes   = 64
	MaxMetadataValueBytes = 256
	MaxQueryBytes         = 1024
	// MaxQueryParts caps the words and phrases of a keyword query.
	MaxQueryParts = 32
	DefaultK      = 5
	MaxK          = 50
	// DefaultMaxTextBytes is the default of IndexConfig.MaxTextBytes.
	DefaultMaxTextBytes = 32 << 10
	// DefaultMaxItems is the default of IndexConfig.MaxItems.
	DefaultMaxItems = 10_000
)

// IndexConfig configures an Index backend.
type IndexConfig struct {
	// Scope is ScopeTenant (the default, when empty) or ScopeUser.
	Scope Scope
	// MaxTextBytes caps one item's text (default DefaultMaxTextBytes).
	MaxTextBytes int
	// MaxItems caps how many items a scope holds (default
	// DefaultMaxItems).
	MaxItems int
}

// Resolve returns c with its defaults filled in, or ErrInvalid for a scope it
// does not know or a negative limit.
func (c IndexConfig) Resolve() (IndexConfig, error) {
	sc, err := Config{Scope: c.Scope}.Resolve()
	if err != nil {
		return c, err
	}
	c.Scope = sc.Scope
	if c.MaxTextBytes < 0 || c.MaxItems < 0 {
		return c, fmt.Errorf("%w: negative limit", ErrInvalid)
	}
	if c.MaxTextBytes == 0 {
		c.MaxTextBytes = DefaultMaxTextBytes
	}
	if c.MaxItems == 0 {
		c.MaxItems = DefaultMaxItems
	}
	return c, nil
}

// The errors of an index, besides the ones it shares with a Store
// (ErrNoTenant, ErrNoUser, ErrInvalid, ErrLimit). Check them with errors.Is.
var (
	// ErrModeUnsupported is a query in a mode the backend does not have.
	ErrModeUnsupported = errors.New("memory: search mode not supported by this index")
	// ErrItemNotFound is an ID with no item in the scope.
	ErrItemNotFound = errors.New("memory: no item with this id")
)

// CheckItem returns it ready to store (its metadata cloned, its time in UTC
// without a monotonic reading), or ErrInvalid for an item that breaks a
// rule of [Item] with c's MaxTextBytes. c must be resolved.
func CheckItem(it Item, c IndexConfig) (Item, error) {
	if err := checkID(it.ID); err != nil {
		return Item{}, err
	}
	switch {
	case it.Text == "":
		return Item{}, fmt.Errorf("%w: empty text", ErrInvalid)
	case !utf8.ValidString(it.Text):
		return Item{}, fmt.Errorf("%w: text is not UTF-8", ErrInvalid)
	case len(it.Text) > c.MaxTextBytes:
		return Item{}, &LimitError{Limit: "text bytes", Max: int64(c.MaxTextBytes), Want: int64(len(it.Text))}
	}
	if err := checkLabels("metadata", it.Metadata); err != nil {
		return Item{}, err
	}
	if !it.Time.IsZero() {
		// The range a SQLite backend stores in nanoseconds since 1970.
		if it.Time.Year() < 1678 || it.Time.Year() > 2261 {
			return Item{}, fmt.Errorf("%w: time %v out of range", ErrInvalid, it.Time)
		}
		it.Time = it.Time.Round(0).UTC()
	}
	return it.Clone(), nil
}

// CheckID returns ErrInvalid for an ID that breaks the rule of Item.ID.
func CheckID(id string) error { return checkID(id) }

func checkID(id string) error {
	switch {
	case id == "":
		return fmt.Errorf("%w: empty id", ErrInvalid)
	case len(id) > MaxIDBytes:
		return fmt.Errorf("%w: id longer than %d bytes", ErrInvalid, MaxIDBytes)
	case !utf8.ValidString(id) || hasControl(id):
		return fmt.Errorf("%w: id has a control character or is not UTF-8", ErrInvalid)
	}
	return nil
}

func checkLabels(what string, m map[string]string) error {
	if len(m) > MaxMetadataPairs {
		return fmt.Errorf("%w: %s has %d pairs, at most %d", ErrInvalid, what, len(m), MaxMetadataPairs)
	}
	for k, v := range m {
		switch {
		case k == "" || len(k) > MaxMetadataKeyBytes:
			return fmt.Errorf("%w: %s key %q: want 1 to %d bytes", ErrInvalid, what, k, MaxMetadataKeyBytes)
		case len(v) > MaxMetadataValueBytes:
			return fmt.Errorf("%w: %s value of %q longer than %d bytes", ErrInvalid, what, k, MaxMetadataValueBytes)
		case !utf8.ValidString(k) || !utf8.ValidString(v):
			return fmt.Errorf("%w: %s is not UTF-8", ErrInvalid, what)
		}
	}
	return nil
}

func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// ResolveQuery returns q ready to run on a backend with modes (its default
// first): its mode and K filled in, its filter cloned. A mode the backend
// lacks is ErrModeUnsupported, checked first; a mode no backend knows, a K
// out of [0, MaxK], a text that is empty, too long or not UTF-8, or a bad
// filter is ErrInvalid.
func ResolveQuery(q Query, modes []Mode) (Query, error) {
	switch q.Mode {
	case "":
		if len(modes) == 0 {
			return q, fmt.Errorf("%w: the index has no mode", ErrModeUnsupported)
		}
		q.Mode = modes[0]
	case ModeKeyword, ModeSemantic, ModeHybrid:
		if !slices.Contains(modes, q.Mode) {
			return q, fmt.Errorf("%w: %q (this index has %v)", ErrModeUnsupported, q.Mode, modes)
		}
	default:
		return q, fmt.Errorf("%w: mode %q: want %q, %q or %q", ErrInvalid, q.Mode, ModeKeyword, ModeSemantic, ModeHybrid)
	}
	switch {
	case q.K < 0 || q.K > MaxK:
		return q, fmt.Errorf("%w: k %d: want 0 to %d", ErrInvalid, q.K, MaxK)
	case q.K == 0:
		q.K = DefaultK
	}
	switch {
	case q.Text == "":
		return q, fmt.Errorf("%w: empty query", ErrInvalid)
	case !utf8.ValidString(q.Text):
		return q, fmt.Errorf("%w: query is not UTF-8", ErrInvalid)
	case len(q.Text) > MaxQueryBytes:
		return q, fmt.Errorf("%w: query longer than %d bytes", ErrInvalid, MaxQueryBytes)
	}
	if err := checkLabels("filter", q.Filter); err != nil {
		return q, err
	}
	if len(q.Filter) == 0 {
		q.Filter = nil
	} else {
		q.Filter = maps.Clone(q.Filter)
	}
	return q, nil
}

// MatchesFilter reports whether metadata has every key of filter with
// exactly its value.
func MatchesFilter(metadata, filter map[string]string) bool {
	for k, v := range filter {
		if got, ok := metadata[k]; !ok || got != v {
			return false
		}
	}
	return true
}
