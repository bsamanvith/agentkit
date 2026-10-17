package wire

import (
	"encoding/json"
	"time"
)

// The statuses of a toolset added at run time, besides [ToolsetReady] and
// [ToolsetUnavailable] (its server cannot be reached just now).
const (
	// ToolsetReviewNeeded: the server lists a tool that is new, or changed
	// since it was approved. Those tools are off until an admin reviews
	// them; the others still work.
	ToolsetReviewNeeded = "review_needed"
	// ToolsetDisabled: an admin turned the toolset off; agents get none of
	// its tools.
	ToolsetDisabled = "disabled"
)

// The review statuses of a tool of a toolset added at run time.
const (
	// ToolApproved: approved as the server lists it now; agents may call it.
	ToolApproved = "approved"
	// ToolNew: the server lists it and nobody reviewed it. It is off.
	ToolNew = "new"
	// ToolChanged: approved once, but the server's definition differs from
	// the approved one now (a "rug pull" until shown otherwise). It is off
	// until approved again.
	ToolChanged = "changed"
	// ToolRemoved: approved, but the server no longer lists it.
	ToolRemoved = "removed"
	// ToolRejected: an admin turned this definition down. It stays off, and
	// needs no review until the server changes it.
	ToolRejected = "rejected"
)

// The auth kinds of an MCP server added at run time.
const (
	AuthNone    = "none"    // the server needs no credential
	AuthHeaders = "headers" // static headers, from variables the operator allows
	AuthOAuth   = "oauth"   // MCP OAuth: not supported for servers added at run time yet
)

// AddToolsetRequest is the body of POST /v1/toolsets: an MCP server to add
// as a toolset of the caller's tenant.
type AddToolsetRequest struct {
	// Type is the toolset's type: "mcp".
	Type string `json:"type"`
	// Name names the toolset, and prefixes its tools ("<name>__<tool>"). It
	// matches ^[a-zA-Z0-9_-]{1,64}$ and is not the name of another toolset.
	Name string `json:"name"`
	// URL is the server's endpoint: https, on a public address unless the
	// operator allows its host.
	URL string `json:"url"`
	// Transport is auto (the default: Streamable HTTP, then HTTP+SSE),
	// http or sse.
	Transport string `json:"transport,omitempty"`
	// Auth is AuthNone (the default) or AuthHeaders.
	Auth string `json:"auth,omitempty"`
	// Headers are sent with every request, with auth "headers". "${NAME}"
	// in a value is the server's variable NAME, which the operator must
	// allow; a header that carries a secret (Authorization, or a name with
	// key, token, secret or auth in it) must take its value from one.
	Headers map[string]string `json:"headers,omitempty"`
	// Scope is "tenant" (the default): one connection the tenant shares.
	// "user" (each user connects their own account) comes with MCP OAuth.
	Scope string `json:"scope,omitempty"`
}

// UpdateToolsetRequest is the body of PATCH /v1/toolsets/{name}. A field
// left out is left as it is.
type UpdateToolsetRequest struct {
	// Enabled turns the toolset on or off.
	Enabled *bool `json:"enabled,omitempty"`
	// Headers replace the toolset's headers (auth "headers").
	Headers map[string]string `json:"headers,omitempty"`
}

// ReviewRequest is the body of POST /v1/toolsets/{name}/review: decisions on
// some of the toolset's tools.
type ReviewRequest struct {
	Tools []ToolDecision `json:"tools"`
}

// The decisions of a [ToolDecision].
const (
	DecisionApprove = "approve"
	DecisionReject  = "reject"
)

// ToolDecision is an admin's decision on one tool.
type ToolDecision struct {
	Name string `json:"name"`
	// Fingerprint is the definition decided on, as the review showed it: a
	// decision on a tool that changed since is refused (409), so nobody
	// approves a definition they did not see.
	Fingerprint string `json:"fingerprint"`
	// Decision is DecisionApprove or DecisionReject.
	Decision string `json:"decision"`
	// Kind and Risk classify an approved tool (tool.Metadata). Left out,
	// the tool keeps what its annotations say, which can raise its class
	// but never lower it to read.
	Kind string `json:"kind,omitempty"`
	Risk string `json:"risk,omitempty"`
}

// ServerToolset is a toolset added at run time: what POST /v1/toolsets and
// the review endpoints return.
type ServerToolset struct {
	V    int    `json:"v"`
	Name string `json:"name"`
	Type string `json:"type"`
	URL  string `json:"url"`
	// Transport, Auth and Scope are as added; HeaderNames names the
	// headers sent, never their values.
	Transport   string   `json:"transport"`
	Auth        string   `json:"auth"`
	HeaderNames []string `json:"header_names,omitempty"`
	Scope       string   `json:"scope"`
	Enabled     bool     `json:"enabled"`
	// Status is ToolsetReady, ToolsetReviewNeeded, ToolsetDisabled or
	// ToolsetUnavailable, with Reason saying why it is unavailable.
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	// Server is what the server said about itself when it was last
	// reached.
	Server ServerInfo `json:"server"`
	// Tools are every tool the server lists, and every approved one it no
	// longer does, sorted by name.
	Tools []ReviewTool `json:"tools"`
	// Version changes on every write.
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ServerInfo is an MCP server's name and version, as it says.
type ServerInfo struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

// ReviewTool is one tool of a toolset added at run time, as a review shows
// it.
type ReviewTool struct {
	// Name is the server's name for the tool; agents call it
	// "<toolset>__<name>".
	Name string `json:"name"`
	// Status is one of the Tool statuses.
	Status string `json:"status"`
	// Fingerprint identifies the definition the server lists now (empty
	// for a removed tool): send it back to approve that definition.
	Fingerprint string `json:"fingerprint,omitempty"`
	// Description is what the model is given: the server's description,
	// sanitized. Current has it raw.
	Description string `json:"description,omitempty"`
	// Current is the definition the server lists now, and Approved the one
	// approved (each when there is one); Changes names the fields that
	// differ between them (name, title, description, inputSchema,
	// outputSchema, annotations).
	Current  *ToolDefinition `json:"current,omitempty"`
	Approved *ToolDefinition `json:"approved,omitempty"`
	Changes  []string        `json:"changes,omitempty"`
	// Kind and Risk are the tool's class: as the review set it, else as its
	// annotations claim (write and medium when they claim nothing).
	Kind string `json:"kind"`
	Risk string `json:"risk"`
	// ReviewedBy names the key that last decided on the tool.
	ReviewedBy string     `json:"reviewed_by,omitempty"`
	ReviewedAt *time.Time `json:"reviewed_at,omitempty"`
}

// ToolDefinition is a tool's definition as its MCP server sends it, raw:
// the description is not sanitized.
type ToolDefinition struct {
	Title        string          `json:"title,omitempty"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema,omitempty"`
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
	Annotations  json.RawMessage `json:"annotations,omitempty"`
}
