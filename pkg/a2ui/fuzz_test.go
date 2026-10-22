package a2ui_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/a2ui"
)

// FuzzValidate feeds any bytes to the validator, as one message and as a
// call's input. It never panics; every error matches ErrInvalid and is
// Errors with a detail for each problem; and what it accepts it accepts
// again: re-validated as typed messages, and encoded and parsed once more.
func FuzzValidate(f *testing.F) {
	for _, name := range []string{"messages.jsonl", "contact_form.jsonl"} {
		for _, line := range lines(f, name) {
			f.Add(line)
			f.Add(input(line))
		}
	}
	f.Add(mustJSON(base()))
	f.Add([]byte(`{"messages":[{"version":"v0.9","updateComponents":{"surfaceId":"s","components":[{"id":"root","component":"Card","child":"root"}]}}]}`))
	f.Add([]byte(`{"messages":[{"version":"v0.9","updateComponents":{"surfaceId":"s","components":[{"id":"root","component":"Column","children":{"componentId":"x","path":"/l"}},{"id":"x","component":"Text","text":{"call":"formatString","args":{"value":{"path":"name"}}}}]}}]}`))
	f.Add([]byte(`{"version":"v0.9","updateDataModel":{"surfaceId":"s","path":"/a~2","value":[[[[1]]]]}}`))
	f.Add([]byte(`[]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		check := func(what string, err error) bool {
			if err == nil {
				return true
			}
			var es a2ui.Errors
			if !errors.Is(err, a2ui.ErrInvalid) || !errors.As(err, &es) || len(es) == 0 {
				t.Fatalf("%s: %v is not Errors matching ErrInvalid", what, err)
			}
			for _, e := range es {
				if e.Detail == "" || e.Err == nil {
					t.Fatalf("%s: problem %+v has no detail or sentinel", what, e)
				}
			}
			return false
		}
		if m, err := a2ui.Parse(data); check("Parse", err) {
			if err := a2ui.Validate(m); err != nil {
				t.Fatalf("Parse accepted %s, Validate of the result says %v", data, err)
			}
			again, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := a2ui.Parse(again); err != nil {
				t.Fatalf("Parse accepted %s but not its encoding %s: %v", data, again, err)
			}
		}
		if msgs, err := a2ui.ParseInput(data); check("ParseInput", err) {
			if len(msgs) == 0 {
				t.Fatalf("ParseInput accepted %s with no messages", data)
			}
			if err := a2ui.ValidateBatch(msgs); err != nil {
				t.Fatalf("ParseInput accepted %s, ValidateBatch of the result says %v", data, err)
			}
		}
	})
}
