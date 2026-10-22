package wsclient_test

import (
	"context"
	"fmt"
	"net/http"

	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/agentclient/wsclient"
)

// A client whose run streams and commands go over one WebSocket: the same
// agentclient.Client, on the transport.
func Example() {
	ctx := context.Background()
	t, err := wsclient.New("http://localhost:8080", wsclient.WithToken("the-api-token"))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer t.Close()
	c, err := agentclient.New("http://localhost:8080", agentclient.WithHTTPClient(&http.Client{Transport: t}))
	if err != nil {
		fmt.Println(err)
		return
	}
	s, err := c.Stream(ctx, agentclient.RunRequest{Message: "hello"})
	if err != nil {
		fmt.Println(err)
		return
	}
	defer s.Close()
	for {
		e, err := s.Next()
		if err != nil {
			return
		}
		fmt.Println(e.Type)
	}
}
