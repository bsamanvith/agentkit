package handbook

import (
	"context"
	"crypto/sha256"
	"encoding/gob"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/memory"
)

// Vectors is an embedder that remembers the handbook's vectors in a file
// and paces its requests, so a free-tier key (Gemini's allows 100 texts a
// minute) can index the handbook once and every later start reads the file
// instead of asking again.
//
// Queries' vectors are kept too, so a repeated search costs no request.
// It is safe for concurrent use.
type Vectors struct {
	e         memory.Embedder
	path      string
	perMinute int
	clock     clock.Clock

	mu      sync.Mutex
	vecs    map[[32]byte][]float32
	unsaved int
	sent    []time.Time // texts sent in the last minute, one entry each
}

// VectorsOption configures [NewVectors].
type VectorsOption func(*Vectors)

// WithClock sets the clock that paces requests.
func WithClock(c clock.Clock) VectorsOption { return func(v *Vectors) { v.clock = c } }

// saveEvery is how many new vectors are kept in memory before the file is
// written again.
const saveEvery = 25

// NewVectors wraps e. path is the file of saved vectors ("" keeps them in
// memory only); perMinute caps the texts sent a minute (0 for no cap). A
// file of another model, or one that cannot be read, is ignored and
// replaced.
func NewVectors(e memory.Embedder, path string, perMinute int, opts ...VectorsOption) *Vectors {
	v := &Vectors{e: e, path: path, perMinute: perMinute, clock: clock.Real(), vecs: map[[32]byte][]float32{}}
	for _, o := range opts {
		o(v)
	}
	v.load()
	return v
}

type vectorFile struct {
	Model string
	Dim   int
	Vecs  map[[32]byte][]float32
}

func (v *Vectors) load() {
	if v.path == "" {
		return
	}
	f, err := os.Open(v.path)
	if err != nil {
		return
	}
	defer f.Close()
	var vf vectorFile
	model, dim := v.e.Model()
	if gob.NewDecoder(f).Decode(&vf) != nil || vf.Model != model || vf.Dim != dim {
		return
	}
	for k, vec := range vf.Vecs {
		if len(vec) == dim {
			v.vecs[k] = vec
		}
	}
}

// Model implements memory.Embedder.
func (v *Vectors) Model() (string, int) { return v.e.Model() }

// Len is the number of vectors held.
func (v *Vectors) Len() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.vecs)
}

func (v *Vectors) key(text string, purpose memory.Purpose) [32]byte {
	model, _ := v.e.Model()
	return sha256.Sum256([]byte(model + "\x00" + string(purpose) + "\x00" + text))
}

// Embed implements memory.Embedder.
func (v *Vectors) Embed(ctx context.Context, texts []string, purpose memory.Purpose) ([][]float32, error) {
	out := make([][]float32, len(texts))
	var need []int
	v.mu.Lock()
	for i, t := range texts {
		if vec, ok := v.vecs[v.key(t, purpose)]; ok {
			out[i] = vec
		} else {
			need = append(need, i)
		}
	}
	v.mu.Unlock()
	for len(need) > 0 {
		n := len(need)
		if v.perMinute > 0 {
			n = min(n, v.perMinute)
		}
		batch := need[:n]
		need = need[n:]
		if err := v.wait(ctx, n); err != nil {
			return nil, err
		}
		ask := make([]string, n)
		for j, i := range batch {
			ask[j] = texts[i]
		}
		vecs, err := v.e.Embed(ctx, ask, purpose)
		if err != nil {
			return nil, err
		}
		if len(vecs) != n {
			return nil, fmt.Errorf("handbook: %d vectors for %d texts", len(vecs), n)
		}
		v.mu.Lock()
		for j, i := range batch {
			out[i] = vecs[j]
			v.vecs[v.key(texts[i], purpose)] = vecs[j]
			v.unsaved++
		}
		save := v.unsaved >= saveEvery
		v.mu.Unlock()
		if save {
			if err := v.Save(); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// wait blocks until n more texts fit in the minute's allowance.
func (v *Vectors) wait(ctx context.Context, n int) error {
	if v.perMinute <= 0 {
		return nil
	}
	for {
		v.mu.Lock()
		now := v.clock.Now()
		keep := v.sent[:0]
		for _, t := range v.sent {
			if now.Sub(t) < time.Minute {
				keep = append(keep, t)
			}
		}
		v.sent = keep
		if len(v.sent)+n <= v.perMinute {
			for range n {
				v.sent = append(v.sent, now)
			}
			v.mu.Unlock()
			return nil
		}
		// The oldest sends that must age out before n more fit.
		until := v.sent[len(v.sent)+n-v.perMinute-1].Add(time.Minute)
		v.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-v.clock.After(until.Sub(now)):
		}
	}
}

// Save writes the vectors to the file, if it has one.
func (v *Vectors) Save() error {
	if v.path == "" {
		return nil
	}
	v.mu.Lock()
	model, dim := v.e.Model()
	vf := vectorFile{Model: model, Dim: dim, Vecs: make(map[[32]byte][]float32, len(v.vecs))}
	for k, vec := range v.vecs {
		vf.Vecs[k] = vec
	}
	v.unsaved = 0
	v.mu.Unlock()
	tmp, err := os.CreateTemp(filepath.Dir(v.path), filepath.Base(v.path)+".*")
	if err != nil {
		return fmt.Errorf("handbook: save vectors: %w", err)
	}
	err = gob.NewEncoder(tmp).Encode(vf)
	err = errors.Join(err, tmp.Close())
	if err == nil {
		err = os.Rename(tmp.Name(), v.path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("handbook: save vectors: %w", err)
	}
	return nil
}
