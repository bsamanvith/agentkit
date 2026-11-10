package members

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/pkg/auth/accounts"
	"github.com/bsamanvith/agentkit/pkg/clock"
)

// Code lifetimes.
const (
	// JoinCodeTTL is how long a code that adds someone to a farm works.
	JoinCodeTTL = 7 * 24 * time.Hour
	// SignInCodeTTL is how long a sign-in code from a partner works.
	SignInCodeTTL = 7 * 24 * time.Hour
)

// purposeJoin is the accounts code purpose of a join code.
const purposeJoin = "farm-join"

// MaxNameLen bounds a holding's name, in bytes.
const MaxNameLen = 100

// Service decides and records who may do what with which farm. It is safe
// for concurrent use.
type Service struct {
	store    Store
	accounts *accounts.Service
	farms    *farm.Service
	clock    clock.Clock
	log      *slog.Logger
	country  string
}

// Option configures [New].
type Option func(*Service)

// WithClock sets the clock that stamps members.
func WithClock(c clock.Clock) Option { return func(s *Service) { s.clock = c } }

// WithCountryCode sets the calling code a phone number without one takes
// at an assisted sign-up ("91", the default).
func WithCountryCode(cc string) Option { return func(s *Service) { s.country = cc } }

// WithLogger sets the logger (default: discard).
func WithLogger(l *slog.Logger) Option { return func(s *Service) { s.log = l } }

// New returns a Service over store, the app's accounts and its plots.
func New(store Store, acc *accounts.Service, farms *farm.Service, opts ...Option) *Service {
	s := &Service{store: store, accounts: acc, farms: farms, clock: clock.Real(), log: slog.New(slog.DiscardHandler), country: "91"}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Membership is a holding and the account's role in it.
type Membership struct {
	Holding Holding `json:"holding"`
	Role    Role    `json:"role"`
}

// Mine returns the account's farms, first joined first. An account with
// none gets one of its own first, as owner, named after it.
func (s *Service) Mine(ctx context.Context, acc accounts.Account) ([]Membership, error) {
	ms, err := s.memberships(ctx, acc.ID)
	if err != nil || len(ms) > 0 {
		return ms, err
	}
	name := "My farm"
	if acc.Name != "" {
		name = truncate(acc.Name+"'s farm", MaxNameLen)
	}
	h, err := s.create(ctx, name, acc.ID, "")
	if err != nil {
		return nil, err
	}
	return []Membership{{Holding: h, Role: Owner}}, nil
}

func (s *Service) memberships(ctx context.Context, account string) ([]Membership, error) {
	ms, err := s.store.Memberships(ctx, account)
	if err != nil {
		return nil, err
	}
	out := []Membership{}
	for _, m := range ms {
		h, err := s.store.Holding(ctx, m.Holding)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, Membership{Holding: h, Role: m.Role})
	}
	return out, nil
}

// create makes a holding with owner as its owner; the app's first holding
// adopts the plots made before sign-in.
func (s *Service) create(ctx context.Context, name, owner, by string) (Holding, error) {
	n, err := s.store.CountHoldings(ctx)
	if err != nil {
		return Holding{}, err
	}
	now := s.clock.Now().UTC()
	h := Holding{ID: newID("hold_"), Name: name, CreatedAt: now}
	if err := s.store.CreateHolding(ctx, h); err != nil {
		return Holding{}, err
	}
	if err := s.store.PutMember(ctx, Member{Holding: h.ID, Account: owner, Role: Owner, AddedBy: by, AddedAt: now}); err != nil {
		return Holding{}, err
	}
	if n == 0 && s.farms != nil {
		adopted, err := s.farms.Adopt(ctx, h.ID)
		if err != nil {
			return Holding{}, fmt.Errorf("members: giving the plots made before sign-in to the first farm: %w", err)
		}
		if adopted > 0 {
			s.log.LogAttrs(ctx, slog.LevelInfo, "members: the first farm took the plots made before sign-in", slog.String("holding", h.ID), slog.Int("plots", adopted))
		}
	}
	return h, nil
}

// Role returns the account's role in a holding; ErrNotFound if it is not
// a member.
func (s *Service) Role(ctx context.Context, account, holding string) (Role, error) {
	m, err := s.store.Member(ctx, holding, account)
	if err != nil {
		return "", err
	}
	return m.Role, nil
}

// need returns the actor's role in holding if it allows a.
func (s *Service) need(ctx context.Context, actor, holding string, a Action) (Role, error) {
	r, err := s.Role(ctx, actor, holding)
	if err != nil {
		return "", err
	}
	if !Can(r, a) {
		return r, ErrForbidden
	}
	return r, nil
}

// MemberView is a member with the name their account goes by.
type MemberView struct {
	Member
	Name string `json:"name"`
}

// Members returns a holding's members, for a member of it.
func (s *Service) Members(ctx context.Context, actor, holding string) ([]MemberView, error) {
	if _, err := s.need(ctx, actor, holding, Read); err != nil {
		return nil, err
	}
	ms, err := s.store.Members(ctx, holding)
	if err != nil {
		return nil, err
	}
	out := make([]MemberView, 0, len(ms))
	for _, m := range ms {
		v := MemberView{Member: m}
		if a, err := s.accounts.Store().Account(ctx, m.Account); err == nil {
			v.Name = a.Name
		}
		out = append(out, v)
	}
	return out, nil
}

// Rename sets a holding's name; an owner's.
func (s *Service) Rename(ctx context.Context, actor, holding, name string) (Holding, error) {
	if _, err := s.need(ctx, actor, holding, Manage); err != nil {
		return Holding{}, err
	}
	name, err := checkName(name)
	if err != nil {
		return Holding{}, err
	}
	h, err := s.store.Holding(ctx, holding)
	if err != nil {
		return Holding{}, err
	}
	h.Name = name
	return h, s.store.UpdateHolding(ctx, h)
}

// Invite makes a code that adds whoever signs in and types it to holding
// with role (not partner: partners come by [Service.Assist]). An owner's.
func (s *Service) Invite(ctx context.Context, actor, holding string, role Role) (string, error) {
	if _, err := s.need(ctx, actor, holding, Manage); err != nil {
		return "", err
	}
	if !role.Valid() || role == Partner {
		return "", fmt.Errorf("%w: invite as owner, family or worker", ErrInvalid)
	}
	return s.accounts.IssueCode(ctx, purposeJoin, map[string]string{"holding": holding, "role": string(role), "by": actor}, JoinCodeTTL)
}

// Join adds the account to the holding a join code names, with its role;
// a member already keeps the role they have. A wrong, used or expired code
// is accounts.ErrBadSecret.
func (s *Service) Join(ctx context.Context, account, code string) (Membership, error) {
	data, err := s.accounts.RedeemCode(ctx, purposeJoin, code)
	if err != nil {
		return Membership{}, err
	}
	h, err := s.store.Holding(ctx, data["holding"])
	if err != nil {
		return Membership{}, err
	}
	if m, err := s.store.Member(ctx, h.ID, account); err == nil {
		return Membership{Holding: h, Role: m.Role}, nil
	}
	role := Role(data["role"])
	if err := s.store.PutMember(ctx, Member{Holding: h.ID, Account: account, Role: role, AddedBy: data["by"], AddedAt: s.clock.Now().UTC()}); err != nil {
		return Membership{}, err
	}
	s.log.LogAttrs(ctx, slog.LevelInfo, "members: joined", slog.String("holding", h.ID), slog.String("account", account), slog.String("role", string(role)))
	return Membership{Holding: h, Role: role}, nil
}

// SetRole changes a member's role; an owner's. The last owner stays one.
func (s *Service) SetRole(ctx context.Context, actor, holding, account string, role Role) (Member, error) {
	if _, err := s.need(ctx, actor, holding, Manage); err != nil {
		return Member{}, err
	}
	if !role.Valid() {
		return Member{}, fmt.Errorf("%w: role %q", ErrInvalid, role)
	}
	m, err := s.store.Member(ctx, holding, account)
	if err != nil {
		return Member{}, err
	}
	if m.Role == Owner && role != Owner {
		if err := s.keepAnOwner(ctx, holding); err != nil {
			return Member{}, err
		}
	}
	m.Role = role
	return m, s.store.PutMember(ctx, m)
}

// Remove takes a member out of a holding: an owner removes anyone, a
// member removes themself. The last owner stays. The removed account's
// sessions are not ended: it still has its own farm.
func (s *Service) Remove(ctx context.Context, actor, holding, account string) error {
	if actor != account {
		if _, err := s.need(ctx, actor, holding, Manage); err != nil {
			return err
		}
	}
	m, err := s.store.Member(ctx, holding, account)
	if err != nil {
		return err
	}
	if m.Role == Owner {
		if err := s.keepAnOwner(ctx, holding); err != nil {
			return err
		}
	}
	return s.store.RemoveMember(ctx, holding, account)
}

func (s *Service) keepAnOwner(ctx context.Context, holding string) error {
	ms, err := s.store.Members(ctx, holding)
	if err != nil {
		return err
	}
	owners := 0
	for _, m := range ms {
		if m.Role == Owner {
			owners++
		}
	}
	if owners <= 1 {
		return ErrLastOwner
	}
	return nil
}

// Assisted is a farmer a partner signed up.
type Assisted struct {
	Account accounts.Account `json:"account"`
	Holding Holding          `json:"holding"`
	// Code is the farmer's sign-in code: the partner reads it to them or
	// writes it down; it works once, for [SignInCodeTTL].
	Code    string    `json:"code"`
	Expires time.Time `json:"expires"`
}

// Assist signs a farmer up on their behalf: a new account with their name
// and phone, a farm of theirs (named farmName, or after them) with them as
// owner and the partner as partner, and a sign-in code. The partner's
// account must have the partner (or admin) role.
func (s *Service) Assist(ctx context.Context, partner accounts.Account, name, phone, farmName string) (Assisted, error) {
	if !isPartner(partner) {
		return Assisted{}, ErrForbidden
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return Assisted{}, fmt.Errorf("%w: the farmer's name is needed", ErrInvalid)
	}
	if farmName = strings.TrimSpace(farmName); farmName == "" {
		farmName = truncate(name+"'s farm", MaxNameLen)
	}
	farmName, err := checkName(farmName)
	if err != nil {
		return Assisted{}, err
	}
	if phone = strings.TrimSpace(phone); phone != "" {
		if phone, err = accounts.NormalizePhone(phone, s.country); err != nil {
			return Assisted{}, err
		}
	}
	acc, err := s.accounts.CreateAccount(ctx, accounts.Account{Name: name, Phone: phone})
	if err != nil {
		return Assisted{}, err
	}
	h, err := s.create(ctx, farmName, acc.ID, partner.ID)
	if err != nil {
		return Assisted{}, err
	}
	now := s.clock.Now().UTC()
	if err := s.store.PutMember(ctx, Member{Holding: h.ID, Account: partner.ID, Role: Partner, AddedBy: partner.ID, AddedAt: now}); err != nil {
		return Assisted{}, err
	}
	code, err := s.accounts.IssueSignInCode(ctx, acc.ID, SignInCodeTTL)
	if err != nil {
		return Assisted{}, err
	}
	s.log.LogAttrs(ctx, slog.LevelInfo, "members: assisted sign-up", slog.String("partner", partner.ID), slog.String("account", acc.ID), slog.String("holding", h.ID))
	return Assisted{Account: acc, Holding: h, Code: code, Expires: s.clock.Now().Add(SignInCodeTTL).UTC()}, nil
}

// SignInCode makes a new sign-in code for a member of holding who cannot
// get in (a lost phone, a forgotten password): the holding's partner's to
// make. The account's other sessions end, so a lost phone is signed out.
func (s *Service) SignInCode(ctx context.Context, partner accounts.Account, holding, account string) (string, error) {
	r, err := s.Role(ctx, partner.ID, holding)
	if err != nil {
		return "", err
	}
	if r != Partner || !isPartner(partner) {
		return "", ErrForbidden
	}
	if _, err := s.store.Member(ctx, holding, account); err != nil {
		return "", err
	}
	if account == partner.ID {
		return "", fmt.Errorf("%w: a partner cannot make a code for themself", ErrInvalid)
	}
	code, err := s.accounts.IssueSignInCode(ctx, account, SignInCodeTTL)
	if err != nil {
		return "", err
	}
	if err := s.accounts.SignOutAll(ctx, account); err != nil {
		return "", err
	}
	s.log.LogAttrs(ctx, slog.LevelInfo, "members: sign-in code for recovery", slog.String("partner", partner.ID), slog.String("account", account), slog.String("holding", holding))
	return code, nil
}

func isPartner(a accounts.Account) bool {
	return slices.Contains(a.Roles, AccountRolePartner) || slices.Contains(a.Roles, accounts.RoleAdmin)
}

func checkName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > MaxNameLen || !utf8.ValidString(name) {
		return "", fmt.Errorf("%w: a farm's name is 1 to %d bytes", ErrInvalid, MaxNameLen)
	}
	return name, nil
}

func truncate(s string, n int) string {
	for len(s) > n {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return s
}

func newID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b) // never fails (crypto/rand)
	return prefix + hex.EncodeToString(b)
}
