package models_test

import (
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/models"
)

func TestParse(t *testing.T) {
	t.Parallel()
	got, err := models.Parse(" anthropic , openrouter/meta-llama/llama-3.3-70b-instruct:free,gemini/gemini-3.5-flash-lite,openai")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"anthropic/claude-haiku-5-5", "openrouter/meta-llama/llama-3.3-70b-instruct:free", "gemini/gemini-3.5-flash-lite", "openai/gpt-5-mini"}
	if len(got) != len(want) {
		t.Fatalf("Parse = %v, want %v", got, want)
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Errorf("model %d = %s, want %s", i, got[i], want[i])
		}
	}
	if got, err := models.Parse(""); err != nil || len(got) != 0 {
		t.Errorf("Parse(\"\") = %v, %v; want an empty lane", got, err)
	}
	for _, bad := range []string{"mistral/large", "openrouter", "anthropic,anthropic/claude-haiku-5-5"} {
		if _, err := models.Parse(bad); err == nil {
			t.Errorf("Parse(%q) = nil error", bad)
		}
	}
	if _, err := models.Parse("mistral"); !errors.Is(err, models.ErrUnknownProvider) {
		t.Errorf("unknown provider: err = %v", err)
	}
}

func TestBuildNeedsKey(t *testing.T) {
	t.Parallel()
	for _, p := range models.Providers() {
		id := p.DefaultID
		if id == "" {
			id = "some/model"
		}
		n := models.Name{Provider: p.Name, ID: id}
		if _, err := models.Build(n, func(string) string { return "" }); !errors.Is(err, models.ErrNoKey) {
			t.Errorf("%s without a key: err = %v, want ErrNoKey", n, err)
		}
		if p.Name == "anthropic" {
			continue // its SDK reads the process environment
		}
		if _, err := models.Build(n, func(k string) string { return map[bool]string{true: "k"}[k == p.Keys[len(p.Keys)-1]] }); err != nil {
			t.Errorf("%s with its last key variable set: %v", n, err)
		}
	}
}

// TestEvaluatedFile keeps evaluated.json readable, every model in it a
// valid name, sorted, once, with a run's details.
func TestEvaluatedFile(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("evaluated.json")
	if err != nil {
		t.Fatal(err)
	}
	var evals []models.Eval
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&evals); err != nil {
		t.Fatal(err)
	}
	day := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	for i, e := range evals {
		ns, err := models.Parse(e.Model)
		if err != nil || len(ns) != 1 || ns[0].String() != e.Model {
			t.Errorf("%q is not a full model name (%v)", e.Model, err)
		}
		if i > 0 && evals[i-1].Model >= e.Model {
			t.Errorf("%q: not sorted, or twice", e.Model)
		}
		if e.Scenarios < 1 || !day.MatchString(e.Date) {
			t.Errorf("%q: scenarios %d, date %q", e.Model, e.Scenarios, e.Date)
		}
		if !models.IsEvaluated(ns[0]) {
			t.Errorf("IsEvaluated(%s) = false", e.Model)
		}
	}
	if models.IsEvaluated(models.Name{Provider: "openai", ID: "never-run"}) {
		t.Error("IsEvaluated of an unknown model = true")
	}
}

func TestRecord(t *testing.T) {
	t.Parallel()
	evals := []models.Eval{{Model: "b/1", Scenarios: 1}, {Model: "d/1", Scenarios: 1}}
	got := models.Record(evals, models.Eval{Model: "c/1", Scenarios: 7})
	got = models.Record(got, models.Eval{Model: "b/1", Scenarios: 9})
	want := []models.Eval{{Model: "b/1", Scenarios: 9}, {Model: "c/1", Scenarios: 7}, {Model: "d/1", Scenarios: 1}}
	if len(got) != len(want) {
		t.Fatalf("Record = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Record[%d] = %v, want %v", i, got[i], want[i])
		}
	}
	if evals[0].Scenarios != 1 {
		t.Error("Record changed its input")
	}
}
