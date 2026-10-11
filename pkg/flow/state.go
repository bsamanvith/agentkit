package flow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sync"
)

// State is the data a flow passes from step to step: named values that
// round-trip through JSON, so the whole state can be saved in a checkpoint.
//
// Values are marshaled when they are set, so a stored value is a snapshot:
// changing the original afterwards changes nothing, and every [Get] returns a
// fresh copy. A State is safe for concurrent use, but [Parallel] gives each
// branch its own copy anyway.
type State struct {
	mu   sync.RWMutex
	vals map[string]json.RawMessage // canonical JSON, never modified in place
}

// NewState returns an empty State. The zero State is also ready to use.
func NewState() *State { return &State{vals: map[string]json.RawMessage{}} }

// Set stores v under key. It fails with [ErrInvalidValue] if key is empty or v
// does not marshal to JSON.
func Set[T any](s *State, key string, v T) error {
	if key == "" {
		return fmt.Errorf("%w: empty key", ErrInvalidValue)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("%w: %q: %w", ErrInvalidValue, key, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.vals == nil {
		s.vals = map[string]json.RawMessage{}
	}
	s.vals[key] = raw
	return nil
}

// Get decodes the value stored under key. A missing key is [ErrNotFound]; a
// value of another type is the JSON decoder's error.
func Get[T any](s *State, key string) (T, error) {
	var v T
	s.mu.RLock()
	raw, ok := s.vals[key]
	s.mu.RUnlock()
	if !ok {
		return v, fmt.Errorf("%w: key %q", ErrNotFound, key)
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, fmt.Errorf("state key %q: %w", key, err)
	}
	return v, nil
}

// Has reports whether key is set.
func (s *State) Has(key string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.vals[key]
	return ok
}

// Delete removes key. Deleting a missing key does nothing.
func (s *State) Delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.vals, key)
}

// Keys returns the keys in sorted order.
func (s *State) Keys() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Sorted(maps.Keys(s.vals))
}

// Clone returns an independent copy.
func (s *State) Clone() *State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return &State{vals: maps.Clone(s.vals)}
}

// MarshalJSON encodes the state as a JSON object with sorted keys.
func (s *State) MarshalJSON() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return json.Marshal(s.vals)
}

// UnmarshalJSON replaces the state with a decoded JSON object. Anything but an
// object is [ErrInvalidState], and the state is left unchanged.
func (s *State) UnmarshalJSON(data []byte) error {
	if t := bytes.TrimSpace(data); len(t) == 0 || t[0] != '{' {
		return fmt.Errorf("%w: not a JSON object", ErrInvalidState)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidState, err)
	}
	vals := make(map[string]json.RawMessage, len(raw))
	for k, v := range raw {
		if k == "" {
			return fmt.Errorf("%w: empty key", ErrInvalidState)
		}
		// Marshaling a RawMessage compacts it and escapes it the way Set's
		// output is escaped, so a decoded value is byte-for-byte what Set
		// would have produced. Parallel's merge compares bytes.
		canon, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("%w: key %q: %w", ErrInvalidState, k, err)
		}
		vals[k] = canon
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vals = vals
	return nil
}

// write is one branch's change to a key: a new value, or a deletion.
type write struct {
	raw     json.RawMessage
	deleted bool
}

// changesFrom returns the keys whose value differs from base, added, changed
// or deleted.
func (s *State) changesFrom(base *State) map[string]write {
	s.mu.RLock()
	defer s.mu.RUnlock()
	base.mu.RLock()
	defer base.mu.RUnlock()
	out := map[string]write{}
	for k, v := range s.vals {
		if old, ok := base.vals[k]; !ok || !bytes.Equal(old, v) {
			out[k] = write{raw: v}
		}
	}
	for k := range base.vals {
		if _, ok := s.vals[k]; !ok {
			out[k] = write{deleted: true}
		}
	}
	return out
}

// apply performs the writes.
func (s *State) apply(ws map[string]write) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.vals == nil {
		s.vals = map[string]json.RawMessage{}
	}
	for k, w := range ws {
		if w.deleted {
			delete(s.vals, k)
		} else {
			s.vals[k] = w.raw
		}
	}
}
