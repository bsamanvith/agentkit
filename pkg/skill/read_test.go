package skill_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/bsamanvith/agentkit/pkg/skill"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// confined is a skill "a" next to a sibling skill and a secret outside the
// root, with nested files and a link inside the folder.
func confined() fstest.MapFS {
	return fstest.MapFS{
		"skills/a/SKILL.md":          md("name: a\ndescription: d", "A body"),
		"skills/a/refs/guide.md":     {Data: []byte("guide")},
		"skills/a/refs/deep/note.md": {Data: []byte("note")},
		"skills/a/alias.md":          link("refs/guide.md"),
		"skills/a/image.png":         {Data: []byte("\x89PNG\x00\x01")},
		"skills/a/latin1.txt":        {Data: []byte("caf\xe9")},
		"skills/a/abs.md":            {Data: []byte("abs")},
		"skills/b/SKILL.md":          md("name: b\ndescription: d", "B body"),
		"skills/b/secret.md":         {Data: []byte("SIBLING")},
		"secret.txt":                 {Data: []byte("SECRET")},
	}
}

// TestReadConfinement: skill_read returns the skill's own text files and
// refuses "..", absolute paths, links leaving the folder (even one swapped
// in after the load) and binary files.
func TestReadConfinement(t *testing.T) {
	t.Parallel()
	fsys := confined()
	skills := load(t, fsys, "skills")
	// After the load, a file becomes a link out of the folder.
	fsys["skills/a/refs/guide.md"] = link("../../b/secret.md")
	fsys["skills/a/refs/deep/note.md"] = link("../../../../secret.txt")
	fsys["skills/a/abs.md"] = link("/secret.txt")
	cat, err := skill.NewCatalog(skills...)
	if err != nil {
		t.Fatal(err)
	}
	read := readTool(t, cat)
	tests := []struct {
		path string
		want string // content, when it is read
		err  error
	}{
		{path: "SKILL.md", want: "---\nname: a\ndescription: d\n---\nA body"},
		{path: "./SKILL.md", want: "---\nname: a\ndescription: d\n---\nA body"},
		{path: "../b/secret.md", err: skill.ErrPath},
		{path: "refs/../../b/secret.md", err: skill.ErrPath},
		{path: "refs/deep/../../SKILL.md", err: skill.ErrPath},
		{path: "..", err: skill.ErrPath},
		{path: "/etc/passwd", err: skill.ErrPath},
		{path: "/skills/a/SKILL.md", err: skill.ErrPath},
		{path: `C:\Windows\win.ini`, err: skill.ErrPath},
		{path: `refs\guide.md`, err: skill.ErrPath},
		{path: "refs/guide.md\x00", err: skill.ErrPath},
		{path: "", err: skill.ErrPath},
		{path: ".", err: skill.ErrPath},
		{path: "refs/guide.md", err: skill.ErrPath},     // now a link to the sibling
		{path: "refs/deep/note.md", err: skill.ErrPath}, // now a link out of the root
		{path: "alias.md", err: skill.ErrPath},          // a link to a link out
		{path: "abs.md", err: skill.ErrPath},            // now a link to an absolute path
		{path: "image.png", err: skill.ErrBinary},
		{path: "latin1.txt", err: skill.ErrBinary},
		{path: "refs", err: skill.ErrNotFound},
		{path: "missing.md", err: skill.ErrNotFound},
	}
	for _, tc := range tests {
		got, err := read("a", tc.path)
		if tc.err != nil {
			if !errors.Is(err, tc.err) {
				t.Errorf("%q: err = %v, want %v", tc.path, err, tc.err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%q: %q, %v; want %q", tc.path, got, err, tc.want)
		}
	}
	if _, err := read("nope", "SKILL.md"); !errors.Is(err, skill.ErrNotFound) {
		t.Errorf("unknown skill: err = %v, want ErrNotFound", err)
	}
}

// TestReadConfinementOnDisk: the same refusals on a real folder, through
// os.Root and through os.DirFS.
func TestReadConfinementOnDisk(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for p, s := range map[string]string{
		"skills/a/SKILL.md":  "---\nname: a\ndescription: d\n---\nbody",
		"skills/a/refs/r.md": "ref",
		"skills/a/later.md":  "later",
		"skills/b/SKILL.md":  "---\nname: b\ndescription: d\n---\nB",
		"skills/b/secret.md": "SIBLING",
		"secret.txt":         "SECRET",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(filepath.Join(dir, "skills"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	sources := map[string][]skill.Skill{
		"os.Root":  load(t, root.FS(), "."),
		"os.DirFS": load(t, os.DirFS(dir), "skills"),
	}
	// After the load, a file is replaced by a link to the sibling skill.
	later := filepath.Join(dir, "skills/a/later.md")
	if err := os.Remove(later); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../b/secret.md", later); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	for name, skills := range sources {
		a := skills[0]
		if got, err := a.Read("refs/r.md"); err != nil || string(got) != "ref" {
			t.Errorf("%s: refs/r.md: %q, %v", name, got, err)
		}
		for _, p := range []string{"later.md", "../b/secret.md", "refs/../../secret.txt", "/etc/passwd"} {
			if got, err := a.Read(p); !errors.Is(err, skill.ErrPath) {
				t.Errorf("%s: %s: %q, %v; want ErrPath", name, p, got, err)
			}
		}
	}
}

// FuzzSkillPath: whatever path skill_read gets, it either fails or returns
// a file of the skill's own folder, never the sibling's or the root's
// secret.
func FuzzSkillPath(f *testing.F) {
	for _, p := range []string{
		"SKILL.md", "refs/guide.md", "refs/deep/note.md", "alias.md", "../b/secret.md",
		"refs/../../secret.txt", "refs/deep/../../../secret.txt", "/secret.txt", "a/../SKILL.md",
		"refs//guide.md", "./alias.md", "out.md", "loop.md", "refs/up/secret.md",
	} {
		f.Add(p)
	}
	fsys := confined()
	skills, err := skill.Load(fsys, "skills")
	if err != nil {
		f.Fatal(err)
	}
	// After the load: links out of the folder, a loop and a link to a
	// folder that leads up.
	fsys["skills/a/out.md"] = link("../b/secret.md")
	fsys["skills/a/loop.md"] = link("loop.md")
	fsys["skills/a/refs/up"] = link("../../b")
	own := map[string]bool{}
	for p, file := range fsys {
		if strings.HasPrefix(p, "skills/a/") && file.Mode&os.ModeSymlink == 0 {
			own[string(file.Data)] = true
		}
	}
	a := skills[0]
	f.Fuzz(func(t *testing.T, p string) {
		got, err := a.Read(p)
		if err != nil {
			return
		}
		if !own[string(got)] {
			t.Fatalf("Read(%q) = %q, not a file of the skill's folder", p, got)
		}
	})
}

// readTool returns a function that calls skill_read through the toolset.
func readTool(t *testing.T, cat *skill.Catalog) func(name, path string) (string, error) {
	t.Helper()
	tools, err := cat.Toolset()
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tool.NewRegistry(tools)
	if err != nil {
		t.Fatal(err)
	}
	return func(name, path string) (string, error) {
		in, _ := json.Marshal(map[string]string{"name": name, "path": path})
		out, err := reg.Invoke(context.Background(), skill.ReadTool, in)
		if err != nil {
			return "", err
		}
		var res struct{ Content string }
		if err := json.Unmarshal(out, &res); err != nil {
			t.Fatal(err)
		}
		return res.Content, nil
	}
}
