package wire_test

import (
	"encoding/json"
	"fmt"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
)

// Decoding an event ignores the fields this version does not know; encoding
// writes the header and the fields of the event's type.
func Example() {
	data := `{"v":1,"type":"tool_call","seq":3,"run_id":"run_1","at":"2026-10-12T09:00:00Z",` +
		`"turn":1,"call":0,"tool_use_id":"toolu_1","name":"echo","input":{"text":"hi"},"added_later":true}`
	var e wire.Event
	if err := json.Unmarshal([]byte(data), &e); err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(e.Type, e.Name, string(e.Input))
	out, _ := json.Marshal(e)
	fmt.Println(string(out))
	// Output:
	// tool_call echo {"text":"hi"}
	// {"v":1,"type":"tool_call","seq":3,"run_id":"run_1","at":"2026-10-12T09:00:00Z","turn":1,"call":0,"tool_use_id":"toolu_1","name":"echo","input":{"text":"hi"}}
}
