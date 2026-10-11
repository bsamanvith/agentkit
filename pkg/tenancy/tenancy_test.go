package tenancy_test

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

func tenant(t testing.TB, id string) tenancy.Tenant {
	t.Helper()
	tn, err := tenancy.NewTenant(id)
	if err != nil {
		t.Fatalf("NewTenant(%q): %v", id, err)
	}
	return tn
}

func TestFromReturnsWhatWithStored(t *testing.T) {
	t.Parallel()
	acme, globex := tenant(t, "acme"), tenant(t, "globex")

	tests := []struct {
		name   string
		ctx    context.Context
		want   tenancy.Tenant
		wantOK bool
	}{
		{"stored", tenancy.With(t.Context(), acme), acme, true},
		{"nested With overrides", tenancy.With(tenancy.With(t.Context(), acme), globex), globex, true},
		{"no tenant", t.Context(), tenancy.Tenant{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := tenancy.From(tt.ctx)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("From = (%v, %v), want (%v, %v)", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestNewTenantRejectsInvalidIDs(t *testing.T) {
	t.Parallel()
	valid := []string{"a", "acme", "Acme-Corp_2.eu", "0", "9lives", strings.Repeat("x", tenancy.MaxIDLength)}
	for _, id := range valid {
		tn, err := tenancy.NewTenant(id)
		if err != nil || tn.ID() != id {
			t.Errorf("NewTenant(%q) = (%q, %v), want it accepted unchanged", id, tn.ID(), err)
		}
	}

	invalid := []string{
		"", strings.Repeat("x", tenancy.MaxIDLength+1),
		".", "..", ".hidden", "-x", "_x",
		"a/b", "../x", `a\b`, "a b", "a\x00b", "a\nb", " a", "a ",
		"a'b", `a"b`, "a;b", "a%00", "a:b", "a*",
		"é", "tenant-é", "ａ", // not ASCII
	}
	for _, id := range invalid {
		if tn, err := tenancy.NewTenant(id); !errors.Is(err, tenancy.ErrInvalidTenant) || tn != (tenancy.Tenant{}) {
			t.Errorf("NewTenant(%q) = (%v, %v), want ErrInvalidTenant and the zero Tenant", id, tn, err)
		}
	}
}

var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// Whatever NewTenant accepts is exactly what the documented pattern allows,
// and comes back unchanged; whatever it rejects reports ErrInvalidTenant.
func FuzzNewTenant(f *testing.F) {
	for _, s := range []string{"acme", "", "..", "a/b", "é", "a\x00", strings.Repeat("x", 64), strings.Repeat("x", 65)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, id string) {
		tn, err := tenancy.NewTenant(id)
		if want := validID.MatchString(id); (err == nil) != want {
			t.Fatalf("NewTenant(%q) error = %v, pattern says valid = %v", id, err, want)
		}
		if err != nil {
			if !errors.Is(err, tenancy.ErrInvalidTenant) {
				t.Fatalf("NewTenant(%q) error %v does not match ErrInvalidTenant", id, err)
			}
			return
		}
		if tn.ID() != id {
			t.Fatalf("NewTenant(%q).ID() = %q", id, tn.ID())
		}
	})
}

func TestScopeSurvivesDerivedContextsButNotSiblings(t *testing.T) {
	t.Parallel()
	acme, globex := tenant(t, "acme"), tenant(t, "globex")
	parent := t.Context()
	inAcme := tenancy.WithRequest(tenancy.WithUser(tenancy.With(parent, acme), "ann"), "req-1")

	cancelled, cancel := context.WithCancel(inAcme)
	defer cancel()
	timed, stop := context.WithTimeout(inAcme, time.Hour)
	defer stop()
	derived := map[string]context.Context{
		"WithCancel":    cancelled,
		"WithTimeout":   timed,
		"WithValue":     context.WithValue(inAcme, otherKey{}, 1),
		"WithoutCancel": context.WithoutCancel(inAcme),
		"after cancel":  afterCancel(inAcme),
	}
	for name, ctx := range derived {
		got, ok := tenancy.From(ctx)
		user, _ := tenancy.User(ctx)
		req, _ := tenancy.Request(ctx)
		if !ok || got != acme || user != "ann" || req != "req-1" {
			t.Errorf("%s: scope = (%v, %v, %q, %q), want acme, ann, req-1", name, got, ok, user, req)
		}
	}

	// Siblings: another tenant on the same parent, and the parent itself,
	// see nothing of acme.
	inGlobex := tenancy.With(parent, globex)
	if got, _ := tenancy.From(inGlobex); got != globex {
		t.Errorf("sibling tenant = %v, want globex", got)
	}
	if u, ok := tenancy.User(inGlobex); ok {
		t.Errorf("sibling sees user %q of another context", u)
	}
	if got, ok := tenancy.From(parent); ok {
		t.Errorf("parent context sees tenant %v set on a child", got)
	}
	// Adding a user in a child does not change the context it came from.
	_ = tenancy.WithUser(inAcme, "bob")
	if u, _ := tenancy.User(inAcme); u != "ann" {
		t.Errorf("user of the original context = %q after a child set another, want ann", u)
	}
}

// otherKey is the key of an unrelated context value.
type otherKey struct{}

func afterCancel(parent context.Context) context.Context {
	ctx, cancel := context.WithCancel(parent)
	cancel()
	return ctx
}

func TestRequireAndMustFromRefuseAContextWithNoTenant(t *testing.T) {
	t.Parallel()
	acme := tenant(t, "acme")

	if got, err := tenancy.Require(tenancy.With(t.Context(), acme)); err != nil || got != acme {
		t.Errorf("Require = (%v, %v), want acme", got, err)
	}
	if _, err := tenancy.Require(t.Context()); !errors.Is(err, tenancy.ErrNoTenant) {
		t.Errorf("Require with no tenant = %v, want ErrNoTenant", err)
	}

	if got := tenancy.MustFrom(tenancy.With(t.Context(), acme)); got != acme {
		t.Errorf("MustFrom = %v, want acme", got)
	}
	defer func() {
		r := recover()
		if err, ok := r.(error); !ok || !errors.Is(err, tenancy.ErrNoTenant) {
			t.Errorf("MustFrom with no tenant panicked with %v, want ErrNoTenant", r)
		}
	}()
	tenancy.MustFrom(t.Context())
}

// With starts a new scope. A user or request id set for one tenant must never
// carry into another, and With on the zero Tenant must not leave the outer
// tenant in place.
func TestWithStartsANewScope(t *testing.T) {
	t.Parallel()
	acme, globex := tenant(t, "acme"), tenant(t, "globex")
	inAcme := tenancy.WithRequest(tenancy.WithUser(tenancy.With(t.Context(), acme), "ann"), "req-1")

	switched := tenancy.With(inAcme, globex)
	if u, ok := tenancy.User(switched); ok {
		t.Errorf("user %q of the old tenant carried over", u)
	}
	if r, ok := tenancy.Request(switched); ok {
		t.Errorf("request id %q of the old tenant carried over", r)
	}

	cleared := tenancy.With(inAcme, tenancy.Tenant{})
	if got, ok := tenancy.From(cleared); ok {
		t.Errorf("With(zero Tenant) left tenant %v in place", got)
	}
	if _, err := tenancy.Require(cleared); !errors.Is(err, tenancy.ErrNoTenant) {
		t.Errorf("Require after clearing = %v, want ErrNoTenant", err)
	}
}
