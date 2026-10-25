package wire

// Error codes of model choice (T125).
const (
	// CodeModelNotAllowed ("model_not_allowed", HTTP 400): the run names a
	// model its agent does not allow; the message lists those it does
	// (GET /v1/agents/{name} "models").
	CodeModelNotAllowed = "model_not_allowed"
	// CodeModelProviderMismatch ("model_provider_mismatch", HTTP 409): the
	// run names a model of another provider than the conversation's last
	// run. A provider cannot read another's replies (thinking blocks and
	// their signatures), so start a new conversation for it.
	CodeModelProviderMismatch = "model_provider_mismatch"
)

// AgentModels are the models an agent may run, in [Agent].
type AgentModels struct {
	// Default is the model a run that names none gets:
	// "<provider>/<id>".
	Default string `json:"default"`
	// Allow are the models allowed by name, the default first.
	Allow []string `json:"allow"`
	// AllowPatterns allow every model name a pattern matches whole (RE2).
	AllowPatterns []string `json:"allow_patterns,omitempty"`
	// Choices are the models to offer a person: Allow, then the models of
	// GET /v1/models that a pattern allows.
	Choices []string `json:"choices"`
}

// ModelProvider is a provider of models this server builds, in
// [ModelList].
type ModelProvider struct {
	// Name is the provider's name, the part of a model name before "/".
	Name string `json:"name"`
	// Models are model names the server knows for the provider, for
	// suggestions: the provider may serve others, and patterns may allow
	// them.
	Models []string `json:"models"`
}

// ModelList is the body of GET /v1/models.
type ModelList struct {
	V int `json:"v"`
	// Default is the default provider: that of a model name without "/".
	Default   string          `json:"default"`
	Providers []ModelProvider `json:"providers"`
}
