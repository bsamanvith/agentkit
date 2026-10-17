package credentials

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// ErrInvalidKey reports a slot or user that is not allowed.
var ErrInvalidKey = errors.New("credentials: invalid key")

// Key says whose a credential is and what for.
type Key struct {
	Tenant tenancy.Tenant
	// User is the user the credential acts for; "" for a credential the
	// tenant shares.
	User string
	// Slot names what the credential is for ("salesforce", "gmail"), with
	// the toolset name rule.
	Slot string
}

var slotPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// MaxUserLength caps a user id.
const MaxUserLength = 256

// keyOf builds the key for the tenant of ctx, checking user and slot.
func keyOf(ctx context.Context, user, slot string) (Key, error) {
	t, err := tenancy.Require(ctx)
	if err != nil {
		return Key{}, err
	}
	if err := checkSlot(slot); err != nil {
		return Key{}, err
	}
	if err := checkUser(user); err != nil {
		return Key{}, err
	}
	return Key{Tenant: t, User: user, Slot: slot}, nil
}

func checkSlot(slot string) error {
	if !slotPattern.MatchString(slot) {
		return fmt.Errorf("%w: slot %q must match %s", ErrInvalidKey, slot, slotPattern)
	}
	return nil
}

// checkUser allows "" and up to MaxUserLength bytes of printable UTF-8.
func checkUser(user string) error {
	if len(user) > MaxUserLength || !utf8.ValidString(user) {
		return fmt.Errorf("%w: user must be valid UTF-8, at most %d bytes", ErrInvalidKey, MaxUserLength)
	}
	for _, r := range user {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: user has a control character", ErrInvalidKey)
		}
	}
	return nil
}
