package knowledge

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/memory/inmem"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// A Document is one file of a knowledge base.
type Document struct {
	// Source is its path in the base: slash-separated, relative to the
	// folder it was loaded from ("returns/refunds.md").
	Source string
	// Title is its first level-one heading, else its file name without
	// the extension.
	Title string
	// Text is its content: UTF-8.
	Text string
}

// The limits of a base.
const (
	// DefaultChunkBytes is the default of Config.ChunkBytes; MinChunkBytes
	// and MaxChunkBytes bound it.
	DefaultChunkBytes = 2000
	MinChunkBytes     = 200
	MaxChunkBytes     = 16 << 10
	// MaxDocumentBytes caps one document; MaxDocuments how many a base
	// holds, MaxPassages how many passages.
	MaxDocumentBytes = 1 << 20
	MaxDocuments     = 5000
	MaxPassages      = 50_000
)

// Extensions are the file extensions [Load] reads, compared without case.
var Extensions = []string{".md", ".markdown", ".txt"}

// Errors of a base, checked with errors.Is.
var (
	// ErrInvalid is a document or a config that breaks a rule of this
	// package (a size, a duplicate source, text that is not UTF-8).
	ErrInvalid = errors.New("knowledge: invalid")
	// ErrEmpty is a base with no passage to search.
	ErrEmpty = errors.New("knowledge: the base has no documents")
)

// Load reads the documents under fsys's root: every regular file, or link
// to one, whose extension is one of [Extensions], in lexical order of their
// paths. Names
// that start with "." (files and folders) are skipped, as are empty files.
// A file over MaxDocumentBytes, or not UTF-8, is ErrInvalid, naming it.
func Load(fsys fs.FS) ([]Document, error) {
	var docs []Document
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != "." && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !slices.Contains(Extensions, strings.ToLower(path.Ext(p))) {
			return nil
		}
		var info fs.FileInfo
		switch {
		case d.Type().IsRegular():
			info, err = d.Info()
		case d.Type()&fs.ModeSymlink != 0:
			// A Kubernetes ConfigMap volume holds each file as a link
			// into its "..data" folder. Stat follows the link; under
			// os.Root it fails for a link that leaves the root, and such
			// a link, or a broken one, is skipped.
			if info, err = fs.Stat(fsys, p); err != nil {
				return nil
			}
		default:
			return nil
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if info.Size() > MaxDocumentBytes {
			return fmt.Errorf("%w: %s: %d bytes, at most %d", ErrInvalid, p, info.Size(), MaxDocumentBytes)
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		if len(b) > MaxDocumentBytes {
			return fmt.Errorf("%w: %s: %d bytes, at most %d", ErrInvalid, p, len(b), MaxDocumentBytes)
		}
		if strings.TrimSpace(string(b)) == "" {
			return nil
		}
		if !utf8.Valid(b) {
			return fmt.Errorf("%w: %s: not UTF-8 text", ErrInvalid, p)
		}
		if len(docs) == MaxDocuments {
			return fmt.Errorf("%w: more than %d documents", ErrInvalid, MaxDocuments)
		}
		docs = append(docs, Document{Source: p, Title: titleOf(p, string(b)), Text: string(b)})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("knowledge: load: %w", err)
	}
	return docs, nil
}

// titleOf is a document's first level-one heading (outside code), else its
// file name without the extension.
func titleOf(source, text string) string {
	fence := ""
	for line := range strings.Lines(text) {
		if level, title, ok := heading(line); ok && fence == "" {
			if level == 1 && title != "" {
				return title
			}
			continue
		}
		fence = nextFence(fence, line)
	}
	name := path.Base(source)
	return strings.TrimSuffix(name, path.Ext(name))
}

// Config configures a [Base].
type Config struct {
	// ChunkBytes caps a passage's text (default DefaultChunkBytes, from
	// MinChunkBytes to MaxChunkBytes).
	ChunkBytes int
	// Embedder makes the vectors of semantic and hybrid search; nil to
	// search by keyword only. New embeds every passage.
	Embedder memory.Embedder
	// Modes are the search modes, the default first, as
	// memory.IndexConfig.Modes: [keyword] without an Embedder and [hybrid,
	// keyword, semantic] with one by default.
	Modes []memory.Mode
}

// A Base is a knowledge base: documents cut into passages and searched as a
// memory.Index, in this process. It is read-only once made, and safe for
// concurrent use. It is the agent's own reference material, like its system
// prompt: every caller searches the same passages, whatever the tenant or
// user of its context.
type Base struct {
	docs     []Document // by Source
	passages []Passage  // the index's item "i" is passages[i]
	index    memory.Index
	tenant   tenancy.Tenant // the index's scope
}

// scopeTenant is the tenant the base's index keeps its items under.
const scopeTenant = "knowledge"

// embedWorkers is how many passages New embeds at once.
const embedWorkers = 8

// New cuts docs into passages and indexes them. Sources must be unique and
// non-empty. A base with no passage is ErrEmpty; an embedder's failure fails
// New.
func New(ctx context.Context, docs []Document, c Config) (*Base, error) {
	switch {
	case c.ChunkBytes == 0:
		c.ChunkBytes = DefaultChunkBytes
	case c.ChunkBytes < MinChunkBytes || c.ChunkBytes > MaxChunkBytes:
		return nil, fmt.Errorf("%w: chunk bytes %d: want %d to %d", ErrInvalid, c.ChunkBytes, MinChunkBytes, MaxChunkBytes)
	}
	if len(docs) > MaxDocuments {
		return nil, fmt.Errorf("%w: %d documents, at most %d", ErrInvalid, len(docs), MaxDocuments)
	}
	b := &Base{docs: slices.Clone(docs)}
	slices.SortFunc(b.docs, func(x, y Document) int { return strings.Compare(x.Source, y.Source) })
	for i, d := range b.docs {
		switch {
		case d.Source == "":
			return nil, fmt.Errorf("%w: a document has no source", ErrInvalid)
		case i > 0 && b.docs[i-1].Source == d.Source:
			return nil, fmt.Errorf("%w: two documents are %q", ErrInvalid, d.Source)
		case len(d.Text) > MaxDocumentBytes:
			return nil, fmt.Errorf("%w: %s: %d bytes, at most %d", ErrInvalid, d.Source, len(d.Text), MaxDocumentBytes)
		case !utf8.ValidString(d.Text) || !utf8.ValidString(d.Source) || !utf8.ValidString(d.Title):
			return nil, fmt.Errorf("%w: %s: not UTF-8", ErrInvalid, d.Source)
		}
		b.passages = append(b.passages, Split(d, c.ChunkBytes)...)
		if len(b.passages) > MaxPassages {
			return nil, fmt.Errorf("%w: more than %d passages", ErrInvalid, MaxPassages)
		}
	}
	if len(b.passages) == 0 {
		return nil, ErrEmpty
	}
	index, err := inmem.NewIndex(memory.IndexConfig{
		Embedder: c.Embedder, Modes: c.Modes, MaxItems: len(b.passages),
		MaxTextBytes: MaxChunkBytes + 2*memory.MaxMetadataValueBytes + 2,
	})
	if err != nil {
		return nil, fmt.Errorf("knowledge: %w", err)
	}
	b.index = index
	if b.tenant, err = tenancy.NewTenant(scopeTenant); err != nil {
		return nil, err
	}
	if err := b.put(ctx); err != nil {
		return nil, fmt.Errorf("knowledge: index: %w", err)
	}
	return b, nil
}

// indexText is what the index searches for a passage: its title and
// section, which name what it is about, then its text.
func indexText(p Passage) string {
	head := p.Title
	switch {
	case p.Section == "":
	case p.Section == p.Title || strings.HasPrefix(p.Section, p.Title+" > "):
		head = p.Section
	default:
		head = p.Title + " > " + p.Section
	}
	return cutBytes(head, 2*memory.MaxMetadataValueBytes) + "\n" + p.Text
}

func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// put indexes every passage, embedWorkers at a time.
func (b *Base) put(ctx context.Context) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	ctx = tenancy.With(ctx, b.tenant)
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range embedWorkers {
		wg.Go(func() {
			for i := range jobs {
				it := memory.Item{ID: strconv.Itoa(i), Text: indexText(b.passages[i])}
				if err := b.index.Put(ctx, it); err != nil {
					cancel(err)
				}
			}
		})
	}
send:
	for i := range b.passages {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break send
		}
	}
	close(jobs)
	wg.Wait()
	return context.Cause(ctx)
}

// A Hit is a passage a search found, and its score (higher is better;
// scores compare only within one search).
type Hit struct {
	Passage
	Score float64
}

// Search returns at most k passages (0: memory.DefaultK; at most
// memory.MaxK) that match query, best first, in the base's default mode.
// A bad query or k is an error matching memory.ErrInvalid.
func (b *Base) Search(ctx context.Context, query string, k int) ([]Hit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// The caller's deadline and cancellation, the base's own scope.
	res, err := b.index.Search(tenancy.With(ctx, b.tenant), memory.Query{Text: query, K: k})
	if err != nil {
		return nil, err
	}
	out := make([]Hit, 0, len(res))
	for _, r := range res {
		i, err := strconv.Atoi(r.Item.ID)
		if err != nil || i < 0 || i >= len(b.passages) {
			return nil, fmt.Errorf("knowledge: the index returned an unknown item %q", r.Item.ID)
		}
		out = append(out, Hit{Passage: b.passages[i], Score: r.Score})
	}
	return out, nil
}

// Modes are the base's search modes, its default first.
func (b *Base) Modes() []memory.Mode { return b.index.Modes() }

// Documents returns the base's documents, by source.
func (b *Base) Documents() []Document { return slices.Clone(b.docs) }

// Document returns the document with the source.
func (b *Base) Document(source string) (Document, bool) {
	i, ok := slices.BinarySearchFunc(b.docs, source, func(d Document, s string) int { return strings.Compare(d.Source, s) })
	if !ok {
		return Document{}, false
	}
	return b.docs[i], true
}

// Passages returns how many passages the base holds.
func (b *Base) Passages() int { return len(b.passages) }
