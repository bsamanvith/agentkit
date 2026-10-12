package exa_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/bsamanvith/agentkit/connectors/exa"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// A toolset of type "exa" in config, built from a catalog. The key comes
// from the environment variable the settings name; cmd/agentkit passes
// os.LookupEnv. Here a fake Exa stands in for the real API.
func Example() {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results": [{"title": "Go", "url": "https://go.dev/",
			"highlights": ["Build simple, secure, scalable systems with Go."]}]}`))
	}))
	defer fake.Close()
	env := map[string]string{"EXA_API_KEY": "your-key"}

	catalog, err := toolset.NewCatalog(exa.Factory(
		exa.WithLookupEnv(func(k string) (string, bool) { v, ok := env[k]; return v, ok }),
		exa.WithBaseURL(fake.URL),
	))
	if err != nil {
		panic(err)
	}
	regs, _, err := toolset.Build(context.Background(), catalog, []toolset.Spec{
		{Name: "exa", Type: "exa", Settings: json.RawMessage(`{"api_key_env": "EXA_API_KEY"}`)},
	})
	if err != nil {
		panic(err)
	}
	out, err := regs["exa"].Invoke(context.Background(), "exa_search", json.RawMessage(`{"query": "the Go language", "max_results": 1}`))
	if err != nil {
		panic(err)
	}
	fmt.Println(string(out))
	// Output: {"results":[{"title":"Go","url":"https://go.dev/","content":"Build simple, secure, scalable systems with Go."}]}
}
