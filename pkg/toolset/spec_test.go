package toolset_test

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// The config contract: this is the JSON form of the YAML in T026.
func configSpecs() []toolset.Spec {
	return []toolset.Spec{
		{Name: "salesforce", Type: "salesforce", Settings: json.RawMessage(`{"instance_url":"https://acme.my.salesforce.com"}`)},
		{Name: "salesforce-sandbox", Type: "salesforce", Settings: json.RawMessage(`{"instance_url":"https://acme--sbx.my.salesforce.com"}`)},
		{Name: "gmail-readonly", Type: "gmail", Include: []string{"gmail_search", "gmail_read*"}, Exclude: []string{"gmail_read_all"}, Optional: true},
	}
}

func TestSpecJSON(t *testing.T) {
	t.Parallel()
	specs := configSpecs()
	got, err := json.MarshalIndent(specs, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", "specs.golden.json")
	testkit.Golden(t, path, got)

	// And it decodes back to the same specs.
	var back []toolset.Spec
	if err := json.Unmarshal(got, &back); err != nil {
		t.Fatalf("decode golden: %v", err)
	}
	again, _ := json.Marshal(back)
	orig, _ := json.Marshal(specs)
	if !bytes.Equal(orig, again) {
		t.Errorf("round trip changed the specs:\n%s\n%s", orig, again)
	}

	t.Run("unknown field is an error", func(t *testing.T) {
		t.Parallel()
		var s toolset.Spec
		if err := json.Unmarshal([]byte(`{"name":"x","type":"demo","inclde":["echo"]}`), &s); err == nil {
			t.Errorf("misspelled field accepted: %+v", s)
		}
	})
}

func FuzzSpec(f *testing.F) {
	for _, s := range configSpecs() {
		b, _ := json.Marshal(s)
		f.Add(b)
	}
	f.Add([]byte(`{"name":"d","type":"demo","settings":{"echo_prefix":"x"},"include":["["]}`))
	f.Add([]byte(`{"name":"d","type":"demo","settings":null}`))
	f.Add([]byte(`[]`))

	cat, err := toolset.NewCatalog(toolset.Demo())
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var s toolset.Spec
		if err := json.Unmarshal(data, &s); err != nil {
			return
		}
		// Decoding is stable: re-encoding and decoding gives the same bytes.
		b1, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		var s2 toolset.Spec
		if err := json.Unmarshal(b1, &s2); err != nil {
			t.Fatalf("decode of own output %s: %v", b1, err)
		}
		if b2, _ := json.Marshal(s2); !bytes.Equal(b1, b2) {
			t.Fatalf("unstable round trip:\n%s\n%s", b1, b2)
		}
		// Whatever the spec holds, Build returns an error or registries, never panics.
		_, _, _ = toolset.Build(t.Context(), cat, []toolset.Spec{s})
	})
}
