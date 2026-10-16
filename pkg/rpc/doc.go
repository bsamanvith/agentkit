// Package rpc is the transport between agentkit's components: methods declared
// once, as typed values, then served by a [Server] and called through a
// [Conn], over HTTP between containers or in process.
//
// A method is a value that names it and gives its types. The same value
// registers the server's handler and makes the client's call, so the two
// cannot disagree, and adding a method is one declaration and one line on
// each side:
//
//	var Add = rpc.Unary[AddRequest, AddResponse]{Name: "calc/Add"}
//
//	srv := rpc.NewServer()
//	Add.Handle(srv, func(ctx context.Context, r AddRequest) (AddResponse, error) {
//		return AddResponse{Sum: r.A + r.B}, nil
//	})
//
//	conn, _ := rpc.Dial("http://calc:8080")     // or rpc.Pipe(srv), in process
//	resp, err := Add.Call(ctx, conn, AddRequest{A: 1, B: 2})
//
// [Stream] is the same for a method that answers with a sequence of messages
// (a run's events). A package that puts an interface on the wire (a tool
// registry, a runner) declares its methods, a Register function that serves
// an implementation, and a client type that implements the interface by
// calling them: in-process and remote implementations are then
// interchangeable, and run the same conformance suite.
//
// # Errors
//
// Errors keep their identity across the wire. A method lists [Code]s: an
// error the handler returns that matches a code's Err ([errors.Is]) crosses
// as the code's name, and the caller gets an [*Error] that matches the same
// Err. Every method also knows the context's errors and this package's own.
// [WithData] attaches a value to an error for the caller (a run's partial
// result), read back with [Error.Data].
//
// # Protocol
//
// A call is POST <base>/rpc/<method> with the request as JSON. The answer is
// newline-delimited JSON frames: {"data":...} per message, ended by
// {"end":true} or by {"error":{...}}. A stream cut short without either is
// [ErrUnavailable]. A client's deadline travels in the Agentkit-Timeout
// header, so the handler stops when the caller gives up. With a token
// ([WithToken], [WithBearer]) every call carries Authorization: Bearer.
//
// Components of one release talk to each other: messages are plain JSON, and
// unknown fields are ignored, but nothing negotiates versions.
package rpc
