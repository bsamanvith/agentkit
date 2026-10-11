package flow

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// shapeVersion prefixes every fingerprint. Change it, and the encoding below,
// together: a changed encoding makes every stored fingerprint look like a
// changed tree.
const shapeVersion = "v1"

// Fingerprint returns a short, stable identifier of the structure of a step
// tree: the name and kind of every step, how they nest, the order of the
// children of a [Sequence] and the branches of a [Parallel], the keys of a
// [Route], and the maximum of a [Loop]. It never covers what a step does (the
// code of a [Func], a route key function, a loop condition), so two trees with
// the same shape but different code have the same fingerprint.
//
// The result depends only on the tree, never on pointers, map order or the
// process, so it is the same in every process and on every run. [Run] stores
// it in the [Checkpoint], and [Resume] compares it with the tree it is given.
//
// A step that is not one of this package's combinators (an agent step, say) is
// a leaf identified by its name. Fingerprint returns an error matching
// [ErrInvalidFlow] for a tree [Validate] rejects.
func Fingerprint(step Step) (string, error) {
	if err := Validate(step); err != nil {
		return "", err
	}
	return shapeOf(step), nil
}

// shapeOf fingerprints a tree that has been validated.
func shapeOf(step Step) string {
	var b strings.Builder
	encodeShape(&b, step, nameOf(step))
	sum := sha256.Sum256([]byte(b.String()))
	return shapeVersion + ":" + hex.EncodeToString(sum[:])
}

// lp writes s as its length in bytes, a colon and its bytes. Unlike quoting, it
// does not depend on which characters the toolchain's Unicode tables call
// printable, so the same tree has the same text under every Go release, and
// since the length says where the string ends, no choice of names or route keys
// makes two different trees write the same text.
func lp(s string) string { return strconv.Itoa(len(s)) + ":" + s }

// encodeShape writes a canonical text of step, whose name in its parent is
// name.
func encodeShape(b *strings.Builder, step Step, name string) {
	switch s := step.(type) {
	case *named:
		// The path of a Named step uses the name it was given, whatever the
		// step inside is called.
		encodeShape(b, s.step, name)
	case *sequence:
		b.WriteString("seq(" + lp(name))
		for _, c := range s.steps {
			b.WriteByte(',')
			encodeShape(b, c, nameOf(c))
		}
		b.WriteByte(')')
	case *parallel:
		b.WriteString("par(" + lp(name))
		for _, c := range s.branches {
			b.WriteByte(',')
			encodeShape(b, c, nameOf(c))
		}
		b.WriteByte(')')
	case *route:
		b.WriteString("route(" + lp(name))
		keys := make([]string, 0, len(s.routes))
		for k := range s.routes {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString("," + lp(k) + "=")
			encodeShape(b, s.routes[k], nameOf(s.routes[k]))
		}
		b.WriteByte(')')
	case *loop:
		b.WriteString("loop(" + lp(name) + "," + strconv.Itoa(s.max) + ",")
		encodeShape(b, s.body, nameOf(s.body))
		b.WriteByte(')')
	case *approve:
		b.WriteString("approve(" + lp(name) + ")")
	default:
		b.WriteString("step(" + lp(name) + ")")
	}
}

// ShapeChangedError reports that the step tree given to [Resume] is not the one
// that produced the checkpoint. It matches [ErrShapeChanged].
type ShapeChangedError struct {
	RunID string
	// Stored is the fingerprint saved in the run's checkpoint.
	Stored string
	// Current is the fingerprint of the step tree given to Resume.
	Current string
}

func (e *ShapeChangedError) Error() string {
	return fmt.Sprintf("%v: run %q was checkpointed with step tree %s, this tree is %s (WithAllowShapeChange resumes anyway)",
		ErrShapeChanged, e.RunID, e.Stored, e.Current)
}

func (e *ShapeChangedError) Unwrap() error { return ErrShapeChanged }
