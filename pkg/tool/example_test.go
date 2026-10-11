package tool_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/bsamanvith/agentkit/pkg/tool"
)

func ExampleFunc() {
	type addInput struct {
		A int `json:"a" jsonschema:"first number"`
		B int `json:"b" jsonschema:"second number"`
	}
	type addOutput struct {
		Sum int `json:"sum"`
	}

	add, err := tool.Func("add", "Add two integers.",
		func(_ context.Context, in addInput) (addOutput, error) {
			return addOutput{Sum: in.A + in.B}, nil
		})
	if err != nil {
		panic(err)
	}

	reg, err := tool.NewRegistry([]tool.Tool{add})
	if err != nil {
		panic(err)
	}

	out, err := reg.Invoke(context.Background(), "add", json.RawMessage(`{"a":2,"b":3}`))
	fmt.Println(string(out), err)

	_, err = reg.Invoke(context.Background(), "add", json.RawMessage(`{"a":"two"}`))
	fmt.Println(errors.Is(err, tool.ErrInvalidInput))
	// Output:
	// {"sum":5} <nil>
	// true
}
