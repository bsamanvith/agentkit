package embed_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/memory/embed"
	"github.com/bsamanvith/agentkit/pkg/memory/embed/embedtest"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest"
)

func options(env embedtest.Env) []embed.Option {
	return []embed.Option{embed.WithHTTPClient(env.HTTPClient), embed.WithClock(env.Clock), embed.WithLogger(env.Logger)}
}

func openAICase() embedtest.Case {
	return embedtest.Case{
		New: func(env embedtest.Env) (memory.Embedder, error) {
			return embed.NewOpenAI(embed.Config{
				Model: "text-embedding-3-small", Dimensions: 8, BaseURL: env.BaseURL + "/v1", Key: env.Key,
				BatchSize: env.BatchSize, MaxBodyBytes: env.MaxBodyBytes,
			}, options(env)...)
		},
		Model: "text-embedding-3-small", Dim: 8,
		Cassettes: "testdata/cassettes/openai",
		Calls: []embedtest.Call{
			{Name: "documents", Texts: []string{"alpha", "beta", "gamma"}, Purpose: memory.PurposeDocument, BatchSize: 2, Cassettes: []string{"batch-1", "batch-2"}},
			{Name: "query", Texts: []string{"what is alpha"}, Purpose: memory.PurposeQuery, Cassettes: []string{"query"}},
		},
		ErrorBody: func(msg string) []byte {
			b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": msg, "type": "invalid_request_error"}})
			return b
		},
	}
}

func voyageCase() embedtest.Case {
	return embedtest.Case{
		New: func(env embedtest.Env) (memory.Embedder, error) {
			return embed.NewVoyage(embed.Config{
				Model: "voyage-3.5-lite", Dimensions: 256, BaseURL: env.BaseURL + "/v1", Key: env.Key,
				BatchSize: env.BatchSize, MaxBodyBytes: env.MaxBodyBytes,
			}, options(env)...)
		},
		Model: "voyage-3.5-lite", Dim: 256,
		Cassettes: "testdata/cassettes/voyage",
		Calls: []embedtest.Call{
			{Name: "documents", Texts: []string{"alpha", "beta", "gamma"}, Purpose: memory.PurposeDocument, BatchSize: 2, Cassettes: []string{"documents-1", "documents-2"}},
			{Name: "query", Texts: []string{"what is alpha"}, Purpose: memory.PurposeQuery, Cassettes: []string{"query"}},
		},
		ErrorBody: func(msg string) []byte {
			b, _ := json.Marshal(map[string]any{"detail": msg})
			return b
		},
	}
}

// TestOpenAIConformance and TestVoyageConformance: both clients pass the
// shared check on their (synthetic) cassettes.
func TestOpenAIConformance(t *testing.T) { t.Parallel(); embedtest.Run(t, openAICase()) }
func TestVoyageConformance(t *testing.T) { t.Parallel(); embedtest.Run(t, voyageCase()) }

// cassetteVectors reads the vectors of a cassette's response, by index.
func cassetteVectors(t *testing.T, path string) map[int][]float32 {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(raw)), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	out := map[int][]float32{}
	for _, d := range body.Data {
		out[d.Index] = d.Embedding
	}
	return out
}

// TestVectorsInInputOrder: a response listing its vectors out of order (as
// batch-1's does) is put back in input order by index.
func TestVectorsInInputOrder(t *testing.T) {
	t.Parallel()
	up := toolsettest.NewUpstream(t, "testdata/cassettes/openai")
	c, err := embed.NewOpenAI(embed.Config{Model: "text-embedding-3-small", Dimensions: 8, BaseURL: up.URL() + "/v1", Key: "k"},
		embed.WithHTTPClient(up.Client()))
	if err != nil {
		t.Fatal(err)
	}
	vs, err := c.Embed(t.Context(), []string{"alpha", "beta"}, memory.PurposeDocument)
	if err != nil {
		t.Fatal(err)
	}
	want := cassetteVectors(t, "testdata/cassettes/openai/batch-1.res.http")
	if !slices.Equal(vs[0], want[0]) || !slices.Equal(vs[1], want[1]) {
		t.Errorf("vectors out of input order: %v", vs)
	}
}

// TestBatchBytes: MaxBatchBytes splits a batch; a text over it goes alone.
func TestBatchBytes(t *testing.T) {
	t.Parallel()
	up := toolsettest.NewUpstream(t, "")
	c, err := embed.NewOpenAI(embed.Config{Model: "local-model", Dimensions: 2, BaseURL: up.URL(), MaxBatchBytes: 10},
		embed.WithHTTPClient(up.Client()))
	if err != nil {
		t.Fatal(err)
	}
	answer := func(n int) toolsettest.Response {
		var data []string
		for i := range n {
			data = append(data, `{"index": `+string(rune('0'+i))+`, "embedding": [1, 0]}`)
		}
		return toolsettest.Response{Body: []byte(`{"data": [` + strings.Join(data, ",") + `]}`)}
	}
	up.Script(answer(2), answer(1), answer(1))
	texts := []string{"aaaa", "bbbb", "cccc", strings.Repeat("d", 40)}
	vs, err := c.Embed(t.Context(), texts, memory.PurposeDocument)
	if err != nil || len(vs) != 4 {
		t.Fatalf("Embed = %d vectors, %v", len(vs), err)
	}
	var inputs [][]string
	for _, r := range up.Requests() {
		var body struct {
			Input      []string `json:"input"`
			Dimensions *int     `json:"dimensions"`
		}
		if err := json.Unmarshal(r.Body, &body); err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, body.Input)
		if body.Dimensions != nil {
			t.Error("dimensions sent for a model whose default is unknown")
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("Authorization sent with no key")
		}
	}
	if want := [][]string{{"aaaa", "bbbb"}, {"cccc"}, {texts[3]}}; !slices.EqualFunc(inputs, want, slices.Equal) {
		t.Errorf("batches %v, want %v", inputs, want)
	}
}

// TestDimensionsSent: the dimension goes to the provider only for a known
// model, when it is not the model's default.
func TestDimensionsSent(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		model string
		dims  int
		sent  bool
		dim   int
	}{
		{"text-embedding-3-small", 0, false, 1536},
		{"text-embedding-3-small", 1536, false, 1536},
		{"text-embedding-3-small", 256, true, 256},
	} {
		up := toolsettest.NewUpstream(t, "")
		x, err := embed.NewOpenAI(embed.Config{Model: c.model, Dimensions: c.dims, BaseURL: up.URL(), Key: "k"}, embed.WithHTTPClient(up.Client()))
		if err != nil {
			t.Fatal(err)
		}
		if _, dim := x.Model(); dim != c.dim {
			t.Errorf("%s %d: dimension %d, want %d", c.model, c.dims, dim, c.dim)
		}
		up.Script(toolsettest.Response{Status: 500})
		_, _ = x.Embed(t.Context(), []string{"x"}, memory.PurposeQuery)
		sent := bytes.Contains(up.Requests()[0].Body, []byte(`"dimensions"`))
		if sent != c.sent {
			t.Errorf("%s %d: dimensions sent %v, want %v", c.model, c.dims, sent, c.sent)
		}
	}
}

// FuzzResponse: whatever the provider answers, Embed returns an error or
// one vector of the configured dimension per text, and never panics.
func FuzzResponse(f *testing.F) {
	f.Add([]byte(`{"data": [{"index": 0, "embedding": [1, 2]}, {"index": 1, "embedding": [3, 4]}]}`))
	f.Add([]byte(`{"data": [{"index": 1, "embedding": [1, 2]}, {"embedding": [3, 4]}]}`))
	f.Add([]byte(`{"data": [{"index": -1, "embedding": [1e999]}]}`))
	f.Add([]byte(`{"data": null}`))
	f.Add([]byte(`[]`))
	var body atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body.Load().([]byte))
	}))
	f.Cleanup(srv.Close)
	c, err := embed.NewOpenAI(embed.Config{Model: "m", Dimensions: 2, BaseURL: srv.URL})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		body.Store(b)
		vs, err := c.Embed(t.Context(), []string{"a", "b"}, memory.PurposeQuery)
		if err != nil {
			return
		}
		if len(vs) != 2 || len(vs[0]) != 2 || len(vs[1]) != 2 {
			t.Errorf("Embed of %q = %v", b, vs)
		}
	})
}

func TestConfigErrors(t *testing.T) {
	t.Parallel()
	for name, f := range map[string]func() error{
		"no model":      func() error { _, err := embed.NewOpenAI(embed.Config{Dimensions: 4}); return err },
		"unknown dims":  func() error { _, err := embed.NewOpenAI(embed.Config{Model: "mystery"}); return err },
		"voyage no key": func() error { _, err := embed.NewVoyage(embed.Config{Model: "voyage-3.5"}); return err },
		"plain http": func() error {
			_, err := embed.NewOpenAI(embed.Config{Model: "m", Dimensions: 4, BaseURL: "http://example.com"})
			return err
		},
		"user in url": func() error {
			_, err := embed.NewOpenAI(embed.Config{Model: "m", Dimensions: 4, BaseURL: "https://u:p@example.com"})
			return err
		},
		"batch too large": func() error {
			_, err := embed.NewVoyage(embed.Config{Model: "voyage-3.5", Key: "k", BatchSize: 1001})
			return err
		},
		"negative dims": func() error { _, err := embed.NewOpenAI(embed.Config{Model: "m", Dimensions: -1}); return err },
	} {
		if err := f(); !errors.Is(err, memory.ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	if _, err := embed.NewOpenAI(embed.Config{Model: "m", Dimensions: 4, BaseURL: "http://127.0.0.1:8080/v1"}); err != nil {
		t.Errorf("http to loopback: %v", err)
	}
}

// TestErrorClasses: the provider's message reaches a 400's error, cut and
// with the key redacted; each status has its class.
func TestErrorClasses(t *testing.T) {
	t.Parallel()
	up := toolsettest.NewUpstream(t, "")
	c, err := embed.NewVoyage(embed.Config{Model: "voyage-3.5", Key: "sekrit", BaseURL: up.URL()}, embed.WithHTTPClient(up.Client()))
	if err != nil {
		t.Fatal(err)
	}
	for status, want := range map[int]error{400: embed.ErrRejected, 401: embed.ErrUnauthorized, 403: embed.ErrUnauthorized, 503: embed.ErrUnavailable} {
		up.Script(toolsettest.Response{Status: status, Body: []byte(`{"detail": "input too long for sekrit ` + strings.Repeat("x", 400) + `"}`)})
		_, err := c.Embed(t.Context(), []string{"x"}, memory.PurposeQuery)
		if !errors.Is(err, want) {
			t.Errorf("status %d: %v, want %v", status, err, want)
		}
		if status == 400 && (!strings.Contains(err.Error(), "input too long for [redacted]") || len(err.Error()) > 400) {
			t.Errorf("400's message: %v", err)
		}
	}
}
