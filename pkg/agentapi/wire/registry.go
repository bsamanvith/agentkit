package wire

import (
	"encoding/json"
	"time"
)

// Error codes of the agent registry (T075).
const (
	// CodeInvalidDefinition ("invalid_definition", HTTP 400): the agent
	// definition was refused; Fields says what is wrong with which field.
	// Nothing was saved.
	CodeInvalidDefinition = "invalid_definition"
	// CodeReadOnly ("read_only", HTTP 409): the agent comes from the
	// server's config file; change it there.
	CodeReadOnly = "read_only"
	// CodeVersionPublished ("version_published", HTTP 409): the version
	// was published (now or before), so runs may have used it, and it is
	// kept. Only drafts are deleted.
	CodeVersionPublished = "version_published"
)

// FieldError is what is wrong with one field of a definition: Field is its
// path in the definition's JSON ("model.provider", "toolsets"), empty for
// the definition as a whole.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// SaveAgentRequest is the body of POST /v1/agents/{name}/versions: a new
// version of a stored agent, which creates the agent when it has none.
type SaveAgentRequest struct {
	// Definition is the agent: the shape of an entry of "agents" in the
	// serve config file. Its "name", if any, must be the path's.
	Definition json.RawMessage `json:"definition"`
	// Message says why, for the version list.
	Message string `json:"message,omitempty"`
	// IfLatest, when set, saves only if the agent's latest version is this
	// one (0: only if the agent does not exist yet), else CodeConflict.
	IfLatest *int64 `json:"if_latest,omitempty"`
	// Publish publishes the new version at once.
	Publish bool `json:"publish,omitempty"`
}

// PublishRequest is the body of POST /v1/agents/{name}/publish: the version
// new runs of the agent use from now on. Publishing an older version rolls
// back; runs already started keep theirs.
type PublishRequest struct {
	Version int64 `json:"version"`
}

// AgentVersion is one version of a stored agent.
type AgentVersion struct {
	V       int    `json:"v"`
	Name    string `json:"name"`
	Version int64  `json:"version"`
	// Published says the version is the one new runs use.
	Published bool `json:"published"`
	// PublishedAt is when the version was first published; absent for a
	// draft that never was. Such a version is kept for good.
	PublishedAt *time.Time `json:"published_at,omitempty"`
	// Definition is the agent as saved, with its "name".
	Definition json.RawMessage `json:"definition"`
	// Author names the key that saved it; Message says why.
	Author    string    `json:"author,omitempty"`
	Message   string    `json:"message,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// AgentVersionList is the body of GET /v1/agents/{name}/versions and the
// answer to a publish: the agent's versions, oldest first.
type AgentVersionList struct {
	V    int    `json:"v"`
	Name string `json:"name"`
	// ReadOnly marks an agent of the config file, which has no stored
	// versions: change it in the file.
	ReadOnly bool `json:"read_only,omitempty"`
	// Published is the version new runs use, 0 for none; Latest the
	// highest number given.
	Published int64          `json:"published"`
	Latest    int64          `json:"latest"`
	Versions  []AgentVersion `json:"versions"`
}
