package login

import (
	"errors"
	"fmt"
	"path"
	"slices"

	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// The roles a [User] can have.
const (
	RoleUser  = "user"
	RoleAdmin = "admin"
)

// User is the configuration of one local user.
type User struct {
	// Username is what the user signs in with: 1 to 64 letters, digits,
	// '.', '_', '-', '@' or '+'. It is compared exactly, case included.
	Username string `json:"username"`
	// PasswordHash is the argon2id hash of the password, in PHC form
	// ([HashPassword]). The password itself is never configured.
	PasswordHash string `json:"password_hash"`
	// Tenant is the id of the tenant the user acts for.
	Tenant string `json:"tenant"`
	// Roles are [RoleUser], [RoleAdmin] or both; at least one.
	Roles []string `json:"roles"`
	// Toolsets are those the user may use, as names or path.Match patterns
	// (see auth.Principal). Absent means all ("*").
	Toolsets []string `json:"toolsets,omitempty"`
}

// ConfigError is a user entry that [New] refused. Its message names the
// user, never the hash.
type ConfigError struct {
	Username string
	Err      error
}

func (e *ConfigError) Error() string { return fmt.Sprintf("login: user %q: %v", e.Username, e.Err) }

func (e *ConfigError) Unwrap() error { return e.Err }

// ScopesOf returns the scopes that roles grant: "agent:run" and "agent:read"
// for [RoleUser]; those, "agent:admin" (toolsets added at run time) and
// "admin:*" (system prompts, credentials) for [RoleAdmin].
func ScopesOf(roles []string) []string {
	var s []string
	if slices.Contains(roles, RoleUser) || slices.Contains(roles, RoleAdmin) {
		s = append(s, "agent:run", "agent:read")
	}
	if slices.Contains(roles, RoleAdmin) {
		s = append(s, "agent:admin", "admin:*")
	}
	return s
}

// user is a checked [User].
type user struct {
	name     string
	hash     hash
	tenant   tenancy.Tenant
	roles    []string
	scopes   []string
	toolsets []string
}

func checkUser(u User) (*user, error) {
	if !validUsername(u.Username) {
		return nil, errors.New("a username is 1 to 64 letters, digits, '.', '_', '-', '@' or '+'")
	}
	h, err := parseHash(u.PasswordHash)
	if err != nil {
		return nil, err
	}
	t, err := tenancy.NewTenant(u.Tenant)
	if err != nil {
		return nil, fmt.Errorf("tenant: %w", err)
	}
	if len(u.Roles) == 0 {
		return nil, errors.New("no roles: give \"user\" or \"admin\"")
	}
	for i, r := range u.Roles {
		if r != RoleUser && r != RoleAdmin {
			return nil, fmt.Errorf("role %q: a role is \"user\" or \"admin\"", r)
		}
		if slices.Contains(u.Roles[:i], r) {
			return nil, fmt.Errorf("role %q listed twice", r)
		}
	}
	toolsets := slices.Clone(u.Toolsets)
	if len(toolsets) == 0 {
		toolsets = []string{"*"}
	}
	for _, p := range toolsets {
		if _, err := path.Match(p, ""); err != nil || p == "" {
			return nil, fmt.Errorf("toolset pattern %q is malformed", p)
		}
	}
	return &user{name: u.Username, hash: h, tenant: t, roles: slices.Clone(u.Roles), scopes: ScopesOf(u.Roles), toolsets: toolsets}, nil
}

func validUsername(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-' || c == '@' || c == '+'
		if !ok {
			return false
		}
	}
	return true
}
