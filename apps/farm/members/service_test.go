package members_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/members"
	"github.com/bsamanvith/agentkit/apps/farm/members/memberstest"
	"github.com/bsamanvith/agentkit/pkg/auth/accounts"
)

type rig struct {
	acc    *accounts.Service
	mem    *members.Service
	invite *accounts.InviteLane
}

func newRig(t *testing.T) rig {
	t.Helper()
	store := accounts.NewMemoryStore()
	invite := accounts.NewInviteLane(store, nil)
	acc, err := accounts.New(store, accounts.WithLanes(invite))
	if err != nil {
		t.Fatal(err)
	}
	return rig{acc: acc, mem: members.New(&memberstest.Memory{}, acc, nil), invite: invite}
}

func (r rig) account(t *testing.T, name string, roles ...string) accounts.Account {
	t.Helper()
	a, err := r.acc.CreateAccount(context.Background(), accounts.Account{Name: name, Roles: roles})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// farmOf returns the account's own farm, made on first use.
func (r rig) farmOf(t *testing.T, a accounts.Account) string {
	t.Helper()
	ms, err := r.mem.Mine(context.Background(), a)
	if err != nil || len(ms) != 1 || ms[0].Role != members.Owner {
		t.Fatalf("Mine = %+v, %v; want one farm as owner", ms, err)
	}
	return ms[0].Holding.ID
}

func TestAJoinCodeAddsWithItsRoleOnce(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	owner, son, other := r.account(t, "Lakshmi"), r.account(t, "Ravi"), r.account(t, "Someone")
	h := r.farmOf(t, owner)
	if _, err := r.mem.Invite(ctx, owner.ID, h, members.Partner); !errors.Is(err, members.ErrInvalid) {
		t.Fatalf("invite as partner: %v, want ErrInvalid (partners come by assisted sign-up)", err)
	}
	if _, err := r.mem.Invite(ctx, son.ID, h, members.Family); !errors.Is(err, members.ErrNotFound) {
		t.Fatalf("a non-member invites: %v, want ErrNotFound", err)
	}
	code, err := r.mem.Invite(ctx, owner.ID, h, members.Worker)
	if err != nil {
		t.Fatal(err)
	}
	m, err := r.mem.Join(ctx, son.ID, code)
	if err != nil || m.Role != members.Worker || m.Holding.ID != h {
		t.Fatalf("Join = %+v, %v; want worker of %s", m, err, h)
	}
	if _, err := r.mem.Join(ctx, other.ID, code); !errors.Is(err, accounts.ErrBadSecret) {
		t.Fatalf("second use of a join code: %v, want ErrBadSecret", err)
	}
	// A worker reads but does not manage.
	if _, err := r.mem.Members(ctx, son.ID, h); err != nil {
		t.Fatalf("a worker lists members: %v", err)
	}
	if _, err := r.mem.Invite(ctx, son.ID, h, members.Worker); !errors.Is(err, members.ErrForbidden) {
		t.Fatalf("a worker invites: %v, want ErrForbidden", err)
	}
	if _, err := r.mem.Rename(ctx, son.ID, h, "Mine now"); !errors.Is(err, members.ErrForbidden) {
		t.Fatalf("a worker renames the farm: %v, want ErrForbidden", err)
	}
}

func TestAFarmKeepsAnOwner(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	owner, wife := r.account(t, "Lakshmi"), r.account(t, "Padma")
	h := r.farmOf(t, owner)
	if _, err := r.mem.SetRole(ctx, owner.ID, h, owner.ID, members.Family); !errors.Is(err, members.ErrLastOwner) {
		t.Fatalf("the last owner steps down: %v, want ErrLastOwner", err)
	}
	if err := r.mem.Remove(ctx, owner.ID, h, owner.ID); !errors.Is(err, members.ErrLastOwner) {
		t.Fatalf("the last owner leaves: %v, want ErrLastOwner", err)
	}
	code, err := r.mem.Invite(ctx, owner.ID, h, members.Owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.mem.Join(ctx, wife.ID, code); err != nil {
		t.Fatal(err)
	}
	// With a second owner, the first may leave.
	if err := r.mem.Remove(ctx, owner.ID, h, owner.ID); err != nil {
		t.Fatalf("an owner leaves with another owner there: %v", err)
	}
	if _, err := r.mem.Role(ctx, owner.ID, h); !errors.Is(err, members.ErrNotFound) {
		t.Fatalf("the one who left still has a role: %v", err)
	}
}

func TestOnlyAPartnerSignsAFarmerUpAndRecoversThem(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	partner, plain := r.account(t, "FPO field worker", members.AccountRolePartner), r.account(t, "Neighbour")
	if _, err := r.mem.Assist(ctx, plain, "Ramaiah", "98765 43210", ""); !errors.Is(err, members.ErrForbidden) {
		t.Fatalf("assist without the partner role: %v, want ErrForbidden", err)
	}
	got, err := r.mem.Assist(ctx, partner, "Ramaiah", "98765 43210", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Account.Phone != "+919876543210" || got.Holding.Name != "Ramaiah's farm" {
		t.Fatalf("assisted = %+v; want the phone in E.164 and a farm named after them", got)
	}
	if role, err := r.mem.Role(ctx, partner.ID, got.Holding.ID); err != nil || role != members.Partner {
		t.Fatalf("partner's role = %q, %v; want partner", role, err)
	}
	if role, err := r.mem.Role(ctx, got.Account.ID, got.Holding.ID); err != nil || role != members.Owner {
		t.Fatalf("farmer's role = %q, %v; want owner", role, err)
	}
	// The code signs the farmer in, once.
	if a, err := r.invite.Check(ctx, "", got.Code); err != nil || a.Account != got.Account.ID {
		t.Fatalf("the farmer's code signs in %v, %v; want %s", a.Account, err, got.Account.ID)
	}
	if _, err := r.invite.Check(ctx, "", got.Code); !errors.Is(err, accounts.ErrBadSecret) {
		t.Fatalf("the code twice: %v, want ErrBadSecret", err)
	}
	// Recovery: the farm's partner makes a new code; the owner (no partner
	// role) cannot, nor a partner of another farm.
	if _, err := r.mem.SignInCode(ctx, got.Account, got.Holding.ID, got.Account.ID); !errors.Is(err, members.ErrForbidden) {
		t.Fatalf("the farmer makes their own code: %v, want ErrForbidden", err)
	}
	stranger := r.account(t, "Other FPO", members.AccountRolePartner)
	if _, err := r.mem.SignInCode(ctx, stranger, got.Holding.ID, got.Account.ID); !errors.Is(err, members.ErrNotFound) {
		t.Fatalf("another farm's partner makes a code: %v, want ErrNotFound", err)
	}
	code, err := r.mem.SignInCode(ctx, partner, got.Holding.ID, got.Account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a, err := r.invite.Check(ctx, "", code); err != nil || a.Account != got.Account.ID {
		t.Fatalf("the recovery code signs in %v, %v", a.Account, err)
	}
}
