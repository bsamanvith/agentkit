package backend

import (
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/bsamanvith/agentkit/pkg/store"
)

var toolsetName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// CheckToolsetName refuses a name that breaks the rule on
// [store.ToolsetRecord].
func CheckToolsetName(name string) error {
	if !toolsetName.MatchString(name) {
		return fmt.Errorf("%w: toolset name %q must match %s", store.ErrInvalid, name, toolsetName)
	}
	return nil
}

// CheckToolset checks t and ifVersion for [store.ToolsetStore.PutToolset]
// against the rules on [store.ToolsetRecord]. It returns a copy with only
// Name and Data, sharing no memory with t. Errors match [store.ErrInvalid].
func CheckToolset(t store.ToolsetRecord, ifVersion int64) (store.ToolsetRecord, error) {
	if err := CheckToolsetName(t.Name); err != nil {
		return store.ToolsetRecord{}, err
	}
	switch {
	case ifVersion < 0:
		return store.ToolsetRecord{}, fmt.Errorf("%w: toolset %q: negative version %d", store.ErrInvalid, t.Name, ifVersion)
	case len(t.Data) == 0:
		return store.ToolsetRecord{}, fmt.Errorf("%w: toolset %q has no data", store.ErrInvalid, t.Name)
	case len(t.Data) > store.MaxToolsetBytes:
		return store.ToolsetRecord{}, fmt.Errorf("%w: toolset %q: data of %d bytes, more than %d", store.ErrInvalid, t.Name, len(t.Data), store.MaxToolsetBytes)
	case !json.Valid(t.Data):
		return store.ToolsetRecord{}, fmt.Errorf("%w: toolset %q: data is not valid JSON", store.ErrInvalid, t.Name)
	}
	if err := CheckTextBytes("toolset data", t.Data); err != nil {
		return store.ToolsetRecord{}, err
	}
	return store.ToolsetRecord{Name: t.Name, Data: clone(t.Data)}, nil
}

// CheckDeleteToolset checks the arguments of
// [store.ToolsetStore.DeleteToolset].
func CheckDeleteToolset(name string, ifVersion int64) error {
	if err := CheckToolsetName(name); err != nil {
		return err
	}
	if ifVersion < 0 {
		return fmt.Errorf("%w: toolset %q: negative version %d", store.ErrInvalid, name, ifVersion)
	}
	return nil
}

// PutToolsetError is the error of a put of version ifVersion of a toolset
// whose stored version is have (0: none stored), or nil when the put may go
// ahead.
func PutToolsetError(name string, ifVersion, have int64) error {
	switch {
	case ifVersion == 0 && have != 0:
		return fmt.Errorf("%w: toolset %q exists", store.ErrConflict, name)
	case ifVersion != 0 && have == 0:
		return fmt.Errorf("%w: toolset %q", store.ErrNotFound, name)
	case ifVersion != have:
		return fmt.Errorf("%w: toolset %q is at version %d, not %d", store.ErrConflict, name, have, ifVersion)
	}
	return nil
}

// DeleteToolsetError is [PutToolsetError] for a delete: ifVersion 0 removes
// any version.
func DeleteToolsetError(name string, ifVersion, have int64) error {
	switch {
	case have == 0:
		return fmt.Errorf("%w: toolset %q", store.ErrNotFound, name)
	case ifVersion != 0 && ifVersion != have:
		return fmt.Errorf("%w: toolset %q is at version %d, not %d", store.ErrConflict, name, have, ifVersion)
	}
	return nil
}
