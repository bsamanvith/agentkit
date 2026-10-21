package a2a

import (
	"encoding/json"
	"strings"
	"time"
)

// The wire types of A2A v1.0 (specification/a2a.proto at tag v1.0.0), in
// their ProtoJSON form: camelCase fields, enums by their proto names,
// timestamps in RFC 3339 UTC. Only what this server reads or writes is here.

// ProtocolVersion is the A2A version this server speaks (Major.Minor).
const ProtocolVersion = "1.0"

// Task states (TaskState).
const (
	StateSubmitted     = "TASK_STATE_SUBMITTED"
	StateWorking       = "TASK_STATE_WORKING"
	StateCompleted     = "TASK_STATE_COMPLETED"
	StateFailed        = "TASK_STATE_FAILED"
	StateCanceled      = "TASK_STATE_CANCELED"
	StateInputRequired = "TASK_STATE_INPUT_REQUIRED"
	StateRejected      = "TASK_STATE_REJECTED"
	StateAuthRequired  = "TASK_STATE_AUTH_REQUIRED"
)

// Roles of a message (Role).
const (
	RoleUser  = "ROLE_USER"
	RoleAgent = "ROLE_AGENT"
)

// Protocol bindings an AgentInterface names.
const (
	BindingJSONRPC  = "JSONRPC"
	BindingHTTPJSON = "HTTP+JSON"
)

// terminal reports whether a task in state s can change no more.
func terminal(s string) bool {
	switch s {
	case StateCompleted, StateFailed, StateCanceled, StateRejected:
		return true
	}
	return false
}

// Timestamp is a time in the spec's form, "2025-10-28T10:30:00.000Z".
type Timestamp time.Time

const timestampLayout = "2006-01-02T15:04:05.000Z07:00"

// MarshalJSON writes t in UTC with milliseconds.
func (t Timestamp) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Time(t).UTC().Format(timestampLayout))
}

// UnmarshalJSON reads an RFC 3339 time.
func (t *Timestamp) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return err
	}
	*t = Timestamp(v)
	return nil
}

// Part is one part of a message or an artifact: exactly one of Text, Raw,
// URL and Data is set.
type Part struct {
	Text      *string          `json:"text,omitempty"`
	Raw       *string          `json:"raw,omitempty"` // base64 bytes
	URL       *string          `json:"url,omitempty"`
	Data      *json.RawMessage `json:"data,omitempty"`
	Metadata  json.RawMessage  `json:"metadata,omitempty"`
	Filename  string           `json:"filename,omitempty"`
	MediaType string           `json:"mediaType,omitempty"`
}

// TextPart returns a text part.
func TextPart(s string) Part { return Part{Text: &s} }

// Message is one message of a conversation.
type Message struct {
	MessageID        string          `json:"messageId"`
	ContextID        string          `json:"contextId,omitempty"`
	TaskID           string          `json:"taskId,omitempty"`
	Role             string          `json:"role"`
	Parts            []Part          `json:"parts"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
	Extensions       []string        `json:"extensions,omitempty"`
	ReferenceTaskIDs []string        `json:"referenceTaskIds,omitempty"`
}

// TaskStatus is where a task is.
type TaskStatus struct {
	State     string     `json:"state"`
	Message   *Message   `json:"message,omitempty"`
	Timestamp *Timestamp `json:"timestamp,omitempty"`
}

// Artifact is an output of a task.
type Artifact struct {
	ArtifactID  string `json:"artifactId"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Parts       []Part `json:"parts"`
}

// Task is a unit of work: here, one run of the agent API.
type Task struct {
	ID        string     `json:"id"`
	ContextID string     `json:"contextId,omitempty"`
	Status    TaskStatus `json:"status"`
	Artifacts []Artifact `json:"artifacts,omitempty"`
	History   []Message  `json:"history,omitempty"`
}

// TaskStatusUpdateEvent is a change of a task's status, in a stream.
type TaskStatusUpdateEvent struct {
	TaskID    string     `json:"taskId"`
	ContextID string     `json:"contextId"`
	Status    TaskStatus `json:"status"`
}

// TaskArtifactUpdateEvent is a piece of an artifact, in a stream.
type TaskArtifactUpdateEvent struct {
	TaskID    string   `json:"taskId"`
	ContextID string   `json:"contextId"`
	Artifact  Artifact `json:"artifact"`
	Append    bool     `json:"append,omitempty"`
	LastChunk bool     `json:"lastChunk,omitempty"`
}

// StreamResponse is one item of a stream: exactly one field is set.
type StreamResponse struct {
	Task           *Task                    `json:"task,omitempty"`
	Message        *Message                 `json:"message,omitempty"`
	StatusUpdate   *TaskStatusUpdateEvent   `json:"statusUpdate,omitempty"`
	ArtifactUpdate *TaskArtifactUpdateEvent `json:"artifactUpdate,omitempty"`
}

// SendMessageResponse is the result of SendMessage: here always a task.
type SendMessageResponse struct {
	Task    *Task    `json:"task,omitempty"`
	Message *Message `json:"message,omitempty"`
}

// PushNotificationConfig is only recognized, to refuse it.
type pushConfig = json.RawMessage

// SendMessageConfiguration is how a message is to be handled.
type SendMessageConfiguration struct {
	AcceptedOutputModes        []string   `json:"acceptedOutputModes,omitempty"`
	TaskPushNotificationConfig pushConfig `json:"taskPushNotificationConfig,omitempty"`
	// PushNotificationConfig is the name the spec's own example (6.6) uses.
	PushNotificationConfig pushConfig `json:"pushNotificationConfig,omitempty"`
	HistoryLength          *int32     `json:"historyLength,omitempty"`
	ReturnImmediately      bool       `json:"returnImmediately,omitempty"`
}

// SendMessageRequest is the params of SendMessage and SendStreamingMessage.
type SendMessageRequest struct {
	Tenant        string                    `json:"tenant,omitempty"`
	Message       *Message                  `json:"message"`
	Configuration *SendMessageConfiguration `json:"configuration,omitempty"`
	Metadata      json.RawMessage           `json:"metadata,omitempty"`
}

// GetTaskRequest is the params of GetTask.
type GetTaskRequest struct {
	Tenant        string `json:"tenant,omitempty"`
	ID            string `json:"id"`
	HistoryLength *int32 `json:"historyLength,omitempty"`
}

// ListTasksRequest is the params of ListTasks.
type ListTasksRequest struct {
	Tenant               string     `json:"tenant,omitempty"`
	ContextID            string     `json:"contextId,omitempty"`
	Status               string     `json:"status,omitempty"`
	PageSize             *int32     `json:"pageSize,omitempty"`
	PageToken            string     `json:"pageToken,omitempty"`
	HistoryLength        *int32     `json:"historyLength,omitempty"`
	StatusTimestampAfter *Timestamp `json:"statusTimestampAfter,omitempty"`
	IncludeArtifacts     *bool      `json:"includeArtifacts,omitempty"`
}

// ListTasksResponse is the result of ListTasks.
type ListTasksResponse struct {
	Tasks         []Task `json:"tasks"`
	NextPageToken string `json:"nextPageToken"`
	PageSize      int32  `json:"pageSize"`
	TotalSize     int32  `json:"totalSize"`
}

// TaskIDRequest is the params of CancelTask and SubscribeToTask.
type TaskIDRequest struct {
	Tenant   string          `json:"tenant,omitempty"`
	ID       string          `json:"id"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// AgentCard describes an agent: what it does, where and how to call it.
type AgentCard struct {
	Name                 string                    `json:"name"`
	Description          string                    `json:"description"`
	SupportedInterfaces  []AgentInterface          `json:"supportedInterfaces"`
	Provider             *AgentProvider            `json:"provider,omitempty"`
	Version              string                    `json:"version"`
	DocumentationURL     string                    `json:"documentationUrl,omitempty"`
	Capabilities         AgentCapabilities         `json:"capabilities"`
	SecuritySchemes      map[string]SecurityScheme `json:"securitySchemes,omitempty"`
	SecurityRequirements []SecurityRequirement     `json:"securityRequirements,omitempty"`
	DefaultInputModes    []string                  `json:"defaultInputModes"`
	DefaultOutputModes   []string                  `json:"defaultOutputModes"`
	Skills               []AgentSkill              `json:"skills"`
}

// AgentInterface is one URL and binding the agent is served at.
type AgentInterface struct {
	URL             string `json:"url"`
	ProtocolBinding string `json:"protocolBinding"`
	ProtocolVersion string `json:"protocolVersion"`
}

// AgentProvider is the organization behind an agent.
type AgentProvider struct {
	Organization string `json:"organization"`
	URL          string `json:"url"`
}

// AgentCapabilities are the optional features the agent has.
type AgentCapabilities struct {
	Streaming         bool `json:"streaming"`
	PushNotifications bool `json:"pushNotifications"`
	ExtendedAgentCard bool `json:"extendedAgentCard"`
}

// AgentSkill is something the agent can do.
type AgentSkill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Examples    []string `json:"examples,omitempty"`
	InputModes  []string `json:"inputModes,omitempty"`
	OutputModes []string `json:"outputModes,omitempty"`
}

// SecurityScheme is one way to authenticate: exactly one field is set.
type SecurityScheme struct {
	APIKey        *APIKeySecurityScheme        `json:"apiKeySecurityScheme,omitempty"`
	HTTPAuth      *HTTPAuthSecurityScheme      `json:"httpAuthSecurityScheme,omitempty"`
	OpenIDConnect *OpenIDConnectSecurityScheme `json:"openIdConnectSecurityScheme,omitempty"`
}

// APIKeySecurityScheme is a key sent in a header, query or cookie.
type APIKeySecurityScheme struct {
	Description string `json:"description,omitempty"`
	Location    string `json:"location"`
	Name        string `json:"name"`
}

// HTTPAuthSecurityScheme is an HTTP authentication scheme, such as Bearer.
type HTTPAuthSecurityScheme struct {
	Description  string `json:"description,omitempty"`
	Scheme       string `json:"scheme"`
	BearerFormat string `json:"bearerFormat,omitempty"`
}

// OpenIDConnectSecurityScheme is an OpenID Connect provider.
type OpenIDConnectSecurityScheme struct {
	Description      string `json:"description,omitempty"`
	OpenIDConnectURL string `json:"openIdConnectUrl"`
}

// SecurityRequirement names schemes that together authenticate a request,
// each with the scopes it needs.
type SecurityRequirement struct {
	Schemes map[string]StringList `json:"schemes"`
}

// StringList is a list of strings, as the proto wraps one in a map.
type StringList struct {
	List []string `json:"list"`
}

// parseState reads a TaskState: its proto name, or the short lowercase form
// the spec's query examples use ("working", "input-required").
func parseState(s string) (string, bool) {
	up := strings.ToUpper(strings.ReplaceAll(s, "-", "_"))
	if !strings.HasPrefix(up, "TASK_STATE_") {
		up = "TASK_STATE_" + up
	}
	switch up {
	case StateSubmitted, StateWorking, StateCompleted, StateFailed, StateCanceled,
		StateInputRequired, StateRejected, StateAuthRequired:
		return up, true
	}
	return "", false
}
