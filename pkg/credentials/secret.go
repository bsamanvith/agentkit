package credentials

import (
	"errors"
	"fmt"
	"log/slog"
)

// ErrEncodeSecret is returned by encoding a [Secret]: a value must be
// revealed on purpose, never serialized by accident.
var ErrEncodeSecret = errors.New("credentials: a Secret cannot be encoded; call Reveal")

const redacted = "[redacted]"

// Secret is a string whose value is reachable only through [Secret.Reveal].
// Printing it with fmt (any verb), logging it with slog and encoding it as
// JSON or text never show the value. The zero Secret is empty.
type Secret struct{ v string }

// NewSecret wraps v.
func NewSecret(v string) Secret { return Secret{v} }

// Reveal returns the value. Call it where the value is used (a request
// header), never to log it.
func (s Secret) Reveal() string { return s.v }

// IsZero reports whether s is empty.
func (s Secret) IsZero() bool { return s.v == "" }

// String redacts.
func (s Secret) String() string { return redacted }

// GoString redacts, for %#v.
func (s Secret) GoString() string { return redacted }

// Format redacts, whatever the verb and flags.
func (s Secret) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(redacted)) }

// LogValue redacts.
func (s Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalJSON fails with [ErrEncodeSecret].
func (s Secret) MarshalJSON() ([]byte, error) { return nil, ErrEncodeSecret }

// MarshalText fails with [ErrEncodeSecret].
func (s Secret) MarshalText() ([]byte, error) { return nil, ErrEncodeSecret }
