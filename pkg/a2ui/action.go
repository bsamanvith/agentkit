package a2ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Action is what a person did on a surface: the spec's client-to-server
// action message (client_to_server.json). Context holds the action's
// context with its bindings resolved: the values the person chose.
type Action struct {
	Name              string          `json:"name"`
	SurfaceID         string          `json:"surfaceId"`
	SourceComponentID string          `json:"sourceComponentId"`
	Timestamp         time.Time       `json:"timestamp"`
	Context           json.RawMessage `json:"context"`
}

// actionMessage is an Action's envelope.
type actionMessage struct {
	Version string `json:"version"`
	Action  Action `json:"action"`
}

// The tags around an action in a user message.
const (
	actionOpen  = "<ui_action>\n"
	actionClose = "\n</ui_action>"
)

// Check reports what is wrong with a: an empty or overlong name, IDs that
// are not IDs, or a context that is not a JSON object within
// [MaxActionBytes] and [MaxDepth]. The error matches [ErrInvalid].
func (a Action) Check() error {
	var c checker
	if strings.TrimSpace(a.Name) == "" {
		c.fail("/name", ErrInvalid, "the action has no name")
	} else {
		c.str("/name", a.Name)
	}
	c.id("/surfaceId", a.SurfaceID, "surfaceId")
	c.id("/sourceComponentId", a.SourceComponentID, "sourceComponentId")
	if len(a.Context) > MaxActionBytes {
		c.fail("/context", ErrTooLarge, "the context is %d bytes, over the limit of %d", len(a.Context), MaxActionBytes)
	} else if len(a.Context) > 0 {
		v, err := decode(a.Context)
		if _, isObj := v.(map[string]any); err != nil || !isObj {
			c.fail("/context", ErrInvalid, "the context must be a JSON object")
		} else {
			c.depth("/context", v, 1)
		}
	}
	if len(c.errs) > 0 {
		return c.errs
	}
	return nil
}

// ActionText is the text of the user message that tells the agent about a:
// the action message as JSON (version, then the action, its timestamp in
// UTC and an absent context as {}), between <ui_action> tags. It fails when
// a does not pass [Action.Check].
func ActionText(a Action) (string, error) {
	if err := a.Check(); err != nil {
		return "", err
	}
	a.Timestamp = a.Timestamp.UTC()
	if len(a.Context) == 0 {
		a.Context = json.RawMessage(`{}`)
	} else {
		var buf bytes.Buffer
		if err := json.Compact(&buf, a.Context); err != nil {
			return "", fmt.Errorf("%w: the context: %w", ErrInvalid, err)
		}
		a.Context = buf.Bytes()
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(actionMessage{Version: Version, Action: a}); err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return actionOpen + strings.TrimSuffix(buf.String(), "\n") + actionClose, nil
}

// ParseActionText reads back the action of a user message's text written by
// [ActionText]; ok is false for any other text.
func ParseActionText(text string) (a Action, ok bool) {
	body, found := strings.CutPrefix(text, actionOpen)
	if !found {
		return Action{}, false
	}
	if body, found = strings.CutSuffix(body, actionClose); !found {
		return Action{}, false
	}
	var m actionMessage
	dec := json.NewDecoder(strings.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil || dec.More() || m.Version != Version || m.Action.Check() != nil {
		return Action{}, false
	}
	return m.Action, true
}
