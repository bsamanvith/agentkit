package registry_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/bsamanvith/agentkit/pkg/mcp/registry"
)

func Example() {
	// A subregistry with one hosted server and one that is only a package.
	sub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"servers":[
			{"server":{"name":"com.example/issues","version":"1.0.0","remotes":[{"type":"streamable-http","url":"https://mcp.example.com/mcp"}]}},
			{"server":{"name":"com.example/local","version":"1.0.0","packages":[{"registryType":"npm","identifier":"x"}]}}
		],"metadata":{"count":2}}`))
	}))
	defer sub.Close()

	c, err := registry.New(sub.URL)
	if err != nil {
		panic(err)
	}
	found, err := c.Search(context.Background(), "example", 10)
	if err != nil {
		panic(err)
	}
	for _, s := range found {
		fmt.Println(s.Name, s.Remotes[0].Transport, s.Remotes[0].URL)
	}
	// Output: com.example/issues streamable-http https://mcp.example.com/mcp
}
