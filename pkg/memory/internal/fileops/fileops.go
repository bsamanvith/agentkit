// Package fileops is the one implementation of the memory commands' rules,
// over a backend's files in one scope ([Files]). Each backend runs a command
// as one transaction over its storage and calls the function here, so the
// rules (paths, directories, limits, edits) cannot drift between backends.
//
// Every function checks everything before it writes anything, so a command
// that fails changes nothing even on a backend with no transactions.
package fileops

import (
	"fmt"
	"slices"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/memory"
)

// File is a stored file's path and size.
type File struct {
	Path string
	Size int64
}

// Files are the files of one scope, as one transaction sees them. Paths are
// clean.
type Files interface {
	// Get returns a file's text; ok is false when there is no file at path.
	Get(path string) (text string, ok bool, err error)
	// Under returns the files strictly under dir, sorted by path.
	Under(dir string) ([]File, error)
	// Usage returns the bytes and the number of files of the scope.
	Usage() (bytes int64, files int, err error)
	// Put writes a file, replacing one there.
	Put(path, text string) error
	// Remove deletes a file.
	Remove(path string) error
}

// isDir reports whether a directory is at path.
func isDir(f Files, path string) (bool, error) {
	if path == memory.Root {
		return true, nil
	}
	under, err := f.Under(path)
	return len(under) > 0, err
}

// exists reports whether a file or directory is at path.
func exists(f Files, path string) (bool, error) {
	if _, ok, err := f.Get(path); ok || err != nil {
		return ok, err
	}
	return isDir(f, path)
}

// checkParents fails with ErrNotDir when a directory path would need is a file.
func checkParents(f Files, path string) error {
	for _, p := range memory.Parents(path) {
		if _, ok, err := f.Get(p); err != nil {
			return err
		} else if ok {
			return fmt.Errorf("%w: %s is a file", memory.ErrNotDir, p)
		}
	}
	return nil
}

// View is [memory.Store.View].
func View(f Files, path string) (memory.View, error) {
	if text, ok, err := f.Get(path); err != nil {
		return memory.View{}, err
	} else if ok {
		return memory.View{Path: path, Size: int64(len(text)), Text: text}, nil
	}
	under, err := f.Under(path)
	if err != nil {
		return memory.View{}, err
	}
	if len(under) == 0 && path != memory.Root {
		return memory.View{}, fmt.Errorf("%w: %s", memory.ErrNotFound, path)
	}
	v := memory.View{Path: path, Dir: true}
	dirs := map[string]int{} // index in v.Entries
	base := memory.Depth(path)
	for _, file := range under {
		v.Size += file.Size
		// Each directory between path and the file, one and two levels down.
		rel := strings.Split(strings.TrimPrefix(file.Path, path+"/"), "/")
		for i := 1; i < len(rel) && i <= 2; i++ {
			d := path + "/" + strings.Join(rel[:i], "/")
			if j, ok := dirs[d]; ok {
				v.Entries[j].Size += file.Size
				continue
			}
			dirs[d] = len(v.Entries)
			v.Entries = append(v.Entries, memory.Entry{Path: d, Dir: true, Size: file.Size})
		}
		if memory.Depth(file.Path)-base <= 2 {
			v.Entries = append(v.Entries, memory.Entry{Path: file.Path, Size: file.Size})
		}
	}
	slices.SortFunc(v.Entries, func(a, b memory.Entry) int { return strings.Compare(a.Path, b.Path) })
	return v, nil
}

// checkWrite checks limits for writing text to path, which holds a file of
// size old (had reports whether there is one).
func checkWrite(f Files, l memory.Limits, path, text string, old int64, had bool) error {
	n := int64(len(text))
	if n > l.MaxFileBytes {
		return &memory.LimitError{Limit: "file bytes", Max: l.MaxFileBytes, Want: n}
	}
	bytes, files, err := f.Usage()
	if err != nil {
		return err
	}
	if want := bytes - old + n; n > old && want > l.MaxScopeBytes {
		return &memory.LimitError{Limit: "scope bytes", Max: l.MaxScopeBytes, Want: want}
	}
	if !had && files+1 > l.MaxFiles {
		return &memory.LimitError{Limit: "files", Max: int64(l.MaxFiles), Want: int64(files + 1)}
	}
	return nil
}

// Create is [memory.Store.Create].
func Create(f Files, l memory.Limits, path, text string) error {
	old, had, err := f.Get(path)
	if err != nil {
		return err
	}
	if !had {
		if dir, err := isDir(f, path); err != nil {
			return err
		} else if dir {
			return fmt.Errorf("%w: %s", memory.ErrIsDir, path)
		}
		if err := checkParents(f, path); err != nil {
			return err
		}
	}
	if err := checkWrite(f, l, path, text, int64(len(old)), had); err != nil {
		return err
	}
	return f.Put(path, text)
}

// getFile returns the file at path, or ErrIsDir or ErrNotFound.
func getFile(f Files, path string) (string, error) {
	text, ok, err := f.Get(path)
	if err != nil || ok {
		return text, err
	}
	if dir, err := isDir(f, path); err != nil {
		return "", err
	} else if dir {
		return "", fmt.Errorf("%w: %s", memory.ErrIsDir, path)
	}
	return "", fmt.Errorf("%w: %s", memory.ErrNotFound, path)
}

// StrReplace is [memory.Store.StrReplace].
func StrReplace(f Files, l memory.Limits, path, old, new string) error {
	if old == "" {
		return fmt.Errorf("%w: old text is empty", memory.ErrInvalid)
	}
	text, err := getFile(f, path)
	if err != nil {
		return err
	}
	edited, err := ReplaceOnce(text, old, new)
	if err != nil {
		return err
	}
	if err := checkWrite(f, l, path, edited, int64(len(text)), true); err != nil {
		return err
	}
	return f.Put(path, edited)
}

// Insert is [memory.Store.Insert].
func Insert(f Files, l memory.Limits, path string, line int, insert string) error {
	text, err := getFile(f, path)
	if err != nil {
		return err
	}
	edited, err := InsertAt(text, line, insert)
	if err != nil {
		return err
	}
	if err := checkWrite(f, l, path, edited, int64(len(text)), true); err != nil {
		return err
	}
	return f.Put(path, edited)
}

// Delete is [memory.Store.Delete].
func Delete(f Files, path string) error {
	if path == memory.Root {
		return fmt.Errorf("%w: %s itself cannot be deleted", memory.ErrInvalidPath, memory.Root)
	}
	if _, ok, err := f.Get(path); err != nil {
		return err
	} else if ok {
		return f.Remove(path)
	}
	under, err := f.Under(path)
	if err != nil {
		return err
	}
	if len(under) == 0 {
		return fmt.Errorf("%w: %s", memory.ErrNotFound, path)
	}
	for _, file := range under {
		if err := f.Remove(file.Path); err != nil {
			return err
		}
	}
	return nil
}

// Rename is [memory.Store.Rename].
func Rename(f Files, from, to string) error {
	switch from {
	case memory.Root:
		return fmt.Errorf("%w: %s itself cannot be renamed", memory.ErrInvalidPath, memory.Root)
	case to:
		if ok, err := exists(f, from); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("%w: %s", memory.ErrNotFound, from)
		}
		return fmt.Errorf("%w: %s", memory.ErrExists, to)
	}
	text, isFile, err := f.Get(from)
	if err != nil {
		return err
	}
	var under []File
	if !isFile {
		if under, err = f.Under(from); err != nil {
			return err
		}
		if len(under) == 0 {
			return fmt.Errorf("%w: %s", memory.ErrNotFound, from)
		}
	}
	if ok, err := exists(f, to); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("%w: %s", memory.ErrExists, to)
	}
	if memory.Within(to, from) {
		return fmt.Errorf("%w: cannot move %s into itself", memory.ErrInvalidPath, from)
	}
	if err := checkParents(f, to); err != nil {
		return err
	}
	if isFile {
		if err := f.Put(to, text); err != nil {
			return err
		}
		return f.Remove(from)
	}
	moved := make([]string, len(under))
	for i, file := range under {
		moved[i] = to + strings.TrimPrefix(file.Path, from)
		if len(moved[i]) > memory.MaxPathBytes {
			return fmt.Errorf("%w: %s would be longer than %d bytes", memory.ErrInvalidPath, moved[i], memory.MaxPathBytes)
		}
	}
	for i, file := range under {
		text, _, err := f.Get(file.Path)
		if err != nil {
			return err
		}
		if err := f.Put(moved[i], text); err != nil {
			return err
		}
		if err := f.Remove(file.Path); err != nil {
			return err
		}
	}
	return nil
}
