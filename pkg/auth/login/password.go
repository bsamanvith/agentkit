package login

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Params are argon2id's cost parameters (RFC 9106).
type Params struct {
	Memory  uint32 // KiB
	Time    uint32 // passes
	Threads uint8
}

// DefaultParams are those [HashPassword] uses: OWASP's argon2id minimum
// (19 MiB, 2 passes, 1 thread), which costs about 20 ms a check.
var DefaultParams = Params{Memory: 19 * 1024, Time: 2, Threads: 1}

// Password length limits, in bytes.
const (
	MinPasswordLength = 8
	MaxPasswordLength = 1024
)

const (
	saltLength = 16
	keyLength  = 32
)

// Hasher derives a key from a password with argon2id: it has the signature
// of argon2.IDKey, the default. Tests pass one that counts its work.
type Hasher func(password, salt []byte, time, memory uint32, threads uint8, keyLen uint32) []byte

// ErrPasswordLength is a password [HashPassword] refuses: shorter than
// [MinPasswordLength] or longer than [MaxPasswordLength].
var ErrPasswordLength = fmt.Errorf("a password must be %d to %d bytes", MinPasswordLength, MaxPasswordLength)

// HashPassword returns the argon2id hash of password in PHC form, with
// [DefaultParams] and a random salt: what a [User]'s PasswordHash takes.
func HashPassword(password string) (string, error) {
	if len(password) < MinPasswordLength || len(password) > MaxPasswordLength {
		return "", ErrPasswordLength
	}
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	p := DefaultParams
	h := hash{params: p, salt: salt, key: argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, keyLength)}
	return h.String(), nil
}

// hash is a parsed PHC argon2id hash.
type hash struct {
	params    Params
	salt, key []byte
}

var b64 = base64.RawStdEncoding

// String is the PHC form: $argon2id$v=19$m=<KiB>,t=<passes>,p=<threads>$<salt>$<key>.
func (h hash) String() string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, h.params.Memory, h.params.Time, h.params.Threads, b64.EncodeToString(h.salt), b64.EncodeToString(h.key))
}

// cost is what a check with h costs, to compare parameters.
func (h hash) cost() uint64 { return uint64(h.params.Memory) * uint64(h.params.Time) }

// matches reports whether password is the one h was made from, comparing in
// constant time.
func (h hash) matches(hasher Hasher, password string) bool {
	k := hasher([]byte(password), h.salt, h.params.Time, h.params.Memory, h.params.Threads, uint32(len(h.key)))
	return subtle.ConstantTimeCompare(k, h.key) == 1
}

// parseHash reads a PHC argon2id hash. Its errors never contain the hash.
func parseHash(s string) (hash, error) {
	parts := strings.Split(s, "$")
	if len(parts) != 6 || parts[0] != "" {
		return hash{}, errors.New("password_hash is not a PHC string ($argon2id$v=19$m=...,t=...,p=...$salt$hash)")
	}
	if parts[1] != "argon2id" {
		return hash{}, errors.New("password_hash is not argon2id")
	}
	if parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return hash{}, fmt.Errorf("password_hash: version is not %d", argon2.Version)
	}
	var h hash
	seen := map[string]bool{}
	for kv := range strings.SplitSeq(parts[3], ",") {
		k, v, ok := strings.Cut(kv, "=")
		n, err := strconv.ParseUint(v, 10, 32)
		if !ok || err != nil || seen[k] || v != strconv.FormatUint(n, 10) {
			return hash{}, errors.New("password_hash: the parameters are not m=<n>,t=<n>,p=<n>")
		}
		seen[k] = true
		switch k {
		case "m":
			h.params.Memory = uint32(n)
		case "t":
			h.params.Time = uint32(n)
		case "p":
			if n > 255 {
				return hash{}, errors.New("password_hash: p is over 255")
			}
			h.params.Threads = uint8(n)
		default:
			return hash{}, errors.New("password_hash: unknown parameter")
		}
	}
	p := h.params
	switch {
	case len(seen) != 3:
		return hash{}, errors.New("password_hash: m, t and p are all required")
	case p.Threads < 1 || p.Threads > 16:
		return hash{}, errors.New("password_hash: p must be 1 to 16")
	case p.Time < 1 || p.Time > 16:
		return hash{}, errors.New("password_hash: t must be 1 to 16")
	case p.Memory < 8*uint32(p.Threads) || p.Memory > 1<<20:
		return hash{}, errors.New("password_hash: m must be 8*p KiB to 1 GiB")
	}
	var err error
	if h.salt, err = b64.Strict().DecodeString(parts[4]); err != nil || len(h.salt) < 8 || len(h.salt) > 64 {
		return hash{}, errors.New("password_hash: the salt is not 8 to 64 bytes of unpadded base64")
	}
	if h.key, err = b64.Strict().DecodeString(parts[5]); err != nil || len(h.key) < 16 || len(h.key) > 64 {
		return hash{}, errors.New("password_hash: the hash is not 16 to 64 bytes of unpadded base64")
	}
	return h, nil
}
