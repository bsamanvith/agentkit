package skill_test

import (
	"errors"
	"io/fs"
	"path"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/bsamanvith/agentkit/pkg/skill"
)

// md builds a SKILL.md from front matter lines and a body.
func md(front, body string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte("---\n" + front + "\n---\n" + body)}
}

// link is a symbolic link to target in a MapFS.
func link(target string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(target), Mode: fs.ModeSymlink | 0o777}
}

// load runs skill.Load and checks what holds for every successful load:
// skills sorted by name with unique names that follow the rule, a
// description, file indexes sorted, without SKILL.md, each path a clean
// relative path within the limits, and every indexed file readable (or
// refused only as binary).
func load(t *testing.T, fsys fs.FS, root string) []skill.Skill {
	t.Helper()
	skills, err := skill.Load(fsys, root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	checkSkills(t, skills)
	return skills
}

func checkSkills(t *testing.T, skills []skill.Skill) {
	t.Helper()
	if !slices.IsSortedFunc(skills, func(a, b skill.Skill) int { return strings.Compare(a.Name, b.Name) }) {
		t.Errorf("skills not sorted by name: %v", names(skills))
	}
	for i, s := range skills {
		if i > 0 && skills[i-1].Name == s.Name {
			t.Errorf("two skills named %q", s.Name)
		}
		if strings.TrimSpace(s.Description) == "" {
			t.Errorf("%s: empty description", s.Name)
		}
		if len(s.Files) > skill.MaxFiles-1 {
			t.Errorf("%s: %d files", s.Name, len(s.Files))
		}
		if !slices.IsSortedFunc(s.Files, func(a, b skill.File) int { return strings.Compare(a.Path, b.Path) }) {
			t.Errorf("%s: files not sorted", s.Name)
		}
		for _, f := range s.Files {
			if f.Path == skill.SkillFile || path.Clean(f.Path) != f.Path || !fs.ValidPath(f.Path) || f.Size > skill.MaxFileBytes {
				t.Errorf("%s: bad index entry %+v", s.Name, f)
			}
			data, err := s.Read(f.Path)
			switch {
			case errors.Is(err, skill.ErrBinary):
			case err != nil:
				t.Errorf("%s: read %s: %v", s.Name, f.Path, err)
			case int64(len(data)) != f.Size:
				t.Errorf("%s: read %s: %d bytes, index says %d", s.Name, f.Path, len(data), f.Size)
			}
		}
		if _, err := s.Read(skill.SkillFile); err != nil {
			t.Errorf("%s: read SKILL.md: %v", s.Name, err)
		}
	}
}

func names(skills []skill.Skill) []string {
	var out []string
	for _, s := range skills {
		out = append(out, s.Name)
	}
	return out
}
