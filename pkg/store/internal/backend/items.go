package backend

import (
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/bsamanvith/agentkit/pkg/store"
)

var itemKind = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// CheckItemKey refuses a kind or name that breaks the rules on
// [store.ItemVersion].
func CheckItemKey(kind, name string) error {
	if !itemKind.MatchString(kind) {
		return fmt.Errorf("%w: item kind %q must match %s", store.ErrInvalid, kind, itemKind)
	}
	if !toolsetName.MatchString(name) {
		return fmt.Errorf("%w: item name %q must match %s", store.ErrInvalid, name, toolsetName)
	}
	return nil
}

// CheckItemRef is [CheckItemKey] with a version, which must be positive.
func CheckItemRef(kind, name string, version int64) error {
	if err := CheckItemKey(kind, name); err != nil {
		return err
	}
	if version < 1 {
		return fmt.Errorf("%w: %s %q: version %d is not positive", store.ErrInvalid, kind, name, version)
	}
	return nil
}

// CheckItemVersion checks v and ifLatest for [store.ItemStore.SaveItem]
// against the rules on [store.ItemVersion]. It returns a copy with only
// Kind, Name, Data, Author and Message, sharing no memory with v. Errors
// match [store.ErrInvalid].
func CheckItemVersion(v store.ItemVersion, ifLatest int64) (store.ItemVersion, error) {
	if err := CheckItemKey(v.Kind, v.Name); err != nil {
		return store.ItemVersion{}, err
	}
	switch {
	case ifLatest < 0:
		return store.ItemVersion{}, fmt.Errorf("%w: %s %q: negative version %d", store.ErrInvalid, v.Kind, v.Name, ifLatest)
	case len(v.Data) == 0:
		return store.ItemVersion{}, fmt.Errorf("%w: %s %q has no data", store.ErrInvalid, v.Kind, v.Name)
	case len(v.Data) > store.MaxItemBytes:
		return store.ItemVersion{}, fmt.Errorf("%w: %s %q: data of %d bytes, more than %d", store.ErrInvalid, v.Kind, v.Name, len(v.Data), store.MaxItemBytes)
	case !json.Valid(v.Data):
		return store.ItemVersion{}, fmt.Errorf("%w: %s %q: data is not valid JSON", store.ErrInvalid, v.Kind, v.Name)
	case len(v.Author) > store.MaxItemMessageBytes || len(v.Message) > store.MaxItemMessageBytes:
		return store.ItemVersion{}, fmt.Errorf("%w: %s %q: author or message longer than %d bytes", store.ErrInvalid, v.Kind, v.Name, store.MaxItemMessageBytes)
	}
	if err := CheckTextBytes("item data", v.Data); err != nil {
		return store.ItemVersion{}, err
	}
	if err := CheckText("item author", v.Author); err != nil {
		return store.ItemVersion{}, err
	}
	if err := CheckText("item message", v.Message); err != nil {
		return store.ItemVersion{}, err
	}
	return store.ItemVersion{Kind: v.Kind, Name: v.Name, Data: clone(v.Data), Author: v.Author, Message: v.Message}, nil
}

// SaveItemError is the error of a save on top of version ifLatest of an
// item whose Latest is have (0: no item), or nil when the save may go ahead.
func SaveItemError(kind, name string, ifLatest, have int64) error {
	switch {
	case ifLatest == 0 && have != 0:
		return fmt.Errorf("%w: %s %q exists", store.ErrConflict, kind, name)
	case ifLatest != 0 && have == 0:
		return fmt.Errorf("%w: %s %q", store.ErrNotFound, kind, name)
	case ifLatest != have:
		return fmt.Errorf("%w: %s %q is at version %d, not %d", store.ErrConflict, kind, name, have, ifLatest)
	}
	return nil
}

// ItemNotFound is the error of an item that does not exist.
func ItemNotFound(kind, name string) error {
	return fmt.Errorf("%w: %s %q", store.ErrNotFound, kind, name)
}

// ItemVersionNotFound is the error of a version that does not exist.
func ItemVersionNotFound(kind, name string, version int64) error {
	return fmt.Errorf("%w: %s %q version %d", store.ErrNotFound, kind, name, version)
}

// ItemVersionPublished is the error of deleting a version that was
// published.
func ItemVersionPublished(kind, name string, version int64) error {
	return fmt.Errorf("%w: %s %q version %d was published; only drafts are deleted", store.ErrConflict, kind, name, version)
}
