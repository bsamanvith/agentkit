package coretools_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/tool"
)

// workspace makes a root folder with files (path -> content) and returns it
// with a files toolset over it.
func workspace(t *testing.T, files map[string]string, settings string) (string, tool.Registry) {
	t.Helper()
	root := t.TempDir()
	for p, content := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if settings == "" {
		settings = "{}"
	}
	var extra map[string]any
	if err := jsonUnmarshal(settings, &extra); err != nil {
		t.Fatal(err)
	}
	extra["root"] = root
	return root, build(t, fmt.Sprintf(`{"name": "files", "type": "files", "settings": %s}`, jsonMarshal(t, extra)))
}

func read(t *testing.T, root, p string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func paths(m map[string]any, key string) []string {
	var out []string
	for _, e := range m[key].([]any) {
		out = append(out, e.(map[string]any)["path"].(string))
	}
	return out
}

func TestListFiles(t *testing.T) {
	_, reg := workspace(t, map[string]string{
		"README.md":        "hi",
		"src/main.go":      "package main",
		"src/util/util.go": "package util",
		".git/config":      "[core]",
	}, "")

	top := mustCall(t, reg, "list_files", `{}`)
	if got, want := paths(top, "entries"), []string{"README.md", "src"}; !slices.Equal(got, want) {
		t.Errorf("top level = %v, want %v", got, want)
	}
	all := mustCall(t, reg, "list_files", `{"recursive": true}`)
	if got, want := paths(all, "entries"), []string{"README.md", "src", "src/main.go", "src/util", "src/util/util.go"}; !slices.Equal(got, want) {
		t.Errorf("recursive = %v, want %v (no .git)", got, want)
	}
	gos := mustCall(t, reg, "list_files", `{"path": "src", "pattern": "*.go", "recursive": true}`)
	if got, want := paths(gos, "entries"), []string{"src/main.go", "src/util/util.go"}; !slices.Equal(got, want) {
		t.Errorf("*.go = %v, want %v", got, want)
	}
	cut := mustCall(t, reg, "list_files", `{"recursive": true, "limit": 2}`)
	if len(paths(cut, "entries")) != 2 || cut["truncated"] != true {
		t.Errorf("limit 2 = %v, want 2 entries and truncated", cut)
	}
	if e := mustCall(t, reg, "list_files", `{"path": "README.md"}`)["entries"].([]any); len(e) != 1 || e[0].(map[string]any)["size"] != 2.0 {
		t.Errorf("listing one file = %v", e)
	}
	wantErr(t, reg, "list_files", `{"pattern": "["}`, "bad glob")
}

func TestReadFile(t *testing.T) {
	_, reg := workspace(t, map[string]string{
		"a.txt":     "one\ntwo\nthree\nfour\n",
		"empty.txt": "",
		"bin.dat":   "PK\x00\x01",
		"big.txt":   strings.Repeat("x", 200),
	}, `{"max_file_bytes": 100}`)

	got := mustCall(t, reg, "read_file", `{"path": "a.txt"}`)
	if got["content"] != "one\ntwo\nthree\nfour\n" || got["total_lines"] != 4.0 || got["truncated"] != nil {
		t.Errorf("whole file = %v", got)
	}
	got = mustCall(t, reg, "read_file", `{"path": "./a.txt", "offset": 2, "limit": 2}`)
	if got["content"] != "two\nthree\n" || got["start_line"] != 2.0 || got["end_line"] != 3.0 || got["truncated"] != true {
		t.Errorf("lines 2-3 = %v", got)
	}
	if got := mustCall(t, reg, "read_file", `{"path": "empty.txt"}`); got["content"] != "" || got["total_lines"] != 0.0 {
		t.Errorf("empty file = %v", got)
	}
	wantErr(t, reg, "read_file", `{"path": "a.txt", "offset": 9}`, "past the end")
	wantErr(t, reg, "read_file", `{"path": "bin.dat"}`, "binary")
	wantErr(t, reg, "read_file", `{"path": "big.txt"}`, "over the 100-byte limit")
	wantErr(t, reg, "read_file", `{"path": "."}`, "folder")
	wantErr(t, reg, "read_file", `{"path": "missing.txt"}`, "no such file")
}

func TestSearchFiles(t *testing.T) {
	_, reg := workspace(t, map[string]string{
		"notes.md":    "TODO: write docs\ndone\n",
		"src/a.go":    "// todo: tests\nfunc A() {}\n// TODO: more\n",
		"src/b.txt":   "nothing here\n",
		"bin.dat":     "TODO\x00",
		".git/HEAD":   "TODO in git",
		"src/long.go": "// TODO " + strings.Repeat("y", 600) + "\n",
	}, "")

	got := mustCall(t, reg, "search_files", `{"pattern": "TODO"}`)
	want := []string{"notes.md:1", "src/a.go:3", "src/long.go:1"}
	var gotAt []string
	for _, m := range got["matches"].([]any) {
		m := m.(map[string]any)
		gotAt = append(gotAt, fmt.Sprintf("%s:%v", m["path"], m["line"]))
		if n := len([]rune(m["text"].(string))); n > 501 {
			t.Errorf("match text is %d runes, want it shortened", n)
		}
	}
	if !slices.Equal(gotAt, want) {
		t.Errorf("matches = %v, want %v (no binary files, no .git)", gotAt, want)
	}
	got = mustCall(t, reg, "search_files", `{"pattern": "todo", "ignore_case": true, "path": "src", "glob": "*.go", "limit": 2}`)
	if len(got["matches"].([]any)) != 2 || got["truncated"] != true {
		t.Errorf("ignore case with limit 2 = %v", got)
	}
	wantErr(t, reg, "search_files", `{"pattern": "("}`, "bad pattern")
}

func TestEditFile(t *testing.T) {
	root, reg := workspace(t, map[string]string{
		"main.go": "package main\n\nfunc main() {\n\tprintln(\"hi\")\n\tprintln(\"hi\")\n}\n",
	}, "")

	wantErr(t, reg, "edit_file", `{"path": "main.go", "old_text": "println(\"hi\")", "new_text": "x"}`, "appears 2 times")
	wantErr(t, reg, "edit_file", `{"path": "main.go", "old_text": "nope", "new_text": "x"}`, "not found")
	wantErr(t, reg, "edit_file", `{"path": "main.go", "old_text": "main", "new_text": "main"}`, "the same")
	if read(t, root, "main.go") != "package main\n\nfunc main() {\n\tprintln(\"hi\")\n\tprintln(\"hi\")\n}\n" {
		t.Fatal("a refused edit changed the file")
	}

	got := mustCall(t, reg, "edit_file", `{"path": "main.go", "old_text": "func main() {\n\tprintln(\"hi\")", "new_text": "func main() {\n\tprintln(\"hello\")"}`)
	if got["replacements"] != 1.0 || got["created"] != nil {
		t.Errorf("edit = %v", got)
	}
	got = mustCall(t, reg, "edit_file", `{"path": "main.go", "old_text": "println", "new_text": "fmt.Println", "replace_all": true}`)
	if got["replacements"] != 2.0 {
		t.Errorf("replace_all = %v", got)
	}
	if want := "package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n\tfmt.Println(\"hi\")\n}\n"; read(t, root, "main.go") != want {
		t.Errorf("file = %q, want %q", read(t, root, "main.go"), want)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 1 {
		t.Errorf("root holds %d entries, want only main.go (no temporary files left)", len(entries))
	}
}

// Creating a file is an edit from nothing: an empty old_text.
func TestEditCreatesFiles(t *testing.T) {
	root, reg := workspace(t, map[string]string{"keep.txt": "keep"}, "")

	got := mustCall(t, reg, "edit_file", `{"path": "docs/new/plan.md", "new_text": "# Plan\n"}`)
	if got["created"] != true || got["path"] != "docs/new/plan.md" {
		t.Errorf("create = %v", got)
	}
	if read(t, root, "docs/new/plan.md") != "# Plan\n" {
		t.Error("created file has the wrong content")
	}
	info, _ := os.Stat(filepath.Join(root, "docs/new/plan.md"))
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644", info.Mode().Perm())
	}
	wantErr(t, reg, "edit_file", `{"path": "keep.txt", "old_text": "", "new_text": "gone"}`, "already exists")
	wantErr(t, reg, "edit_file", `{"path": "docs", "new_text": "x"}`, "already exists")
	wantErr(t, reg, "edit_file", `{"path": "absent.txt", "old_text": "a", "new_text": "b"}`, "leave old_text empty")
	if read(t, root, "keep.txt") != "keep" {
		t.Error("an existing file was overwritten")
	}
}

func TestEditKeepsMode(t *testing.T) {
	root, reg := workspace(t, map[string]string{"run.sh": "echo a\n"}, "")
	if err := os.Chmod(filepath.Join(root, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustCall(t, reg, "edit_file", `{"path": "run.sh", "old_text": "a", "new_text": "b"}`)
	if info, _ := os.Stat(filepath.Join(root, "run.sh")); info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v, want 0755 kept", info.Mode().Perm())
	}
}

// Edits in one toolset are serialized: concurrent edits of one file all land.
func TestConcurrentEdits(t *testing.T) {
	const n = 20
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	root, reg := workspace(t, map[string]string{"f.txt": b.String()}, "")
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			if _, err := call(t, reg, "edit_file", fmt.Sprintf(`{"path": "f.txt", "old_text": "line %d\n", "new_text": "LINE %d\n"}`, i, i)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if got := read(t, root, "f.txt"); strings.Contains(got, "line ") {
		t.Errorf("lost an edit:\n%s", got)
	}
}

// Nothing outside the root is reachable: not by an absolute path, not by
// "..", not through a symlink.
func TestConfinedToRoot(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("SECRET\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, reg := workspace(t, map[string]string{"in.txt": "in\n"}, "")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("in.txt", filepath.Join(root, "inner.txt")); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct{ tool, input, want string }{
		{"read_file", `{"path": "../` + filepath.Base(outside) + `/secret.txt"}`, "outside the root"},
		{"read_file", `{"path": "` + filepath.Join(outside, "secret.txt") + `"}`, "absolute"},
		{"read_file", `{"path": "link.txt"}`, "escapes"},
		{"read_file", `{"path": "linkdir/secret.txt"}`, "escapes"},
		{"list_files", `{"path": "linkdir"}`, "escapes"},
		{"list_files", `{"path": ".."}`, "outside the root"},
		{"search_files", `{"pattern": "x", "path": "linkdir"}`, "escapes"},
		{"edit_file", `{"path": "../evil.txt", "new_text": "x"}`, "outside the root"},
		{"edit_file", `{"path": "linkdir/evil.txt", "new_text": "x"}`, "escapes"},
		{"edit_file", `{"path": "link.txt", "old_text": "SECRET", "new_text": "x"}`, "symlink"},
		{"edit_file", `{"path": ".", "new_text": "x"}`, "root folder"},
	} {
		wantErr(t, reg, c.tool, c.input, c.want)
	}
	// A recursive search never follows the symlinks out.
	got := mustCall(t, reg, "search_files", `{"pattern": "SECRET"}`)
	if len(got["matches"].([]any)) != 0 {
		t.Errorf("search found %v outside the root", got["matches"])
	}
	if _, err := os.Stat(filepath.Join(outside, "evil.txt")); err == nil {
		t.Error("a file was created outside the root")
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "secret.txt")); string(b) != "SECRET\n" {
		t.Error("a file outside the root was changed")
	}
	// A symlink that stays inside the root reads like its target.
	if got := mustCall(t, reg, "read_file", `{"path": "inner.txt"}`); got["content"] != "in\n" {
		t.Errorf("inner symlink = %v", got)
	}
}

// A read-only toolset is the files type with edit_file excluded.
func TestReadOnlyToolset(t *testing.T) {
	root := t.TempDir()
	reg := build(t, `{"name": "docs", "type": "files", "settings": {"root": "`+root+`"}, "exclude": ["edit_file"]}`)
	if _, err := call(t, reg, "edit_file", `{"path": "x", "new_text": "y"}`); err == nil {
		t.Fatal("edit_file ran in a toolset that excludes it")
	}
}

func TestCreateRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "a", "workspace")
	reg := build(t, `{"name": "f", "type": "files", "settings": {"root": "`+root+`", "create_root": true}}`)
	mustCall(t, reg, "edit_file", `{"path": "x.txt", "new_text": "y"}`)
	if read(t, root, "x.txt") != "y" {
		t.Error("file not created in the new root")
	}
}
