package memory

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Embedder turns texts into vectors whose closeness is closeness of meaning,
// for ModeSemantic and ModeHybrid. Package memory/embed has the clients of
// embedding APIs; memorytest.FakeEmbedder is a deterministic one for tests.
// Implementations must be safe for concurrent use.
type Embedder interface {
	// Embed returns one vector of Model's dimension for each text, in
	// order. purpose says whether the texts are queries or documents
	// (some providers embed them differently). It never returns some
	// vectors and an error.
	Embed(ctx context.Context, texts []string, purpose Purpose) ([][]float32, error)
	// Model names the model and gives its dimension, without a request. An
	// index records both with every vector it stores, and refuses to
	// compare vectors of another model or dimension (ErrEmbedderMismatch).
	Model() (name string, dim int)
}

// Purpose is what the texts of an [Embedder.Embed] call are for.
type Purpose string

// The purposes.
const (
	// PurposeQuery is the text of a search.
	PurposeQuery Purpose = "query"
	// PurposeDocument is the text of an item to store.
	PurposeDocument Purpose = "document"
)

// MaxEmbedderDim caps an embedder's dimension (providers' largest are 4096).
const MaxEmbedderDim = 8192

// ErrEmbedderMismatch is a vector of another model or dimension than the
// index's embedder gives: an index written with one embedder and searched
// with another, or an embedder whose vectors do not have the dimension its
// Model says. Vectors of different models are never compared; re-embed the
// index ([Reembedder]) after changing models.
var ErrEmbedderMismatch = errors.New("memory: vectors of another embedding model or dimension")

// Reembedder is an index that can embed its items again with its current
// embedder: every item, in every scope, whose vector is missing or was made
// by another model or dimension. Re-embedding is always this explicit call
// (`agentkit memory reembed`), never a side effect of opening or searching.
type Reembedder interface {
	// Reembed embeds the stale items and returns how many it updated. It
	// fails with ErrInvalid on an index with no embedder.
	Reembed(ctx context.Context) (int, error)
}

// HybridKeywordText is the query text of the keyword half of a hybrid
// search of text: text itself when it is a valid keyword query; otherwise
// (too many words and phrases) its first MaxQueryParts distinct words, quotes
// dropped; "" when it has no word at all, and the keyword half finds
// nothing. A long natural-language query (auto recall sends a message's
// text) so still gets keyword matches.
func HybridKeywordText(text string) string {
	if _, err := ParseKeywordQuery(text); err == nil {
		return text
	}
	return recallQuery(text, ModeKeyword)
}

// resolveModes checks and fills in the modes of an index with or without an
// embedder.
func resolveModes(modes []Mode, e Embedder) ([]Mode, error) {
	if e != nil {
		name, dim := e.Model()
		switch {
		case strings.TrimSpace(name) == "":
			return nil, errorf("embedder: empty model name")
		case dim < 1 || dim > MaxEmbedderDim:
			return nil, errorf("embedder %s: dimension %d: want 1 to %d", name, dim, MaxEmbedderDim)
		}
	}
	if len(modes) == 0 {
		if e == nil {
			return []Mode{ModeKeyword}, nil
		}
		return []Mode{ModeHybrid, ModeKeyword, ModeSemantic}, nil
	}
	out := make([]Mode, 0, len(modes))
	for _, m := range modes {
		switch m {
		case ModeKeyword:
		case ModeSemantic, ModeHybrid:
			if e == nil {
				return nil, errorf("mode %q needs an embedder", m)
			}
		default:
			return nil, errorf("mode %q: want %q, %q or %q", m, ModeKeyword, ModeSemantic, ModeHybrid)
		}
		if slices.Contains(out, m) {
			return nil, errorf("mode %q listed twice", m)
		}
		out = append(out, m)
	}
	return out, nil
}

// errorf is an ErrInvalid with a message.
func errorf(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, args...)...)
}
