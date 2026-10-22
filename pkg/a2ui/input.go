package a2ui

import (
	"bytes"
	"encoding/json"
)

// ReadInput decodes the messages of a ui_render call's input that the tool
// has accepted, without validating them again: what [ParseInput] returns
// for it. The agent API uses it to send the messages of a successful call
// on, so that it links none of the catalog or the validator; for input not
// known to be valid, use ParseInput.
func ReadInput(input []byte) ([]Message, error) {
	var in struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	out := make([]Message, len(in.Messages))
	for i, raw := range in.Messages {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}
