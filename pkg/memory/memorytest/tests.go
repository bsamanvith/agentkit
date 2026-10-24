package memorytest

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/memory"
)

// rig is one store in one scope, stepping through ops with the checks.
type rig struct {
	t     *testing.T
	ctx   context.Context
	s     memory.Store
	l     memory.Limits
	files map[string]string
}

func newRig(t *testing.T, newStore NewStore, cfg memory.Config) *rig {
	t.Helper()
	full, err := cfg.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	return &rig{t: t, ctx: Ctx(t, "t1", "u1"), s: newStore(t, cfg), l: full.Limits, files: map[string]string{}}
}

// do runs o and returns its error; the checks run after it.
func (r *rig) do(o Op) error {
	r.t.Helper()
	var err error
	r.files, err = Step(r.ctx, r.t, r.s, r.l, r.files, o)
	return err
}

// ok runs o and fails the test on an error.
func (r *rig) ok(o Op) {
	r.t.Helper()
	if err := r.do(o); err != nil {
		r.t.Fatalf("%v: %v", o, err)
	}
}

// fails runs o and checks its error matches want.
func (r *rig) fails(o Op, want error) {
	r.t.Helper()
	_ = r.failsWith(o, want)
}

// failsWith is fails, returning the error for a closer look.
func (r *rig) failsWith(o Op, want error) error {
	r.t.Helper()
	err := r.do(o)
	if !errors.Is(err, want) {
		r.t.Errorf("%v: error %v, want %v", o, err, want)
	}
	return err
}

func (r *rig) view(p string) memory.View {
	r.t.Helper()
	v, err := r.s.View(r.ctx, p)
	if err != nil {
		r.t.Fatalf("View(%s): %v", p, err)
	}
	return v
}

func create(p, text string) Op      { return Op{Kind: "create", Path: p, Text: text} }
func replace(p, old, new string) Op { return Op{Kind: "str_replace", Path: p, Old: old, New: new} }
func insert(p string, line int, text string) Op {
	return Op{Kind: "insert", Path: p, Line: line, Text: text}
}
func del(p string) Op           { return Op{Kind: "delete", Path: p} }
func rename(from, to string) Op { return Op{Kind: "rename", Path: from, To: to} }
func view(p string) Op          { return Op{Kind: "view", Path: p} }

// testCommands: each command as documented, with its error cases.
func testCommands(t *testing.T, newStore NewStore) {
	r := newRig(t, newStore, memory.Config{})

	// view of an empty store is the root's listing, not an error.
	if v := r.view(memory.Root); !v.Dir || v.Path != memory.Root || len(v.Entries) != 0 || v.Size != 0 {
		t.Errorf("View of an empty root = %+v, want an empty directory", v)
	}
	r.fails(view("/memories/missing.md"), memory.ErrNotFound)

	// create, and create again overwrites.
	r.ok(create("/memories/prefs.md", "units: metric\n"))
	if v := r.view("/memories/prefs.md"); v.Dir || v.Text != "units: metric\n" || v.Size != 14 {
		t.Errorf("View after create = %+v", v)
	}
	r.ok(create("/memories/prefs.md", "units: metric\ntone: brief\n"))

	// str_replace: exactly one match; zero and two are errors.
	r.ok(replace("/memories/prefs.md", "brief", "warm"))
	r.fails(replace("/memories/prefs.md", "absent", "x"), memory.ErrNoMatch)
	r.ok(create("/memories/twice.md", "a\nx\nb\nx\n"))
	err := r.failsWith(replace("/memories/twice.md", "x", "y"), memory.ErrMultipleMatches)
	var m *memory.MatchError
	if !errors.As(err, &m) || !slices.Equal(m.Lines, []int{2, 4}) {
		t.Errorf("two matches: %v, want a *MatchError on lines [2 4]", err)
	}
	r.fails(replace("/memories/twice.md", "", "y"), memory.ErrInvalid)
	r.fails(replace("/memories/missing.md", "x", "y"), memory.ErrNotFound)
	r.ok(replace("/memories/twice.md", "a\nx", "a")) // new_str empty deletes

	// insert: after a line, 0 for the start, the end; past the end is an
	// error.
	r.ok(create("/memories/todo.md", "one\ntwo"))
	r.ok(insert("/memories/todo.md", 0, "zero"))
	r.ok(insert("/memories/todo.md", 3, "three\n"))
	r.ok(insert("/memories/todo.md", 1, "half"))
	if v := r.view("/memories/todo.md"); v.Text != "zero\nhalf\none\ntwo\nthree\n" {
		t.Errorf("after inserts: %q", v.Text)
	}
	err = r.failsWith(insert("/memories/todo.md", 6, "past"), memory.ErrInvalidLine)
	var le *memory.LineError
	if !errors.As(err, &le) || le.Line != 6 || le.Lines != 5 {
		t.Errorf("insert past the end: %v, want a *LineError of line 6 in 5", err)
	}
	r.fails(insert("/memories/todo.md", -1, "before"), memory.ErrInvalidLine)
	r.fails(insert("/memories/missing.md", 0, "x"), memory.ErrNotFound)
	r.ok(create("/memories/empty.md", ""))
	r.ok(insert("/memories/empty.md", 0, "first"))

	// rename: onto an existing file is an error; the file moves.
	r.fails(rename("/memories/todo.md", "/memories/prefs.md"), memory.ErrExists)
	r.fails(rename("/memories/missing.md", "/memories/new.md"), memory.ErrNotFound)
	r.ok(rename("/memories/todo.md", "/memories/done/todo.md"))
	r.fails(view("/memories/todo.md"), memory.ErrNotFound)

	// delete.
	r.ok(del("/memories/twice.md"))
	r.fails(del("/memories/twice.md"), memory.ErrNotFound)
	r.fails(del(memory.Root), memory.ErrInvalidPath)
	r.fails(rename(memory.Root, "/memories/x"), memory.ErrInvalidPath)
}

// testDirectories: directories are implicit, listed two levels deep, and
// deleted and renamed whole.
func testDirectories(t *testing.T, newStore NewStore) {
	r := newRig(t, newStore, memory.Config{})
	r.ok(create("/memories/a/one.md", "1"))
	r.ok(create("/memories/a/b/two.md", "22"))
	r.ok(create("/memories/a/b/c/three.md", "333"))
	r.ok(create("/memories/top.md", "4444"))
	r.ok(create("/memories/ab.md", "5")) // a name that a's own start

	v := r.view(memory.Root)
	var paths []string
	for _, e := range v.Entries {
		paths = append(paths, e.Path)
	}
	// Two levels down: a, a/b, a/one.md, ab.md, top.md; not a/b/two.md.
	if want := []string{"/memories/a", "/memories/a/b", "/memories/a/one.md", "/memories/ab.md", "/memories/top.md"}; !slices.Equal(paths, want) || v.Size != 11 {
		t.Errorf("root listing = %v (%d bytes), want %v (11 bytes)", paths, v.Size, want)
	}
	if v := r.view("/memories/a"); v.Size != 6 || len(v.Entries) != 4 {
		t.Errorf("View(/memories/a) = %+v, want 6 bytes in a/b, a/b/c, a/b/two.md, a/one.md", v)
	}
	if v := r.view("/memories//a/./b/"); v.Path != "/memories/a/b" || !v.Dir || v.Size != 5 {
		t.Errorf("View of an unclean path = %+v, want /memories/a/b, 5 bytes", v)
	}

	// A file command on a directory, a file under a file.
	r.fails(create("/memories/a", "x"), memory.ErrIsDir)
	r.fails(create(memory.Root, "x"), memory.ErrIsDir)
	r.fails(replace("/memories/a", "x", "y"), memory.ErrIsDir)
	r.fails(insert("/memories/a/b", 0, "x"), memory.ErrIsDir)
	r.fails(create("/memories/top.md/inner.md", "x"), memory.ErrNotDir)
	r.fails(rename("/memories/a/one.md", "/memories/top.md/one.md"), memory.ErrNotDir)

	// Rename a directory: everything under it moves; not into itself; not
	// onto a directory that exists.
	r.fails(rename("/memories/a", "/memories/a/b/a"), memory.ErrInvalidPath)
	r.fails(rename("/memories/a/b", "/memories/a"), memory.ErrExists)
	r.ok(rename("/memories/a", "/memories/z"))
	if v := r.view("/memories/z/b/c/three.md"); v.Text != "333" {
		t.Errorf("moved file = %q", v.Text)
	}
	r.fails(view("/memories/a"), memory.ErrNotFound)

	// Delete a directory: everything under it goes; the parent of a last
	// file goes with it.
	r.ok(del("/memories/z/b"))
	r.ok(del("/memories/z/one.md"))
	r.fails(view("/memories/z"), memory.ErrNotFound)
	if v := r.view(memory.Root); len(v.Entries) != 2 || v.Entries[1].Path != "/memories/top.md" {
		t.Errorf("root after deletes = %+v", v.Entries)
	}
}

// testIsolation: tenants, and users of one tenant with ScopeUser, never see
// each other's files; a context without a tenant or user is refused.
func testIsolation(t *testing.T, newStore NewStore) {
	for _, scope := range []memory.Scope{memory.ScopeTenant, memory.ScopeUser} {
		t.Run(string(scope), func(t *testing.T) {
			t.Parallel()
			s := newStore(t, memory.Config{Scope: scope})
			a := Ctx(t, "tenant-a", "u1")
			b := Ctx(t, "tenant-b", "u1")
			a2 := Ctx(t, "tenant-a", "u2")
			if err := s.Create(a, "/memories/secret.md", "a's"); err != nil {
				t.Fatal(err)
			}
			if err := s.Create(b, "/memories/b.md", "b's"); err != nil {
				t.Fatal(err)
			}
			// Tenant B sees only its own file, and cannot reach A's.
			checkOnly(b, t, s, "/memories/b.md")
			for _, o := range []Op{
				view("/memories/secret.md"), replace("/memories/secret.md", "a", "b"),
				insert("/memories/secret.md", 0, "x"), del("/memories/secret.md"), rename("/memories/secret.md", "/memories/x.md"),
			} {
				if err := o.Apply(b, s); !errors.Is(err, memory.ErrNotFound) {
					t.Errorf("tenant b: %v = %v, want ErrNotFound", o, err)
				}
			}
			// A second user of tenant A: the same files with ScopeTenant,
			// none with ScopeUser.
			if scope == memory.ScopeTenant {
				checkOnly(a2, t, s, "/memories/secret.md")
			} else {
				checkOnly(a2, t, s)
				if err := s.Create(a2, "/memories/secret.md", "u2's"); err != nil {
					t.Fatal(err)
				}
				if v, err := s.View(a, "/memories/secret.md"); err != nil || v.Text != "a's" {
					t.Errorf("u1's file after u2 wrote its own = %+v, %v", v, err)
				}
			}
			checkOnly(a, t, s, "/memories/secret.md")

			// No tenant, and with ScopeUser no user: refused.
			for _, o := range []Op{
				view(memory.Root), create("/memories/x.md", "x"), replace("/memories/secret.md", "a", "b"),
				insert("/memories/secret.md", 0, "x"), del("/memories/secret.md"), rename("/memories/secret.md", "/memories/x.md"),
			} {
				if err := o.Apply(context.Background(), s); !errors.Is(err, memory.ErrNoTenant) {
					t.Errorf("no tenant: %v = %v, want ErrNoTenant", o, err)
				}
				if scope != memory.ScopeUser {
					continue
				}
				if err := o.Apply(Ctx(t, "tenant-a", ""), s); !errors.Is(err, memory.ErrNoUser) {
					t.Errorf("no user: %v = %v, want ErrNoUser", o, err)
				}
			}
			checkOnly(a, t, s, "/memories/secret.md")
		})
	}
}

// checkOnly checks the scope of ctx holds exactly the files paths.
func checkOnly(ctx context.Context, t *testing.T, s memory.Store, paths ...string) {
	t.Helper()
	files, err := Snapshot(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for p := range files {
		got = append(got, p)
	}
	slices.Sort(got)
	slices.Sort(paths)
	if !slices.Equal(got, paths) {
		t.Errorf("files = %v, want %v", got, paths)
	}
}

// testPathRules: every command refuses a path outside /memories, with "..",
// or too long, and changes nothing.
func testPathRules(t *testing.T, newStore NewStore) {
	r := newRig(t, newStore, memory.Config{})
	r.ok(create("/memories/keep.md", "keep"))
	long := memory.Root + "/" + strings.Repeat("d/", memory.MaxPathBytes/2) + "x"
	bad := []string{
		"", "memories/x", "/memoriesx", "/etc/passwd", "/", "/memories/../etc/passwd",
		"/memories/a/../../x", "/memories/..", "/memories/.hidden", `/memories/a\..\x`,
		"/memories/%2e%2e/x", "/memories/a%2fb", "/memories/a\x00b", long,
		"/memories/" + strings.Repeat("n", memory.MaxNameBytes+1),
	}
	for _, p := range bad {
		for _, o := range []Op{
			view(p), create(p, "x"), replace(p, "keep", "x"), insert(p, 0, "x"),
			del(p), rename(p, "/memories/y.md"), rename("/memories/keep.md", p),
		} {
			r.fails(o, memory.ErrInvalidPath)
		}
	}
	// A rename whose moved paths would be too long.
	r.ok(create("/memories/d/"+strings.Repeat("f", memory.MaxNameBytes), "x"))
	r.fails(rename("/memories/d", "/memories/"+strings.Repeat("e", memory.MaxNameBytes)), memory.ErrInvalidPath)
}

// testLimits: writes past the file, scope and count limits fail and change
// nothing; writes that shrink go through.
func testLimits(t *testing.T, newStore NewStore) {
	r := newRig(t, newStore, memory.Config{Limits: memory.Limits{MaxFileBytes: 10, MaxScopeBytes: 25, MaxFiles: 3}})
	r.fails(create("/memories/big.md", strings.Repeat("x", 11)), memory.ErrLimit)
	r.ok(create("/memories/a.md", strings.Repeat("a", 10)))
	r.fails(replace("/memories/a.md", "a", "bb"), memory.ErrMultipleMatches)
	r.ok(create("/memories/b.md", "0123456789"))
	r.fails(replace("/memories/b.md", "0", "00"), memory.ErrLimit) // 11 bytes in the file
	r.fails(insert("/memories/b.md", 1, "x"), memory.ErrLimit)
	r.ok(create("/memories/c.md", "12345"))
	r.fails(create("/memories/c.md", "123456"), memory.ErrLimit) // 26 in the scope
	r.fails(create("/memories/d.md", ""), memory.ErrLimit)       // a fourth file
	var le *memory.LimitError
	if err := r.do(create("/memories/c.md", "1234567")); !errors.As(err, &le) || le.Limit != "scope bytes" || le.Max != 25 || le.Want != 27 {
		t.Errorf("past the scope limit: %v, want a *LimitError of scope bytes 27 > 25", err)
	}
	// Shrinking and deleting free room.
	r.ok(replace("/memories/b.md", "0123", ""))
	r.ok(create("/memories/c.md", "123456789"))
	r.ok(del("/memories/a.md"))
	r.ok(create("/memories/d.md", "d"))
	// A rename keeps the count and the bytes.
	r.ok(rename("/memories/d.md", "/memories/e/d.md"))
}

// testText: files hold readable text. Tabs, line ends and any script go
// in; a write of bytes that are not UTF-8, or of another control character,
// fails with ErrNotText and changes nothing, whichever command writes it.
func testText(t *testing.T, newStore NewStore) {
	r := newRig(t, newStore, memory.Config{})
	r.ok(create("/memories/notes.md", "col\tcol\r\nunits: metric\nname: Søren, 東京 🙂\n"))
	for _, bad := range []string{"\x00", "a\x1b[31m", "\xff\xfe", "\u0085", "\x7f", "ok\xc3"} {
		r.fails(create("/memories/bin.md", bad), memory.ErrNotText)
		r.fails(create("/memories/notes.md", bad), memory.ErrNotText)
		r.fails(replace("/memories/notes.md", "metric", bad), memory.ErrNotText)
		r.fails(insert("/memories/notes.md", 1, bad), memory.ErrNotText)
	}
	r.ok(replace("/memories/notes.md", "metric", "imperial\t(US)"))
	r.ok(insert("/memories/notes.md", 0, "# Notes"))
}

// testCanceled: a canceled context is refused before anything is done.
func testCanceled(t *testing.T, newStore NewStore) {
	s := newStore(t, memory.Config{})
	ctx, cancel := context.WithCancel(Ctx(t, "t1", ""))
	cancel()
	if err := s.Create(ctx, "/memories/x.md", "x"); !errors.Is(err, context.Canceled) {
		t.Errorf("Create on a canceled context = %v, want context.Canceled", err)
	}
	checkOnly(Ctx(t, "t1", ""), t, s)
}
