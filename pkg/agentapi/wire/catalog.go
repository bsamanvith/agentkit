package wire

import (
	"encoding/json"
	"time"
)

// CodeAgentMismatch is HTTP 409: the run names another agent than the one
// the conversation belongs to. Continue it without "agent" (or with its own),
// or start a new conversation.
const CodeAgentMismatch = "agent_mismatch"

// Toolset statuses.
const (
	// ToolsetReady: the toolset was built; its tools are listed.
	ToolsetReady = "ready"
	// ToolsetSkipped: an optional toolset that failed to build (a missing
	// key, say); Reason says why. It has no tools.
	ToolsetSkipped = "skipped"
)

// Credential statuses of a toolset. Values are never sent, only where they
// come from.
const (
	// CredentialConfigured: the toolset's key is set (in Credential.Env).
	CredentialConfigured = "configured"
	// CredentialMissing: the toolset needs a key and Credential.Env is unset.
	CredentialMissing = "missing"
	// CredentialKeyless: the toolset runs without a key (a keyless tier);
	// setting Credential.Env, if named, gives it one.
	CredentialKeyless = "keyless"
	// CredentialNotNeeded: the toolset needs no credential at all.
	CredentialNotNeeded = "not_needed"
)

// Agent is an agent the server runs, as GET /v1/agents lists it and GET
// /v1/agents/{name} returns it.
type Agent struct {
	V           int    `json:"v"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Default marks the agent a run gets when it names none.
	Default bool  `json:"default"`
	Model   Model `json:"model"`
	// Toolsets are the toolsets the agent uses, in its config's order, with
	// the names its model sees for their tools.
	Toolsets []AgentToolset `json:"toolsets"`
	// Skills and Memory describe the agent's skills and long-term memory
	// (E12); empty and false until those land.
	Skills []string `json:"skills"`
	Memory bool     `json:"memory"`
	// System is the system prompt: only in GET /v1/agents/{name}, and only
	// for admins once the API has roles.
	System string `json:"system,omitempty"`
}

// Model is the model an agent runs on.
type Model struct {
	Provider string `json:"provider,omitempty"`
	ID       string `json:"id"`
	Effort   string `json:"effort,omitempty"`
}

// AgentToolset is one toolset of an agent.
type AgentToolset struct {
	Name string `json:"name"`
	// Status is ToolsetReady or ToolsetSkipped (then Tools is empty).
	Status string `json:"status"`
	// Tools are the tool names the agent's model sees (prefixed with the
	// toolset's name when the agent prefixes them).
	Tools []string `json:"tools"`
}

// AgentList is the body of GET /v1/agents.
type AgentList struct {
	V      int     `json:"v"`
	Agents []Agent `json:"agents"`
}

// ConversationSummary is a conversation in GET /v1/conversations: what a
// list shows, without the messages.
type ConversationSummary struct {
	ID    string `json:"id"`
	Agent string `json:"agent"`
	// Title is the start of the conversation's first message: empty for a
	// conversation created without one, until its first message.
	Title        string    `json:"title"`
	Status       string    `json:"status"`
	MessageCount int       `json:"message_count"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// CreateConversationRequest is the body of POST /v1/conversations, which
// creates an empty conversation: a client gets its ID before the first
// message (POST /v1/runs with that conversation_id).
type CreateConversationRequest struct {
	// Agent names the conversation's agent (empty: the default agent). The
	// conversation stays with it.
	Agent string `json:"agent,omitempty"`
}

// ConversationList is the body of GET /v1/conversations: a page of the
// caller's conversations, newest (most recently started) first.
type ConversationList struct {
	V             int                   `json:"v"`
	Conversations []ConversationSummary `json:"conversations"`
	// NextCursor, when set, fetches the next page (?cursor=). It is opaque.
	NextCursor string `json:"next_cursor,omitempty"`
}

// Toolset is a toolset as GET /v1/toolsets lists it.
type Toolset struct {
	Name string `json:"name"`
	// Type is the connector ("demo", "tavily", ...).
	Type string `json:"type"`
	// Status is ToolsetReady or ToolsetSkipped.
	Status string `json:"status"`
	// Reason says why a skipped toolset was skipped.
	Reason string `json:"reason,omitempty"`
	// MCPPath is where the server serves the toolset over MCP, relative to
	// the server's base URL ("/mcp/demo"); empty when it is not served.
	MCPPath    string     `json:"mcp_path,omitempty"`
	ToolCount  int        `json:"tool_count"`
	Credential Credential `json:"credential"`
}

// Credential is where a toolset's credential comes from, and whether it is
// there. It never holds the value.
type Credential struct {
	// Status is one of the Credential constants.
	Status string `json:"status"`
	// Env names the environment variable the key is read from, if any.
	Env string `json:"env,omitempty"`
}

// ToolsetList is the body of GET /v1/toolsets.
type ToolsetList struct {
	V        int       `json:"v"`
	Toolsets []Toolset `json:"toolsets"`
}

// Tool is a tool's definition, under the name its toolset gives it.
type Tool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"input_schema"`
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
}

// ToolList is the body of GET /v1/toolsets/{name}/tools.
type ToolList struct {
	V       int    `json:"v"`
	Toolset string `json:"toolset"`
	Status  string `json:"status"`
	Tools   []Tool `json:"tools"`
}
