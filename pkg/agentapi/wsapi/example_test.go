package wsapi_test

import (
	"fmt"
	"net/http"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wsapi"
)

// The binding is mounted beside the API it serves, as serve does.
func Example() {
	api := http.NotFoundHandler() // agentapi's Server.Handler()
	ws, err := wsapi.New(api)
	if err != nil {
		fmt.Println(err)
		return
	}
	mux := http.NewServeMux()
	mux.Handle(wire.WSPath, ws.Handler())
	mux.Handle("/v1/", api)
	fmt.Println(ws.Name(), "at", wire.WSPath)
	// Output: agent-ws at /v1/ws
}
