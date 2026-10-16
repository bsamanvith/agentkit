package rpc_test

import (
	"context"
	"fmt"
	"net/http/httptest"

	"github.com/bsamanvith/agentkit/pkg/rpc"
)

type (
	greetReq  struct{ Name string }
	greetResp struct{ Greeting string }
)

// greet is declared once; the server handles it and the client calls it.
var greet = rpc.Unary[greetReq, greetResp]{Name: "hello/Greet"}

func Example() {
	srv := rpc.NewServer(rpc.WithToken("s3cret"))
	greet.Handle(srv, func(_ context.Context, r greetReq) (greetResp, error) {
		return greetResp{Greeting: "hello, " + r.Name}, nil
	})

	// In another container: the component mounts srv at rpc.PathPrefix + "hello/".
	ts := httptest.NewServer(srv)
	defer ts.Close()
	remote, err := rpc.Dial(ts.URL, rpc.WithBearer("s3cret"))
	if err != nil {
		fmt.Println(err)
		return
	}

	// The same call over the wire and in process.
	for _, conn := range []rpc.Conn{remote, rpc.Pipe(srv)} {
		resp, err := greet.Call(context.Background(), conn, greetReq{Name: "runner"})
		fmt.Println(resp.Greeting, err)
	}
	// Output:
	// hello, runner <nil>
	// hello, runner <nil>
}
