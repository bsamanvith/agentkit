package tavily_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"

	"github.com/bsamanvith/agentkit/connectors/tavily"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

func ExampleFactory() {
	// A stand-in for api.tavily.com; leave WithBaseURL out to use the real one.
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Println(r.URL.Path, "keyless:", r.Header.Get("X-Tavily-Access-Mode"))
		_, _ = w.Write([]byte(`{"results": [{"title": "Go 1.27 Release Notes", "url": "https://go.dev/doc/go1.27",
			"content": "The latest Go release, version 1.27, arrives six months after Go 1.26.", "score": 0.9}]}`))
	}))
	defer fake.Close()

	catalog, err := toolset.NewCatalog(tavily.Factory(
		tavily.WithBaseURL(fake.URL),
		tavily.WithLookupEnv(os.LookupEnv), // auto: the key if TAVILY_API_KEY is set, else keyless
	))
	if err != nil {
		panic(err)
	}
	specs := []toolset.Spec{{Name: "search", Type: tavily.Type, Settings: json.RawMessage(`{"auth": "keyless"}`)}}
	regs, _, err := toolset.Build(context.Background(), catalog, specs)
	if err != nil {
		panic(err)
	}

	out, err := regs["search"].Invoke(context.Background(), "tavily_search", json.RawMessage(`{"query": "latest Go release"}`))
	if err != nil {
		panic(err)
	}
	fmt.Println(string(out))
	// Output:
	// /search keyless: keyless
	// {"results":[{"title":"Go 1.27 Release Notes","url":"https://go.dev/doc/go1.27","score":0.9,"content":"The latest Go release, version 1.27, arrives six months after Go 1.26."}]}
}
