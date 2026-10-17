package credentials

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Errors of the sealer, checked with errors.Is.
var (
	// ErrKeyConfig reports a key ring that cannot be used. Its message
	// never holds key material.
	ErrKeyConfig = errors.New("credentials: bad key ring")
	// ErrCorrupt reports a blob that does not open: changed, truncated, or
	// sealed for another tenant, user or slot.
	ErrCorrupt = errors.New("credentials: blob does not open")
	// ErrUnknownKey reports a blob sealed under a key id the ring does not
	// hold (a key removed too early, or a ring from another deployment).
	ErrUnknownKey = errors.New("credentials: blob sealed under an unknown key")
)

// KeySize is the size of a key: AES-256.
const KeySize = 32

var keyIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// KeyRing holds the keys a [Sealer] uses: the current one seals, any one
// opens. It is immutable.
type KeyRing struct {
	current string
	aeads   map[string]cipher.AEAD
	ids     []string // in the order given
}

// ParseKeyRing parses a key ring in the form AGENTKIT_CREDENTIALS_KEY takes:
// "<id>:<base64 of 32 bytes>[,<id>:<base64>...]", the first key current. Ids
// match ^[A-Za-z0-9_-]{1,32}$. Base64 may be standard or URL-safe, padded or
// not. Errors match [ErrKeyConfig] and never quote a key.
func ParseKeyRing(s string) (*KeyRing, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("%w: no key (set AGENTKIT_CREDENTIALS_KEY to <id>:<base64 of %d random bytes>)", ErrKeyConfig, KeySize)
	}
	keys := map[string][]byte{}
	var ids []string
	for i, part := range strings.Split(s, ",") {
		id, b64, ok := strings.Cut(strings.TrimSpace(part), ":")
		switch {
		case !ok:
			return nil, fmt.Errorf("%w: key %d is not <id>:<base64>", ErrKeyConfig, i+1)
		case !keyIDPattern.MatchString(id):
			return nil, fmt.Errorf("%w: key %d: the id must match %s", ErrKeyConfig, i+1, keyIDPattern)
		case keys[id] != nil:
			return nil, fmt.Errorf("%w: key id %q appears twice", ErrKeyConfig, id)
		}
		key, err := decodeKey(b64)
		if err != nil {
			return nil, fmt.Errorf("%w: key %q: not base64", ErrKeyConfig, id)
		}
		if len(key) != KeySize {
			return nil, fmt.Errorf("%w: key %q is %d bytes, want %d", ErrKeyConfig, id, len(key), KeySize)
		}
		keys[id] = key
		ids = append(ids, id)
	}
	return NewKeyRing(ids[0], keys)
}

func decodeKey(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, errors.New("not base64")
}

// NewKeyRing builds a key ring from keys by id, current sealing. The keys
// are copied.
func NewKeyRing(current string, keys map[string][]byte) (*KeyRing, error) {
	if keys[current] == nil {
		return nil, fmt.Errorf("%w: no current key %q", ErrKeyConfig, current)
	}
	r := &KeyRing{current: current, aeads: map[string]cipher.AEAD{}, ids: []string{current}}
	for id, key := range keys {
		if !keyIDPattern.MatchString(id) {
			return nil, fmt.Errorf("%w: key id must match %s", ErrKeyConfig, keyIDPattern)
		}
		if len(key) != KeySize {
			return nil, fmt.Errorf("%w: key %q is %d bytes, want %d", ErrKeyConfig, id, len(key), KeySize)
		}
		block, err := aes.NewCipher(append([]byte(nil), key...))
		if err != nil {
			return nil, fmt.Errorf("%w: key %q: %w", ErrKeyConfig, id, err)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("%w: key %q: %w", ErrKeyConfig, id, err)
		}
		r.aeads[id] = aead
		if id != current {
			r.ids = append(r.ids, id)
		}
	}
	slices.Sort(r.ids[1:])
	return r, nil
}

// Current is the id of the key that seals.
func (r *KeyRing) Current() string { return r.current }

// IDs are the ids of every key, the current one first.
func (r *KeyRing) IDs() []string { return append([]string(nil), r.ids...) }

// Sealer seals credentials with a key ring.
type Sealer struct{ ring *KeyRing }

// NewSealer returns a sealer for ring.
func NewSealer(ring *KeyRing) *Sealer { return &Sealer{ring: ring} }

// A blob is: format version (1 byte), key id length (1 byte), key id, nonce
// (12 bytes), then AES-256-GCM's ciphertext and tag. The associated data
// binds the format version, the key id and the key (tenant, user and slot).
const (
	formatV1  byte = 1
	nonceSize      = 12
	aadLabel       = "agentkit/credentials"
)

// Seal encrypts plaintext for key under the current key, with a fresh random
// nonce.
func (s *Sealer) Seal(key Key, plaintext []byte) ([]byte, error) {
	id := s.ring.current
	aead := s.ring.aeads[id]
	blob := make([]byte, 0, 2+len(id)+nonceSize+len(plaintext)+aead.Overhead())
	blob = append(blob, formatV1, byte(len(id)))
	blob = append(blob, id...)
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("credentials: nonce: %w", err)
	}
	blob = append(blob, nonce...)
	return aead.Seal(blob, nonce, plaintext, aad(formatV1, id, key)), nil
}

// Open decrypts a blob sealed for key, under any key of the ring. It reports
// the id of the key it was sealed under.
func (s *Sealer) Open(key Key, blob []byte) (plaintext []byte, keyID string, err error) {
	if len(blob) < 2 || blob[0] != formatV1 {
		return nil, "", ErrCorrupt
	}
	n := int(blob[1])
	if n == 0 || len(blob) < 2+n+nonceSize {
		return nil, "", ErrCorrupt
	}
	id := string(blob[2 : 2+n])
	aead, ok := s.ring.aeads[id]
	if !ok {
		if !keyIDPattern.MatchString(id) {
			return nil, "", ErrCorrupt
		}
		return nil, "", fmt.Errorf("%w: key id %q", ErrUnknownKey, id)
	}
	nonce := blob[2+n : 2+n+nonceSize]
	out, err := aead.Open(nil, nonce, blob[2+n+nonceSize:], aad(formatV1, id, key))
	if err != nil {
		return nil, "", ErrCorrupt
	}
	return out, id, nil
}

// Reseal opens a blob and seals it again under the current key (with a new
// nonce). A blob already under the current key is sealed again too.
func (s *Sealer) Reseal(key Key, blob []byte) ([]byte, error) {
	plain, _, err := s.Open(key, blob)
	if err != nil {
		return nil, err
	}
	defer clear(plain)
	return s.Seal(key, plain)
}

// aad is the associated data: every field length-prefixed, so no two keys
// share it.
func aad(version byte, keyID string, k Key) []byte {
	out := []byte(aadLabel)
	out = append(out, version)
	for _, f := range []string{keyID, k.Tenant.ID(), k.User, k.Slot} {
		out = binary.BigEndian.AppendUint32(out, uint32(len(f)))
		out = append(out, f...)
	}
	return out
}
