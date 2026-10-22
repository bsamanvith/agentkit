package memory

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// Store is a context store: files under [Root], scoped by the tenant (and,
// with [ScopeUser], the user) in ctx. Paths go through [CleanPath] first.
// Implementations must be safe for concurrent use, and a command that fails
// must change nothing.
type Store interface {
	// View returns a file's content, or a directory's listing up to two
	// levels deep. A missing path is ErrNotFound.
	View(ctx context.Context, path string) (View, error)
	// Create writes a file, replacing one that is there. A directory at
	// path is ErrIsDir, a file where a parent directory should be ErrNotDir.
	Create(ctx context.Context, path, text string) error
	// StrReplace replaces old with new in a file. old must occur exactly
	// once: none is ErrNoMatch, more is a *MatchError (ErrMultipleMatches).
	// An empty old is ErrInvalid.
	StrReplace(ctx context.Context, path, old, new string) error
	// Insert inserts text after line (0 for the start of the file). A line
	// past the end is a *LineError (ErrInvalidLine).
	Insert(ctx context.Context, path string, line int, text string) error
	// Delete removes a file, or a directory and everything under it.
	// Root is ErrInvalidPath.
	Delete(ctx context.Context, path string) error
	// Rename moves a file or a directory. A destination that exists is
	// ErrExists; moving Root, or a directory into itself, is
	// ErrInvalidPath.
	Rename(ctx context.Context, from, to string) error
}

// View is what [Store.View] returns: a file's text, or a directory's entries.
type View struct {
	// Path is the cleaned path viewed.
	Path string
	// Dir reports a directory; then Entries lists it and Text is empty.
	Dir bool
	// Size is the file's bytes, or the bytes of every file under the
	// directory.
	Size int64
	// Text is the file's content.
	Text string
	// Entries are the files and directories under the directory, one and
	// two levels down, sorted by path.
	Entries []Entry
}

// Entry is one file or directory of a listing.
type Entry struct {
	Path string
	Dir  bool
	// Size is the file's bytes, or the bytes of every file under the
	// directory.
	Size int64
}

// Scope is whose files a store's calls reach.
type Scope string

// The scopes.
const (
	// ScopeTenant shares the files among everyone of the tenant in ctx.
	ScopeTenant Scope = "tenant"
	// ScopeUser keeps each user's files apart: the tenant and the user in
	// ctx key every call.
	ScopeUser Scope = "user"
)

// Limits cap what one scope stores. A zero field takes its default.
type Limits struct {
	// MaxFileBytes caps one file (default [DefaultMaxFileBytes]).
	MaxFileBytes int64
	// MaxScopeBytes caps all files of a scope together (default
	// [DefaultMaxScopeBytes]).
	MaxScopeBytes int64
	// MaxFiles caps how many files a scope holds (default
	// [DefaultMaxFiles]).
	MaxFiles int
}

// The default limits. A file the size of the default is about 4,000 tokens
// to view, under the 16,000 characters Claude's memory tool expects a view
// to show before it pages.
const (
	DefaultMaxFileBytes  = 16 << 10
	DefaultMaxScopeBytes = 1 << 20
	DefaultMaxFiles      = 200
)

// Config configures a store backend.
type Config struct {
	// Scope is ScopeTenant (the default, when empty) or ScopeUser.
	Scope Scope
	// Limits are the caps of each scope.
	Limits Limits
}

// Resolve returns c with its defaults filled in, or ErrInvalid for a scope it
// does not know or a negative limit.
func (c Config) Resolve() (Config, error) {
	switch c.Scope {
	case "":
		c.Scope = ScopeTenant
	case ScopeTenant, ScopeUser:
	default:
		return c, fmt.Errorf("%w: scope %q: want %q or %q", ErrInvalid, c.Scope, ScopeTenant, ScopeUser)
	}
	l := &c.Limits
	if l.MaxFileBytes < 0 || l.MaxScopeBytes < 0 || l.MaxFiles < 0 {
		return c, fmt.Errorf("%w: negative limit", ErrInvalid)
	}
	if l.MaxFileBytes == 0 {
		l.MaxFileBytes = DefaultMaxFileBytes
	}
	if l.MaxScopeBytes == 0 {
		l.MaxScopeBytes = DefaultMaxScopeBytes
	}
	if l.MaxFiles == 0 {
		l.MaxFiles = DefaultMaxFiles
	}
	return c, nil
}

// Owner is whose files a call reaches: a tenant, and a user with ScopeUser
// (empty with ScopeTenant).
type Owner struct {
	Tenant string
	User   string
}

// MaxUserBytes bounds the user id that keys a ScopeUser call.
const MaxUserBytes = 256

// OwnerOf returns the owner of a call in ctx under scope: the tenant
// ([ErrNoTenant] without one) and, with ScopeUser, the user ([ErrNoUser]
// without one, or with one longer than MaxUserBytes).
func OwnerOf(ctx context.Context, scope Scope) (Owner, error) {
	t, err := tenancy.Require(ctx)
	if err != nil {
		return Owner{}, err
	}
	o := Owner{Tenant: t.ID()}
	if scope == ScopeUser {
		u, ok := tenancy.User(ctx)
		if !ok || u == "" || len(u) > MaxUserBytes {
			return Owner{}, ErrNoUser
		}
		o.User = u
	}
	return o, nil
}

// The errors of a store. Check them with errors.Is.
var (
	// ErrNoTenant is tenancy.ErrNoTenant: ctx carries no tenant.
	ErrNoTenant = tenancy.ErrNoTenant
	// ErrNoUser means a ScopeUser store got a ctx with no user.
	ErrNoUser = errors.New("memory: no user in context")
	// ErrInvalidPath is a path outside Root, with a ".." or hidden segment,
	// or too long; or Root where a command cannot take it.
	ErrInvalidPath = errors.New("memory: invalid path")
	// ErrNotFound is a path with no file or directory.
	ErrNotFound = errors.New("memory: path does not exist")
	// ErrExists is a rename onto a path that exists.
	ErrExists = errors.New("memory: path already exists")
	// ErrIsDir is a file command on a directory.
	ErrIsDir = errors.New("memory: path is a directory")
	// ErrNotDir is a path under a file.
	ErrNotDir = errors.New("memory: a parent of the path is a file")
	// ErrNoMatch is a str_replace whose old text does not occur.
	ErrNoMatch = errors.New("memory: old text not found")
	// ErrMultipleMatches is a str_replace whose old text occurs more than
	// once; the error is a *MatchError.
	ErrMultipleMatches = errors.New("memory: old text occurs more than once")
	// ErrInvalidLine is an insert line outside the file; the error is a
	// *LineError.
	ErrInvalidLine = errors.New("memory: invalid line")
	// ErrLimit is a write past a limit; the error is a *LimitError.
	ErrLimit = errors.New("memory: limit exceeded")
	// ErrInvalid is any other invalid argument, such as an empty old text.
	ErrInvalid = errors.New("memory: invalid argument")
)

// MatchError is a str_replace whose old text occurs on more than one line.
type MatchError struct {
	// Lines are the 1-based lines each occurrence starts on.
	Lines []int
}

func (e *MatchError) Error() string {
	s := make([]string, len(e.Lines))
	for i, l := range e.Lines {
		s[i] = strconv.Itoa(l)
	}
	return fmt.Sprintf("%v: lines %s", ErrMultipleMatches, strings.Join(s, ", "))
}

// Is reports whether target is ErrMultipleMatches.
func (e *MatchError) Is(target error) bool { return target == ErrMultipleMatches }

// LineError is an insert line outside [0, Lines].
type LineError struct {
	Line  int
	Lines int // lines in the file
}

func (e *LineError) Error() string {
	return fmt.Sprintf("%v: %d is not within [0, %d]", ErrInvalidLine, e.Line, e.Lines)
}

// Is reports whether target is ErrInvalidLine.
func (e *LineError) Is(target error) bool { return target == ErrInvalidLine }

// LimitError is a write past one of the [Limits].
type LimitError struct {
	// Limit names it: "file bytes", "scope bytes" or "files" for a Store;
	// "text bytes" or "items" for an Index.
	Limit string
	// Max is the limit; Want what the write would have made it.
	Max, Want int64
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("%v: %s would be %d, the limit is %d", ErrLimit, e.Limit, e.Want, e.Max)
}

// Is reports whether target is ErrLimit.
func (e *LimitError) Is(target error) bool { return target == ErrLimit }
