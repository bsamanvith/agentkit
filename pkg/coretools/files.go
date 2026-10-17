package coretools

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// FilesType is the type of the [Files] factory.
const FilesType = "files"

const (
	defaultMaxFileBytes = 1 << 20  // 1 MiB
	maxMaxFileBytes     = 64 << 20 // 64 MiB
	defaultReadLines    = 2000
	maxReadLines        = 10_000
	defaultListLimit    = 500
	maxListLimit        = 5000
	defaultSearchLimit  = 100
	maxSearchLimit      = 1000
	maxMatchRunes       = 500
	binarySniffBytes    = 8000
)

// Files returns the "files" factory: list_files, read_file and search_files
// (read/low) and edit_file (write/medium) on the files under one root folder.
// Paths in calls are relative to the root, with "/" between names; nothing
// outside the root is reachable, by ".." or by a symlink (see [os.Root]).
//
// Settings:
//
//	{"root": "/srv/workspace", "create_root": false, "max_file_bytes": 1048576}
//
// root is required: an absolute path, or one relative to the server's working
// directory. create_root creates it when missing (otherwise a missing root
// fails the build). max_file_bytes (default 1 MiB, at most 64 MiB) is the
// largest file read_file, search_files and edit_file open.
//
// edit_file replaces old_text with new_text; old_text must appear exactly
// once unless replace_all is set. An empty old_text creates a new file (and
// its folders), so creating is an edit from nothing. Writes go to a temporary
// file renamed over the original, so a reader never sees half a file, and
// edits in one toolset are serialized.
func Files() toolset.Factory { return filesFactory{} }

type filesFactory struct{}

func (filesFactory) Type() string { return FilesType }

type filesSettings struct {
	Root         string `json:"root"`
	CreateRoot   bool   `json:"create_root,omitempty"`
	MaxFileBytes int64  `json:"max_file_bytes,omitempty"`
}

type files struct {
	root    *os.Root
	fsys    fs.FS
	maxSize int64
	edits   sync.Mutex
}

type (
	listIn struct {
		Path      string `json:"path,omitempty" jsonschema:"folder to list, relative to the root (default: the root)"`
		Pattern   string `json:"pattern,omitempty" jsonschema:"only names matching this glob, e.g. *.go"`
		Recursive bool   `json:"recursive,omitempty" jsonschema:"list subfolders too"`
		Limit     int    `json:"limit,omitempty" jsonschema:"most entries to return (default 500, at most 5000)"`
	}
	entry struct {
		Path string `json:"path"`
		// Type is file, dir, symlink or other.
		Type string `json:"type"`
		Size int64  `json:"size,omitempty"`
	}
	listOut struct {
		Entries   []entry `json:"entries"`
		Truncated bool    `json:"truncated,omitempty"`
	}

	readIn struct {
		Path   string `json:"path" jsonschema:"file to read, relative to the root"`
		Offset int    `json:"offset,omitempty" jsonschema:"first line to return, from 1 (default 1)"`
		Limit  int    `json:"limit,omitempty" jsonschema:"most lines to return (default 2000, at most 10000)"`
	}
	readOut struct {
		Path       string `json:"path"`
		Content    string `json:"content"`
		StartLine  int    `json:"start_line"`
		EndLine    int    `json:"end_line"`
		TotalLines int    `json:"total_lines"`
		// Truncated is true when lines after EndLine were left out.
		Truncated bool `json:"truncated,omitempty"`
	}

	searchIn struct {
		Pattern    string `json:"pattern" jsonschema:"regular expression (RE2 syntax) to find in each line"`
		Path       string `json:"path,omitempty" jsonschema:"file or folder to search, relative to the root (default: the root)"`
		Glob       string `json:"glob,omitempty" jsonschema:"only files whose name matches this glob, e.g. *.md"`
		IgnoreCase bool   `json:"ignore_case,omitempty" jsonschema:"match regardless of case"`
		Limit      int    `json:"limit,omitempty" jsonschema:"most matches to return (default 100, at most 1000)"`
	}
	match struct {
		Path string `json:"path"`
		Line int    `json:"line"`
		Text string `json:"text"`
	}
	searchOut struct {
		Matches       []match `json:"matches"`
		FilesSearched int     `json:"files_searched"`
		Truncated     bool    `json:"truncated,omitempty"`
	}

	editIn struct {
		Path       string `json:"path" jsonschema:"file to edit or create, relative to the root"`
		OldText    string `json:"old_text,omitempty" jsonschema:"exact text to replace; empty or left out creates a new file with new_text"`
		NewText    string `json:"new_text" jsonschema:"the replacement text, or the new file's content"`
		ReplaceAll bool   `json:"replace_all,omitempty" jsonschema:"replace every occurrence of old_text, not just one"`
	}
	editOut struct {
		Path         string `json:"path"`
		Created      bool   `json:"created,omitempty"`
		Replacements int    `json:"replacements"`
	}
)

func (filesFactory) Build(_ context.Context, settings json.RawMessage) ([]tool.Tool, error) {
	var s filesSettings
	if err := toolset.DecodeSettings(settings, &s); err != nil {
		return nil, err
	}
	switch {
	case s.Root == "":
		return nil, fmt.Errorf("%w: root is required", toolset.ErrInvalidSettings)
	case s.MaxFileBytes < 0 || s.MaxFileBytes > maxMaxFileBytes:
		return nil, fmt.Errorf("%w: max_file_bytes %d out of range 1..%d", toolset.ErrInvalidSettings, s.MaxFileBytes, maxMaxFileBytes)
	case s.MaxFileBytes == 0:
		s.MaxFileBytes = defaultMaxFileBytes
	}
	if s.CreateRoot {
		if err := os.MkdirAll(s.Root, 0o755); err != nil {
			return nil, fmt.Errorf("files: create root: %w", err)
		}
	}
	// The root stays open for the life of the process, like the toolset.
	root, err := os.OpenRoot(s.Root)
	if err != nil {
		return nil, fmt.Errorf("files: open root: %w", err)
	}
	f := &files{root: root, fsys: root.FS(), maxSize: s.MaxFileBytes}

	read := func(title, icon, present, past string) tool.FuncOption {
		return tool.WithMetadata(tool.Metadata{
			Kind: tool.KindRead, Risk: tool.RiskLow, Tags: []string{tool.TagIdempotent},
			Display: tool.Display{Title: title, Icon: icon, Present: present, Past: past},
		})
	}
	var tools []tool.Tool
	add := func(t tool.Tool, err error) error {
		if err == nil {
			tools = append(tools, t)
		}
		return err
	}
	err = errors.Join(
		add(tool.Func("list_files", "List the files and folders in a folder (never .git), optionally recursively and filtered by a name glob.",
			f.list, read("List files", "page", "Listing files", "Listed files"))),
		add(tool.Func("read_file", "Read a text file, or a range of its lines. Large files: read in parts with offset and limit.",
			f.read, read("Read file", "page", "Reading a file", "Read a file"))),
		add(tool.Func("search_files", "Search the text files under a folder for lines matching a regular expression.",
			f.search, read("Search files", "search", "Searching files", "Searched files"))),
		add(tool.Func("edit_file",
			"Edit a text file by replacing old_text with new_text (old_text must appear exactly once unless replace_all). "+
				"With an empty old_text, create a new file containing new_text.",
			f.edit, tool.WithMetadata(tool.Metadata{
				Kind: tool.KindWrite, Risk: tool.RiskMedium,
				Display: tool.Display{Title: "Edit file", Icon: "tool", Present: "Editing a file", Past: "Edited a file"},
			}))),
	)
	if err != nil {
		root.Close()
		return nil, err
	}
	return tools, nil
}

// clean turns a call's path into an fs.FS path: slash-separated, relative to
// the root, never above it. os.Root also refuses escapes; this gives the
// model a clear message first.
func clean(p string) (string, error) {
	if p == "" {
		return ".", nil
	}
	if strings.ContainsRune(p, 0) {
		return "", errors.New("the path contains a NUL byte")
	}
	if strings.HasPrefix(p, "/") || filepath.IsAbs(p) {
		return "", fmt.Errorf("%q is absolute: give a path relative to the root folder", p)
	}
	c := path.Clean(filepath.ToSlash(p))
	if c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("%q is outside the root folder", p)
	}
	return c, nil
}

func limit(n, def, max int) (int, error) {
	switch {
	case n < 0:
		return 0, fmt.Errorf("limit %d is negative", n)
	case n == 0:
		return def, nil
	case n > max:
		return max, nil
	}
	return n, nil
}

func checkGlob(g string) error {
	if _, err := path.Match(g, ""); err != nil {
		return fmt.Errorf("bad glob %q: %w", g, err)
	}
	return nil
}

func typeOf(m fs.FileMode) string {
	switch {
	case m.IsDir():
		return "dir"
	case m&fs.ModeSymlink != 0:
		return "symlink"
	case m.IsRegular():
		return "file"
	}
	return "other"
}

func (f *files) list(ctx context.Context, in listIn) (listOut, error) {
	p, err := clean(in.Path)
	if err != nil {
		return listOut{}, err
	}
	n, err := limit(in.Limit, defaultListLimit, maxListLimit)
	if err != nil {
		return listOut{}, err
	}
	if err := checkGlob(in.Pattern); err != nil {
		return listOut{}, err
	}
	out := listOut{Entries: []entry{}}
	errFull := errors.New("full")
	err = fs.WalkDir(f.fsys, p, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == p {
			if !d.IsDir() { // listing one file
				return f.addEntry(&out, name, d)
			}
			return nil
		}
		if d.IsDir() && d.Name() == ".git" {
			return fs.SkipDir
		}
		if in.Pattern == "" || matches(in.Pattern, d.Name()) {
			if len(out.Entries) == n {
				out.Truncated = true
				return errFull
			}
			if err := f.addEntry(&out, name, d); err != nil {
				return err
			}
		}
		if d.IsDir() && !in.Recursive {
			return fs.SkipDir
		}
		return nil
	})
	if err != nil && !errors.Is(err, errFull) {
		return listOut{}, err
	}
	return out, nil
}

func matches(glob, name string) bool {
	ok, _ := path.Match(glob, name) // checked by checkGlob
	return ok
}

func (f *files) addEntry(out *listOut, name string, d fs.DirEntry) error {
	e := entry{Path: name, Type: typeOf(d.Type())}
	if e.Type == "file" {
		info, err := d.Info()
		if err != nil {
			return err
		}
		e.Size = info.Size()
	}
	out.Entries = append(out.Entries, e)
	return nil
}

// readText reads a regular text file of at most maxSize bytes.
func (f *files) readText(p string) (string, fs.FileInfo, error) {
	info, err := fs.Stat(f.fsys, p)
	if err != nil {
		return "", nil, err
	}
	if info.IsDir() {
		return "", nil, fmt.Errorf("%s is a folder: use list_files", p)
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("%s is not a regular file", p)
	}
	if info.Size() > f.maxSize {
		return "", nil, fmt.Errorf("%s is %d bytes, over the %d-byte limit: use search_files to find what you need", p, info.Size(), f.maxSize)
	}
	data, err := readAtMost(f.fsys, p, f.maxSize)
	if err != nil {
		return "", nil, err
	}
	if int64(len(data)) > f.maxSize { // grew since the stat
		return "", nil, fmt.Errorf("%s is over the %d-byte limit", p, f.maxSize)
	}
	if binary(data) {
		return "", nil, fmt.Errorf("%s is a binary file", p)
	}
	return string(data), info, nil
}

func binary(data []byte) bool {
	return bytes.IndexByte(data[:min(len(data), binarySniffBytes)], 0) >= 0
}

func (f *files) read(_ context.Context, in readIn) (readOut, error) {
	p, err := clean(in.Path)
	if err != nil {
		return readOut{}, err
	}
	if in.Offset < 0 {
		return readOut{}, fmt.Errorf("offset %d is negative", in.Offset)
	}
	n, err := limit(in.Limit, defaultReadLines, maxReadLines)
	if err != nil {
		return readOut{}, err
	}
	text, _, err := f.readText(p)
	if err != nil {
		return readOut{}, err
	}
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" { // text ends with a newline, or is empty
		lines = lines[:len(lines)-1]
	}
	start := max(in.Offset, 1)
	if start > max(len(lines), 1) { // an empty file reads from line 1
		return readOut{}, fmt.Errorf("offset %d is past the end: %s has %d lines", start, p, len(lines))
	}
	end := min(start-1+n, len(lines))
	return readOut{
		Path:       p,
		Content:    strings.Join(lines[min(start-1, len(lines)):end], ""),
		StartLine:  start,
		EndLine:    end,
		TotalLines: len(lines),
		Truncated:  end < len(lines),
	}, nil
}

func (f *files) search(ctx context.Context, in searchIn) (searchOut, error) {
	p, err := clean(in.Path)
	if err != nil {
		return searchOut{}, err
	}
	n, err := limit(in.Limit, defaultSearchLimit, maxSearchLimit)
	if err != nil {
		return searchOut{}, err
	}
	if err := checkGlob(in.Glob); err != nil {
		return searchOut{}, err
	}
	expr := in.Pattern
	if in.IgnoreCase {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return searchOut{}, fmt.Errorf("bad pattern: %w", err)
	}
	out := searchOut{Matches: []match{}}
	errFull := errors.New("full")
	err = fs.WalkDir(f.fsys, p, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" && name != p {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || (in.Glob != "" && !matches(in.Glob, d.Name())) {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > f.maxSize {
			return nil // vanished or too big: skipped, like a binary file
		}
		data, err := readAtMost(f.fsys, name, f.maxSize)
		if err != nil || int64(len(data)) > f.maxSize || binary(data) {
			return nil
		}
		out.FilesSearched++
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(nil, len(data)+1)
		for line := 1; sc.Scan(); line++ {
			if !re.Match(sc.Bytes()) {
				continue
			}
			if len(out.Matches) == n {
				out.Truncated = true
				return errFull
			}
			out.Matches = append(out.Matches, match{Path: name, Line: line, Text: shorten(sc.Text())})
		}
		return nil
	})
	if err != nil && !errors.Is(err, errFull) {
		return searchOut{}, err
	}
	return out, nil
}

func shorten(s string) string {
	if utf8.RuneCountInString(s) <= maxMatchRunes {
		return s
	}
	return string([]rune(s)[:maxMatchRunes]) + "…"
}

func (f *files) edit(_ context.Context, in editIn) (editOut, error) {
	p, err := clean(in.Path)
	if err != nil {
		return editOut{}, err
	}
	if p == "." {
		return editOut{}, errors.New("path names the root folder, not a file")
	}
	if int64(len(in.NewText)) > f.maxSize {
		return editOut{}, fmt.Errorf("new_text is over the %d-byte limit", f.maxSize)
	}
	f.edits.Lock()
	defer f.edits.Unlock()

	native := filepath.FromSlash(p)
	info, err := f.root.Lstat(native)
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return editOut{}, err
	}
	if exists && info.Mode()&fs.ModeSymlink != 0 {
		return editOut{}, fmt.Errorf("%s is a symlink: edit the file it points to", p)
	}

	if in.OldText == "" {
		if exists && (info.IsDir() || info.Size() > 0) {
			return editOut{}, fmt.Errorf("%s already exists: give old_text to change it", p)
		}
		perm := fs.FileMode(0o644)
		if exists {
			perm = info.Mode().Perm()
		} else if dir := path.Dir(p); dir != "." {
			if err := f.root.MkdirAll(filepath.FromSlash(dir), 0o755); err != nil {
				return editOut{}, err
			}
		}
		if err := f.write(p, in.NewText, perm); err != nil {
			return editOut{}, err
		}
		return editOut{Path: p, Created: !exists, Replacements: 0}, nil
	}

	if !exists {
		return editOut{}, fmt.Errorf("%s does not exist: to create it, leave old_text empty", p)
	}
	if in.OldText == in.NewText {
		return editOut{}, errors.New("old_text and new_text are the same")
	}
	text, info, err := f.readText(p)
	if err != nil {
		return editOut{}, err
	}
	count := strings.Count(text, in.OldText)
	switch {
	case count == 0:
		return editOut{}, fmt.Errorf("old_text was not found in %s (it must match exactly, whitespace included)", p)
	case count > 1 && !in.ReplaceAll:
		return editOut{}, fmt.Errorf("old_text appears %d times in %s: include more of the surrounding text to make it unique, or set replace_all", count, p)
	}
	if !in.ReplaceAll {
		count = 1
	}
	updated := strings.Replace(text, in.OldText, in.NewText, count)
	if int64(len(updated)) > f.maxSize {
		return editOut{}, fmt.Errorf("the edited file would be over the %d-byte limit", f.maxSize)
	}
	if err := f.write(p, updated, info.Mode().Perm()); err != nil {
		return editOut{}, err
	}
	return editOut{Path: p, Replacements: count}, nil
}

// write replaces p's content through a temporary file in the same folder, so
// readers see the old file or the new one, never part of one.
func (f *files) write(p, content string, perm fs.FileMode) error {
	var suffix [8]byte
	_, _ = rand.Read(suffix[:])
	tmp := filepath.FromSlash(path.Join(path.Dir(p), "."+path.Base(p)+".tmp-"+hex.EncodeToString(suffix[:])))
	file, err := f.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	_, err = file.WriteString(content)
	if err == nil {
		err = file.Sync()
	}
	if cerr := file.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = f.root.Chmod(tmp, perm) // OpenFile's mode is narrowed by the umask
	}
	if err == nil {
		err = f.root.Rename(tmp, filepath.FromSlash(p))
	}
	if err != nil {
		_ = f.root.Remove(tmp)
		return err
	}
	return nil
}

// readAtMost reads the file name, but no more than limit+1 bytes: a file that
// grew past limit since it was checked reads as longer than limit, without
// reading all of it.
func readAtMost(fsys fs.FS, name string, limit int64) ([]byte, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit+1))
}
