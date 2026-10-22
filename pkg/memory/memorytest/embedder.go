package memorytest

import (
	"context"
	"hash/fnv"
	"slices"
	"sync"

	"github.com/bsamanvith/agentkit/pkg/memory"
)

// FakeEmbedder is a deterministic memory.Embedder for tests, built so that
// paraphrases are close and unrelated texts far: each word of a text (by
// memory.Tokenize, stop words dropped) counts once more in one dimension
// (with a sign), chosen by hashing the word, or its concept when
// FakeConcepts lists it, so "flight" and "aisle seat" share the dimension of
// air travel. Vectors are not normalized (their length grows with the text),
// so an index that forgets to normalize ranks long texts first. It records
// every call, and fails every call once SetErr is given an error.
type FakeEmbedder struct {
	name string
	dim  int

	mu    sync.Mutex
	err   error
	calls []EmbedCall
}

// EmbedCall is one call a FakeEmbedder received.
type EmbedCall struct {
	Texts   []string
	Purpose memory.Purpose
}

// NewFakeEmbedder returns a FakeEmbedder whose Model is name and dim.
// DefaultFakeModel and DefaultFakeDim are what the suite uses.
func NewFakeEmbedder(name string, dim int) *FakeEmbedder {
	return &FakeEmbedder{name: name, dim: dim}
}

// The suite's fake model.
const (
	DefaultFakeModel = "fake-embed-1"
	DefaultFakeDim   = 256
)

// Model implements memory.Embedder.
func (e *FakeEmbedder) Model() (string, int) { return e.name, e.dim }

// Embed implements memory.Embedder.
func (e *FakeEmbedder) Embed(ctx context.Context, texts []string, p memory.Purpose) ([][]float32, error) {
	e.mu.Lock()
	e.calls = append(e.calls, EmbedCall{Texts: slices.Clone(texts), Purpose: p})
	err := e.err
	e.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = FakeVector(t, e.dim)
	}
	return out, nil
}

// SetErr makes every later call fail with err (nil: succeed again).
func (e *FakeEmbedder) SetErr(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.err = err
}

// Calls returns the calls so far, oldest first.
func (e *FakeEmbedder) Calls() []EmbedCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.calls)
}

// FakeVector is FakeEmbedder's vector of text with dim dimensions.
func FakeVector(text string, dim int) []float32 {
	v := make([]float32, dim)
	for _, w := range memory.Tokenize(text) {
		if fakeStopWords[w] {
			continue
		}
		feature := "w:" + w
		if c, ok := FakeConcepts[w]; ok {
			feature = "c:" + c
		}
		h := fnv.New64a()
		_, _ = h.Write([]byte(feature))
		sum := h.Sum64()
		sign := float32(1)
		if sum>>63 == 1 {
			sign = -1
		}
		v[sum%uint64(dim)] += sign
	}
	return v
}

// FakeConcepts maps words to the concept FakeEmbedder embeds them as: the
// paraphrases of SemanticRelevance and a few more.
var FakeConcepts = concepts(map[string][]string{
	"air travel": {"flight", "flights", "fly", "flying", "plane", "planes", "aisle", "seat", "airline", "airport"},
	"preference": {"preference", "preferences", "prefer", "prefers", "always", "usually", "favourite", "favorite"},
	"diet":       {"diet", "vegetarian", "vegan", "meat", "fish", "eat", "eats"},
	"allergy":    {"allergy", "allergies", "allergic", "nut", "nuts", "peanut", "peanuts", "reaction"},
	"meeting":    {"meeting", "meetings", "standup", "sync", "daily", "morning"},
	"clock time": {"time", "nine", "thirty", "starts", "o'clock"},
	"computer":   {"computer", "laptop", "notebook", "thinkpad", "macbook"},
	"salary":     {"salary", "pay", "payday", "wages", "paycheck"},
	"dog":        {"dog", "dogs", "puppy", "retriever", "pet", "breed"},
	"car":        {"car", "cars", "automobile", "vehicle", "hatchback", "sedan"},
	"cancel":     {"cancel", "cancelling", "end", "stop", "quit", "terminate"},
	"membership": {"membership", "subscription", "renewal"},
	"weather":    {"weather", "rain", "rained", "raining", "sunny", "forecast"},
	"portugal":   {"portugal", "lisbon", "porto"},
	"garden":     {"garden", "gardening", "grow", "plants", "tomatoes", "basil", "vegetable", "vegetables"},
})

func concepts(m map[string][]string) map[string]string {
	out := map[string]string{}
	for c, words := range m {
		for _, w := range words {
			out[w] = c
		}
	}
	return out
}

var fakeStopWords = func() map[string]bool {
	out := map[string]bool{}
	for _, w := range []string{
		"a", "an", "the", "and", "or", "of", "to", "in", "on", "at", "for", "with", "is", "are", "was",
		"were", "be", "it", "its", "i", "my", "me", "he", "she", "him", "her", "his", "their", "they",
		"we", "our", "you", "your", "this", "that", "which", "what", "when", "how", "do", "does", "after",
		"before", "all", "every", "two",
	} {
		out[w] = true
	}
	return out
}()

// FakeParaphrases are pairs of texts FakeEmbedder puts close (cosine over
// 0.5); FakeUnrelated pairs it puts far (under 0.3).
var (
	FakeParaphrases = [][2]string{
		{"my flight preferences", "I always take an aisle seat on planes."},
		{"nut allergy", "Peanuts give him a severe reaction."},
		{"pet dog breed", "Their puppy is a golden retriever named Biscuit."},
		{"portugal weather", "It rained all week in Lisbon."},
	}
	FakeUnrelated = [][2]string{
		{"my flight preferences", "Peanuts give him a severe reaction."},
		{"pet dog breed", "It rained all week in Lisbon."},
		{"salary", "Tomatoes and basil grow on the balcony."},
	}
)
