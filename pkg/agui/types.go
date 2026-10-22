package agui

import "encoding/json"

// The AG-UI wire format this server speaks: the events it sends and the
// request it takes, with the field names of the spec (@ag-ui/core 1.0.2,
// PROTOCOL_VERSION "1.0"). Only what the server uses is modelled; a request's
// other fields are ignored.

// ProtocolVersion is the AG-UI version the server speaks, which RUN_STARTED
// declares.
const ProtocolVersion = "1.0"

// EventType names an AG-UI event: the "type" of an [Event].
type EventType string

// The events the server sends.
const (
	EventRunStarted         EventType = "RUN_STARTED"
	EventRunFinished        EventType = "RUN_FINISHED"
	EventRunError           EventType = "RUN_ERROR"
	EventTextMessageStart   EventType = "TEXT_MESSAGE_START"
	EventTextMessageContent EventType = "TEXT_MESSAGE_CONTENT"
	EventTextMessageEnd     EventType = "TEXT_MESSAGE_END"
	EventToolCallStart      EventType = "TOOL_CALL_START"
	EventToolCallArgs       EventType = "TOOL_CALL_ARGS"
	EventToolCallEnd        EventType = "TOOL_CALL_END"
	EventToolCallResult     EventType = "TOOL_CALL_RESULT"
	EventActivitySnapshot   EventType = "ACTIVITY_SNAPSHOT"
	EventMessagesSnapshot   EventType = "MESSAGES_SNAPSHOT"
)

// Run outcomes, the Type of an [Outcome].
const (
	OutcomeSuccess   = "success"
	OutcomeInterrupt = "interrupt"
	OutcomeCancelled = "cancelled"
)

// Interrupt reasons the server gives (the spec's core values).
const (
	// ReasonToolCall: a tool call waits for an approval.
	ReasonToolCall = "tool_call"
	// ReasonInputRequired: a tool call asks a question.
	ReasonInputRequired = "input_required"
)

// Resume statuses, the Status of a [ResumeEntry].
const (
	ResumeResolved  = "resolved"
	ResumeCancelled = "cancelled"
)

// ActivityTypeA2UI is the activity type of an A2UI surface: an
// ACTIVITY_SNAPSHOT whose content holds the surface's A2UI messages under
// A2UIOperationsKey, as AG-UI's A2UI middleware and CopilotKit's A2UI
// renderer exchange them.
const ActivityTypeA2UI = "a2ui-surface"

// A2UIOperationsKey is the key of an A2UI activity's content that holds its
// messages, in order.
const A2UIOperationsKey = "a2ui_operations"

// MetadataKey is the key of an event's or an interrupt's metadata under
// which the server says what is its own: the conversation and run behind an
// AG-UI run, what an interrupt waits for.
const MetadataKey = "agentkit"

// Event is one AG-UI event: the JSON of an SSE data line. Only the fields of
// its Type are set; Delta, Content, Result and Messages are pointers or raw
// JSON so that a value the spec requires is written even when it is empty.
type Event struct {
	Type      EventType      `json:"type"`
	Timestamp int64          `json:"timestamp,omitempty"` // milliseconds since the Unix epoch
	Metadata  map[string]any `json:"metadata,omitempty"`

	// RUN_STARTED, RUN_FINISHED: the request's threadId and runId.
	ThreadID        string `json:"threadId,omitempty"`
	RunID           string `json:"runId,omitempty"`
	ProtocolVersion string `json:"protocolVersion,omitempty"`
	// RUN_FINISHED
	Result  json.RawMessage `json:"result,omitempty"`
	Outcome *Outcome        `json:"outcome,omitempty"`
	// RUN_FINISHED, RUN_ERROR
	Usage []TokenUsage `json:"usage,omitempty"`
	// RUN_ERROR
	Message string `json:"message,omitempty"`
	Code    string `json:"code,omitempty"`

	// TEXT_MESSAGE_*, TOOL_CALL_RESULT, ACTIVITY_SNAPSHOT
	MessageID string `json:"messageId,omitempty"`
	// TEXT_MESSAGE_START ("assistant"), TOOL_CALL_RESULT ("tool")
	Role string `json:"role,omitempty"`
	// TEXT_MESSAGE_CONTENT, TOOL_CALL_ARGS
	Delta *string `json:"delta,omitempty"`

	// TOOL_CALL_*
	ToolCallID      string `json:"toolCallId,omitempty"`
	ToolCallName    string `json:"toolCallName,omitempty"`
	ParentMessageID string `json:"parentMessageId,omitempty"`
	// TOOL_CALL_RESULT (a string), ACTIVITY_SNAPSHOT (an object)
	Content json.RawMessage `json:"content,omitempty"`

	// ACTIVITY_SNAPSHOT
	ActivityType string `json:"activityType,omitempty"`
	Replace      *bool  `json:"replace,omitempty"`

	// MESSAGES_SNAPSHOT: a JSON array of messages.
	Messages json.RawMessage `json:"messages,omitempty"`
}

// Outcome is why a run ended (RUN_FINISHED): success, interrupt (with what
// it waits for) or cancelled.
type Outcome struct {
	Type       string      `json:"type"`
	Interrupts []Interrupt `json:"interrupts,omitempty"`
}

// Interrupt is a tool call of the run that waits for people. Its ID is what
// a [ResumeEntry] answers.
type Interrupt struct {
	ID             string          `json:"id"`
	Reason         string          `json:"reason"`
	Message        string          `json:"message,omitempty"`
	ToolCallID     string          `json:"toolCallId,omitempty"`
	ResponseSchema json.RawMessage `json:"responseSchema,omitempty"`
	Metadata       map[string]any  `json:"metadata,omitempty"`
}

// TokenUsage is a run's token counts in AG-UI's accounting: InputTokens is
// the whole prompt, cache reads and writes included.
type TokenUsage struct {
	Provider              string `json:"provider,omitempty"`
	Model                 string `json:"model,omitempty"`
	InputTokens           int    `json:"inputTokens"`
	OutputTokens          int    `json:"outputTokens"`
	TotalTokens           int    `json:"totalTokens"`
	CachedInputTokens     int    `json:"cachedInputTokens,omitempty"`
	CacheWriteInputTokens int    `json:"cacheWriteInputTokens,omitempty"`
}

// RunAgentInput is the body of a request: the thread, the client's run ID,
// the conversation as the client has it, and the answers to a previous run's
// interrupts. The server reads the fields below and ignores the rest.
type RunAgentInput struct {
	ThreadID        string            `json:"threadId"`
	RunID           string            `json:"runId"`
	ProtocolVersion string            `json:"protocolVersion,omitempty"`
	Messages        []json.RawMessage `json:"messages"`
	// Tools are the frontend's tools, which the server does not offer the
	// agent; State and Context are not given to it either (see the package
	// doc).
	Tools          []json.RawMessage `json:"tools,omitempty"`
	State          json.RawMessage   `json:"state,omitempty"`
	Context        []json.RawMessage `json:"context,omitempty"`
	ForwardedProps json.RawMessage   `json:"forwardedProps,omitempty"`
	Resume         []ResumeEntry     `json:"resume,omitempty"`
}

// ResumeEntry answers one interrupt of the run before.
type ResumeEntry struct {
	InterruptID string          `json:"interruptId"`
	Status      string          `json:"status"`
	Payload     json.RawMessage `json:"payload,omitempty"`
}

// Message is a message of the conversation, as a request carries it and a
// MESSAGES_SNAPSHOT lists it: the fields the server reads or writes.
type Message struct {
	ID   string `json:"id"`
	Role string `json:"role"`
	// Content is a string, or (for a user's message) a list of parts.
	Content    json.RawMessage `json:"content,omitempty"`
	ToolCalls  []ToolCall      `json:"toolCalls,omitempty"`
	ToolCallID string          `json:"toolCallId,omitempty"`
}

// ToolCall is a call of an assistant message.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // "function"
	Function FunctionCall `json:"function"`
}

// FunctionCall is a call's tool and its arguments, as JSON text.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// UserAction is an action on an A2UI surface, in a request's
// forwardedProps as {"a2uiAction": {"userAction": ...}}: the convention of
// AG-UI's A2UI middleware and CopilotKit's renderer.
type UserAction struct {
	Name              string          `json:"name"`
	SurfaceID         string          `json:"surfaceId"`
	SourceComponentID string          `json:"sourceComponentId,omitempty"`
	Context           json.RawMessage `json:"context,omitempty"`
}
