package skill

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Limits of a skill, checked by [Load].
const (
	// MaxSkillMDBytes is the largest SKILL.md.
	MaxSkillMDBytes = 64 << 10
	// MaxFileBytes is the largest other file of a skill.
	MaxFileBytes = 1 << 20
	// MaxFiles is the most files in a skill's folder, SKILL.md included.
	MaxFiles = 200
	// MaxDescriptionRunes is the longest description, in characters.
	MaxDescriptionRunes = 1024
)

// SkillFile is the name of the file that makes a folder a skill.
const SkillFile = "SKILL.md"

// maxLinks bounds the symbolic links followed to resolve one path.
const maxLinks = 40

var (
	// ErrInvalidSkill reports a skill that breaks a rule of the format; the
	// error is an [*Error] naming the folder and the [Rule].
	ErrInvalidSkill = errors.New("invalid skill")
	// ErrNotFound reports a skill name or a file that is not in the catalog
	// or the skill.
	ErrNotFound = errors.New("not found")
	// ErrPath reports a path a skill may not read: absolute, with a ".."
	// element, or through a symbolic link that leaves the skill's folder.
	ErrPath = errors.New("path outside the skill's folder")
	// ErrBinary reports a file that is not text (it holds a NUL byte or is
	// not UTF-8).
	ErrBinary = errors.New("not a text file")
	// ErrTooLarge reports a file over its size limit at read time.
	ErrTooLarge = errors.New("file too large")
)

// Rule names the rule an invalid skill breaks.
type Rule string

// The rules [Load] checks.
const (
	RuleSkillMD     Rule = "skill_md"     // the folder has no SKILL.md
	RuleFrontMatter Rule = "front_matter" // missing or malformed front matter
	RuleName        Rule = "name"         // name missing or not ^[a-zA-Z0-9_-]{1,64}$
	RuleDescription Rule = "description"  // description empty or too long
	RuleMetadata    Rule = "metadata"     // metadata not a map of strings
	RuleTools       Rule = "tools"        // tools not names or globs
	RuleSize        Rule = "size"         // SKILL.md or another file too large
	RuleFileCount   Rule = "file_count"   // too many files
	RuleDuplicate   Rule = "duplicate"    // two skills with one name
	RuleSymlink     Rule = "symlink"      // a symbolic link leaves the folder
	RuleRead        Rule = "read"         // the folder or a file cannot be read
)

// Error is a skill that failed to load. It matches [ErrInvalidSkill] with
// errors.Is, and its cause.
type Error struct {
	// Folder is the skill's folder, relative to the root given to Load.
	Folder string
	// Name is the skill's name when it is known.
	Name string
	Rule Rule
	Err  error
}

func (e *Error) Error() string {
	who := "skill folder " + e.Folder
	if e.Name != "" {
		who = fmt.Sprintf("skill %s (folder %s)", e.Name, e.Folder)
	}
	return fmt.Sprintf("%v: %s: %s: %v", ErrInvalidSkill, who, e.Rule, e.Err)
}

// Unwrap returns ErrInvalidSkill and the cause.
func (e *Error) Unwrap() []error { return []error{ErrInvalidSkill, e.Err} }

// File is one file of a skill's folder other than SKILL.md.
type File struct {
	// Path is relative to the skill's folder, with "/" between names.
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// Skill is one loaded skill. Build it with [Load]; its Read method needs the
// folder it came from.
type Skill struct {
	// Name identifies the skill (front matter "name").
	Name string
	// Description says what the skill does and when to use it (front
	// matter "description"); it is all the model sees until it loads the
	// skill.
	Description string
	// Metadata is the front matter's optional "metadata" map.
	Metadata map[string]string
	// Tools are tool names or globs the skill wants loaded when it is
	// loaded (front matter "tools"), for deferred tool loading (T085).
	Tools []string
	// Body is SKILL.md after the front matter: the instructions.
	Body string
	// Files indexes the other files of the folder, sorted by path.
	Files []File

	fsys   fs.FS
	dir    string // the folder in fsys, free of symbolic links
	folder string // the folder's name under the root, for errors
}

// clone returns a deep copy of s, sharing only the read-only fsys.
func (s Skill) clone() Skill {
	s.Metadata = maps.Clone(s.Metadata)
	s.Tools = slices.Clone(s.Tools)
	s.Files = slices.Clone(s.Files)
	return s
}

// namePattern is the tool name rule, shared by MCP and the model APIs.
var namePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// Load reads the skills under root in fsys: every folder directly under it
// is one skill and must hold a SKILL.md. Folders whose names start with "."
// and files directly under root are ignored. A symbolic link directly under
// root may name a skill folder elsewhere under root.
//
// It returns the skills sorted by name, or nil and every problem found, each
// an [*Error], joined.
func Load(fsys fs.FS, root string) ([]Skill, error) {
	root = path.Clean(root)
	if !fs.ValidPath(root) {
		return nil, fmt.Errorf("%w: root %q is not a valid fs.FS path", ErrInvalidSkill, root)
	}
	entries, err := fs.ReadDir(fsys, root)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidSkill, err)
	}
	var (
		skills []Skill
		errs   []error
	)
	for _, e := range entries {
		folder := e.Name()
		if strings.HasPrefix(folder, ".") {
			continue
		}
		dir := path.Join(root, folder)
		if e.Type()&fs.ModeSymlink != 0 {
			resolved, err := resolve(fsys, root, folder)
			if err != nil {
				errs = append(errs, &Error{Folder: folder, Rule: RuleSymlink, Err: err})
				continue
			}
			info, err := fs.Stat(fsys, resolved)
			if err != nil || !info.IsDir() {
				continue // a link to a file, or a broken one: not a skill
			}
			dir = resolved
		} else if !e.IsDir() {
			continue
		}
		s, err := loadOne(fsys, dir, folder)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		skills = append(skills, s)
	}
	errs = append(errs, duplicates(skills)...)
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	slices.SortFunc(skills, func(a, b Skill) int { return strings.Compare(a.Name, b.Name) })
	return skills, nil
}

// duplicates reports every skill whose name an earlier one has.
func duplicates(skills []Skill) []error {
	var errs []error
	seen := map[string]string{} // name: folder
	for _, s := range skills {
		if first, ok := seen[s.Name]; ok {
			errs = append(errs, &Error{
				Folder: s.folder, Name: s.Name, Rule: RuleDuplicate,
				Err: fmt.Errorf("the skill in folder %s has the same name", first),
			})
			continue
		}
		seen[s.Name] = s.folder
	}
	return errs
}

// loadOne loads the skill in dir (free of symbolic links); folder names it in
// errors.
func loadOne(fsys fs.FS, dir, folder string) (Skill, error) {
	fail := func(rule Rule, err error) (Skill, error) {
		return Skill{}, &Error{Folder: folder, Rule: rule, Err: err}
	}
	var (
		files   []File
		count   int
		mdSize  int64 = -1
		walkErr error
	)
	err := fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			walkErr = &Error{Folder: folder, Rule: RuleRead, Err: err}
			return walkErr
		}
		if p == dir {
			return nil
		}
		rel := strings.TrimPrefix(p, dir+"/")
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		info, err := statResolved(fsys, dir, rel, d)
		if err != nil {
			walkErr = &Error{Folder: folder, Rule: RuleSymlink, Err: err}
			return walkErr
		}
		if info == nil {
			return nil // a link to a folder, or not a file: not indexed
		}
		count++
		if count > MaxFiles {
			walkErr = &Error{Folder: folder, Rule: RuleFileCount, Err: fmt.Errorf("more than %d files", MaxFiles)}
			return walkErr
		}
		if rel == SkillFile {
			mdSize = info.Size()
			if mdSize > MaxSkillMDBytes {
				walkErr = &Error{Folder: folder, Rule: RuleSize, Err: fmt.Errorf("%s is %d bytes, over %d", SkillFile, mdSize, MaxSkillMDBytes)}
				return walkErr
			}
			return nil
		}
		if info.Size() > MaxFileBytes {
			walkErr = &Error{Folder: folder, Rule: RuleSize, Err: fmt.Errorf("%s is %d bytes, over %d", rel, info.Size(), MaxFileBytes)}
			return walkErr
		}
		files = append(files, File{Path: rel, Size: info.Size()})
		return nil
	})
	if walkErr != nil {
		return Skill{}, walkErr
	}
	if err != nil {
		return fail(RuleRead, err)
	}
	if mdSize < 0 {
		return fail(RuleSkillMD, fmt.Errorf("no %s in the folder", SkillFile))
	}
	s := Skill{fsys: fsys, dir: dir, folder: folder, Files: files}
	raw, err := s.readFile(SkillFile, MaxSkillMDBytes)
	if err != nil {
		return fail(RuleRead, err)
	}
	fm, body, err := parseSkillMD(string(raw))
	if err != nil {
		return fail(RuleFrontMatter, err)
	}
	s.Name, s.Description, s.Metadata, s.Tools, s.Body = fm.name, fm.description, fm.metadata, fm.tools, body
	e := func(rule Rule, err error) (Skill, error) {
		return Skill{}, &Error{Folder: folder, Name: fm.name, Rule: rule, Err: err}
	}
	switch {
	case !namePattern.MatchString(fm.name):
		return e(RuleName, fmt.Errorf("name %q must match %s", fm.name, namePattern))
	case strings.TrimSpace(fm.description) == "":
		return e(RuleDescription, errors.New("description is required"))
	case utf8.RuneCountInString(fm.description) > MaxDescriptionRunes:
		return e(RuleDescription, fmt.Errorf("description is %d characters, over %d", utf8.RuneCountInString(fm.description), MaxDescriptionRunes))
	case fm.metadataErr != nil:
		return e(RuleMetadata, fm.metadataErr)
	case fm.toolsErr != nil:
		return e(RuleTools, fm.toolsErr)
	}
	slices.SortFunc(s.Files, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	return s, nil
}

// statResolved describes the file at rel in dir as it is read: a regular
// file, or a symbolic link to one in dir. It returns nil for anything else
// (a link to a folder, a device) and an error for a link out of dir.
func statResolved(fsys fs.FS, dir, rel string, d fs.DirEntry) (fs.FileInfo, error) {
	if d.Type()&fs.ModeSymlink == 0 {
		if !d.Type().IsRegular() {
			return nil, nil
		}
		return d.Info()
	}
	resolved, err := resolve(fsys, dir, rel)
	if err != nil {
		return nil, err
	}
	info, err := fs.Stat(fsys, resolved)
	if err != nil || !info.Mode().IsRegular() {
		return nil, nil
	}
	return info, nil
}

// Read returns the file at p in the skill's folder: SKILL.md or a file of
// the index. p is relative to the folder, with "/" between names. It fails
// with [ErrPath] for an absolute path, a ".." element or a symbolic link
// that leaves the folder, [ErrNotFound] for a file not in the index,
// [ErrTooLarge] past [MaxFileBytes] and [ErrBinary] for a file that is not
// text.
func (s Skill) Read(p string) ([]byte, error) {
	clean, err := checkPath(p)
	if err != nil {
		return nil, err
	}
	if s.fsys == nil {
		return nil, fmt.Errorf("%w: skill %q was not loaded from a folder", ErrNotFound, s.Name)
	}
	limit := int64(MaxFileBytes)
	if clean == SkillFile {
		limit = MaxSkillMDBytes
	} else if _, ok := slices.BinarySearchFunc(s.Files, clean, func(f File, p string) int { return strings.Compare(f.Path, p) }); !ok {
		return nil, fmt.Errorf("%w: skill %q has no file %q", ErrNotFound, s.Name, clean)
	}
	data, err := s.readFile(clean, limit)
	if err != nil {
		return nil, err
	}
	if !isText(data) {
		return nil, fmt.Errorf("%w: %s", ErrBinary, clean)
	}
	return data, nil
}

// readFile reads rel in the skill's folder after resolving every symbolic
// link on the way, refusing one that leaves the folder.
func (s Skill) readFile(rel string, limit int64) ([]byte, error) {
	resolved, err := resolve(s.fsys, s.dir, rel)
	if err != nil {
		return nil, err
	}
	f, err := s.fsys.Open(resolved)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrNotFound, rel)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: %s is over %d bytes", ErrTooLarge, rel, limit)
	}
	return data, nil
}

// checkPath validates a path a caller (usually a model) gives and returns it
// cleaned: relative, with "/" between names, and no "." or ".." element.
func checkPath(p string) (string, error) {
	switch {
	case p == "":
		return "", fmt.Errorf("%w: the path is empty", ErrPath)
	case strings.ContainsRune(p, 0):
		return "", fmt.Errorf("%w: the path holds a NUL byte", ErrPath)
	case strings.HasPrefix(p, "/") || strings.Contains(p, `\`) || (len(p) > 1 && p[1] == ':'):
		return "", fmt.Errorf("%w: %q is absolute or uses \\; give a path relative to the skill's folder, with / between names", ErrPath, p)
	}
	for elem := range strings.SplitSeq(p, "/") {
		if elem == ".." {
			return "", fmt.Errorf("%w: %q has a .. element", ErrPath, p)
		}
	}
	clean := path.Clean(p)
	if !fs.ValidPath(clean) || clean == "." {
		return "", fmt.Errorf("%w: %q is not a file path", ErrPath, p)
	}
	return clean, nil
}

// resolve returns the path of rel inside dir with every symbolic link on the
// way followed, failing with ErrPath when one leads out of dir. dir must be
// free of symbolic links. Without fs.ReadLinkFS, fsys is taken to have no
// links.
func resolve(fsys fs.FS, dir, rel string) (string, error) {
	rl, _ := fsys.(fs.ReadLinkFS)
	pending := strings.Split(rel, "/")
	cur, links := dir, 0
	for len(pending) > 0 {
		elem := pending[0]
		pending = pending[1:]
		switch elem {
		case "", ".":
			continue
		case "..":
			if cur == dir {
				return "", fmt.Errorf("%w: %q leads out of the folder", ErrPath, rel)
			}
			cur = path.Dir(cur)
			continue
		}
		next := path.Join(cur, elem)
		if rl == nil {
			cur = next
			continue
		}
		info, err := rl.Lstat(next)
		if err != nil {
			return "", err
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			cur = next
			continue
		}
		if links++; links > maxLinks {
			return "", fmt.Errorf("%w: %q: too many symbolic links", ErrPath, rel)
		}
		target, err := rl.ReadLink(next)
		if err != nil {
			return "", err
		}
		if strings.HasPrefix(target, "/") || strings.Contains(target, `\`) || (len(target) > 1 && target[1] == ':') {
			return "", fmt.Errorf("%w: %q goes through a symbolic link to an absolute path", ErrPath, rel)
		}
		// The target is relative to the link's folder, cur.
		pending = append(strings.Split(target, "/"), pending...)
	}
	return cur, nil
}

// isText reports whether data is UTF-8 with no NUL byte.
func isText(data []byte) bool {
	return bytes.IndexByte(data, 0) < 0 && utf8.Valid(data)
}
