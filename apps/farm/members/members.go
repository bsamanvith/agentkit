// Package members is who works a farm: a holding (the farm in the
// farmer's sense, its plots and the people on it) and its members, each
// with a role. Accounts and sign-in are pkg/auth/accounts'; this package
// says what each account may do with which farm.
//
// Roles, from most to least:
//
//   - owner: everything, including who else is a member;
//   - partner: the field worker of a partner organisation who signed the
//     farmer up (assisted sign-up), or helps them; reads and writes, cannot
//     change members, can make the farmer a new sign-in code;
//   - family: reads and writes the plots and plans;
//   - worker: reads (a field worker; recording work comes with the task
//     layer).
//
// An account with no farm gets one of its own, as owner, the first time
// it asks ([Service.Mine]). The first farm made in an app that was used
// before sign-in takes the plots made then (farm.Service.Adopt).
package members

import (
	"context"
	"errors"
	"time"
)

// Role is a member's role in a holding.
type Role string

// The roles.
const (
	Owner   Role = "owner"
	Partner Role = "partner"
	Family  Role = "family"
	Worker  Role = "worker"
)

// Valid reports whether r is one of the roles.
func (r Role) Valid() bool {
	switch r {
	case Owner, Partner, Family, Worker:
		return true
	}
	return false
}

// Action is what a role may be allowed.
type Action int

// The actions.
const (
	// Read the plots, plans, notices and members.
	Read Action = iota
	// Write the plots, groups, plans and designs.
	Write
	// Manage the members and the farm's name.
	Manage
)

// Can reports whether role may do a.
func Can(role Role, a Action) bool {
	switch a {
	case Read:
		return role.Valid()
	case Write:
		return role == Owner || role == Partner || role == Family
	case Manage:
		return role == Owner
	}
	return false
}

// AccountRolePartner is the app-wide account role (accounts.Account.Roles)
// of a partner organisation's field worker: they may sign farmers up
// ([Service.Assist]). An admin grants it, or the deployment by email.
const AccountRolePartner = "partner"

// Errors callers branch on.
var (
	// ErrNotFound: no such holding, or the account is not its member (the
	// two look alike from outside).
	ErrNotFound = errors.New("members: not found")
	// ErrExists: a holding id that is taken.
	ErrExists = errors.New("members: already exists")
	// ErrForbidden: the role does not allow it.
	ErrForbidden = errors.New("members: not allowed for your role")
	// ErrLastOwner: a farm keeps at least one owner.
	ErrLastOwner = errors.New("members: a farm keeps at least one owner")
	// ErrInvalid: a malformed request.
	ErrInvalid = errors.New("members: invalid")
)

// Holding is a farm in the farmer's sense: plots and the people on them.
type Holding struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// Member is an account's place in a holding.
type Member struct {
	Holding string    `json:"holding"`
	Account string    `json:"account"`
	Role    Role      `json:"role"`
	AddedBy string    `json:"added_by,omitempty"`
	AddedAt time.Time `json:"added_at"`
}

// Store keeps holdings and members. Implementations are safe for
// concurrent use and pass memberstest.RunStore.
type Store interface {
	// CreateHolding saves a new holding; ErrExists if its ID is taken.
	CreateHolding(ctx context.Context, h Holding) error
	// Holding returns a holding; ErrNotFound if there is none.
	Holding(ctx context.Context, id string) (Holding, error)
	// UpdateHolding replaces a holding; ErrNotFound if there is none.
	UpdateHolding(ctx context.Context, h Holding) error
	// CountHoldings returns how many holdings there are.
	CountHoldings(ctx context.Context) (int, error)

	// PutMember adds a member, or replaces the one of the same holding and
	// account.
	PutMember(ctx context.Context, m Member) error
	// Member returns the account's membership of a holding; ErrNotFound if
	// it has none.
	Member(ctx context.Context, holding, account string) (Member, error)
	// Members returns a holding's members, first added first.
	Members(ctx context.Context, holding string) ([]Member, error)
	// Memberships returns an account's memberships, first added first.
	Memberships(ctx context.Context, account string) ([]Member, error)
	// RemoveMember removes a membership; ErrNotFound if there is none.
	RemoveMember(ctx context.Context, holding, account string) error
}
