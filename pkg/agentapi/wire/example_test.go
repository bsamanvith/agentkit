package wire_test

import (
	"encoding/json"
	"fmt"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
)

// Decoding an event ignores the fields this version does not know; encoding
// writes the header and the fields of the event's type.
func Example() {
	data := `{"v":1,"type":"tool_call","id":"3","seq":3,"run_id":"run_1","at":"2026-10-12T09:00:00Z",` +
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
	// {"v":1,"type":"tool_call","id":"3","seq":3,"run_id":"run_1","at":"2026-10-12T09:00:00Z","turn":1,"call":0,"tool_use_id":"toolu_1","name":"echo","input":{"text":"hi"}}
}

// Event IDs order a run's events: a text_delta's ID comes after the stored
// event before it.
func ExampleCompareIDs() {
	fmt.Println(wire.CompareIDs("12", "12.3"), wire.CompareIDs("12.3", "12.10"), wire.CompareIDs("13", "12.10"))
	// Output: -1 -1 1
}
