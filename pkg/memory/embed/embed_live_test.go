//go:build live

package embed_test

import (
	"math"
	"os"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/memory/embed"
)

// TestLiveParaphrase: real embeddings rank a paraphrase above an unrelated
// text, with the provider whose key is set (VOYAGE_API_KEY,
// OPENAI_API_KEY); skipped with none. Run with make test-live.
func TestLiveParaphrase(t *testing.T) {
	t.Parallel()
	var clients []*embed.Client
	if key := os.Getenv("VOYAGE_API_KEY"); key != "" {
		c, err := embed.NewVoyage(embed.Config{Model: "voyage-3.5-lite", Key: key})
		if err != nil {
			t.Fatal(err)
		}
		clients = append(clients, c)
	}
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		c, err := embed.NewOpenAI(embed.Config{Model: "text-embedding-3-small", Key: key})
		if err != nil {
			t.Fatal(err)
		}
		clients = append(clients, c)
	}
	if len(clients) == 0 {
		t.Skip("no embedding key: set VOYAGE_API_KEY or OPENAI_API_KEY")
	}
	for _, c := range clients {
		name, dim := c.Model()
		q, err := c.Embed(t.Context(), []string{"my flight preferences"}, memory.PurposeQuery)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		docs, err := c.Embed(t.Context(), []string{"I always take an aisle seat on planes.", "Peanuts give him a severe reaction."}, memory.PurposeDocument)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(q[0]) != dim {
			t.Errorf("%s: %d dimensions, Model says %d", name, len(q[0]), dim)
		}
		para, other := cosine(q[0], docs[0]), cosine(q[0], docs[1])
		t.Logf("%s: paraphrase %.3f, unrelated %.3f", name, para, other)
		if para <= other {
			t.Errorf("%s: the paraphrase (%.3f) does not outrank the unrelated text (%.3f)", name, para, other)
		}
	}
}

func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	return dot / math.Sqrt(na*nb)
}
