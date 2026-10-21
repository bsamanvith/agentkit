package a2a

import (
	"bytes"
	"encoding/json"
	"mime"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
)

// answerArtifactID names the artifact that holds a task's answer: one per
// task, so the ID only has to be unique within it.
const answerArtifactID = "answer"

// stateOf is the task state of a run status. A run that is interrupted or
// failed is failed for A2A, which has no state for a run that waits to be
// resumed: the agent API can still resume it (POST /v1/runs/{id}/resume).
// A suspended run waits for a decision: input-required.
func stateOf(status string) string {
	switch status {
	case wire.StatusRunning:
		return StateWorking
	case wire.StatusDone:
		return StateCompleted
	case wire.StatusCanceled:
		return StateCanceled
	case wire.StatusRejected:
		return StateRejected
	case wire.StatusSuspended:
		return StateInputRequired
	}
	return StateFailed // failed, interrupted, and any status added later
}

// statusTime is when a run's status last changed, as far as the run says:
// its end, else its start.
func statusTime(r wire.Run) time.Time {
	if r.FinishedAt != nil {
		return *r.FinishedAt
	}
	return r.CreatedAt
}

// taskOf is the task of a run: its status, and its answer as the one
// artifact when it has one (and artifacts are wanted).
func taskOf(r wire.Run, artifacts bool) Task {
	t := Task{ID: r.ID, ContextID: r.ConversationID, Status: statusOf(r)}
	if artifacts && r.Answer != "" {
		t.Artifacts = []Artifact{answerArtifact(r.Answer)}
	}
	return t
}

func statusOf(r wire.Run) TaskStatus {
	ts := Timestamp(statusTime(r))
	s := TaskStatus{State: stateOf(r.Status), Timestamp: &ts}
	if r.Error != nil && s.State != StateCompleted {
		msg := agentMessage(r.ID, r.ConversationID, r.ID+"-status", r.Error.Message)
		s.Message = &msg
	}
	return s
}

func answerArtifact(text string) Artifact {
	return Artifact{ArtifactID: answerArtifactID, Name: "answer", Parts: []Part{TextPart(text)}}
}

func agentMessage(taskID, contextID, id, text string) Message {
	return Message{MessageID: id, TaskID: taskID, ContextID: contextID, Role: RoleAgent, Parts: []Part{TextPart(text)}}
}

// promptOf turns a user's message into the text of a run's prompt: text
// parts as they are and data parts as their JSON, one after the other with a
// blank line between them. File parts (raw bytes or a URL) are refused with
// ContentTypeNotSupportedError: the agent API's messages are text.
func promptOf(m *Message) (string, error) {
	if m == nil {
		return "", ErrInvalidParams.with("params.message is required")
	}
	switch m.Role {
	case RoleUser, "user":
	default:
		return "", ErrInvalidParams.with(`params.message.role: want "ROLE_USER"`)
	}
	if len(m.Parts) == 0 {
		return "", ErrInvalidParams.with("params.message.parts: at least one part is required")
	}
	var texts []string
	for _, p := range m.Parts {
		set := 0
		for _, ok := range []bool{p.Text != nil, p.Raw != nil, p.URL != nil, p.Data != nil} {
			if ok {
				set++
			}
		}
		if set != 1 {
			return "", ErrInvalidParams.with("params.message.parts: each part has exactly one of text, raw, url and data")
		}
		switch {
		case p.Raw != nil, p.URL != nil:
			return "", ErrContentType.with("file parts are not supported: send text or data", "mediaType", p.MediaType)
		case p.Data != nil:
			var buf bytes.Buffer
			if err := json.Compact(&buf, *p.Data); err != nil {
				return "", ErrInvalidParams.with("params.message.parts: a data part is not JSON")
			}
			texts = append(texts, buf.String())
		default:
			texts = append(texts, *p.Text)
		}
	}
	text := strings.Join(texts, "\n\n")
	if strings.TrimSpace(text) == "" {
		// The agent API reads an empty message as "resume", so it never
		// gets one from here.
		return "", ErrInvalidParams.with("params.message: the message has no text")
	}
	return text, nil
}

// acceptsText reports whether the output modes a client accepts include
// text, the only output the agents have. None listed accepts anything.
func acceptsText(modes []string) bool {
	if len(modes) == 0 {
		return true
	}
	for _, m := range modes {
		mt, _, err := mime.ParseMediaType(m)
		if err != nil {
			continue
		}
		switch mt {
		case "text/plain", "text/markdown", "text/*", "*/*":
			return true
		}
	}
	return false
}
