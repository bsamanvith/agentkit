package toolset

import (
	"bytes"
	"encoding/json"

	"github.com/bsamanvith/agentkit/pkg/credentials"
)

// Spec is one toolset entry in config. Its JSON form is the config contract
// (snake_case keys; pkg/config decodes the YAML form into the same shape):
//
//	{"name": "gmail-readonly", "type": "gmail",
//	 "include": ["gmail_search", "gmail_read*"], "optional": true,
//	 "credential": {"slot": "gmail", "scope": "user"}}
type Spec struct {
	// Name is the toolset's name and URL path segment (/mcp/{name}). It must
	// match ^[a-zA-Z0-9_-]{1,64}$ and be unique.
	Name string `json:"name"`
	// Type selects the factory in the [Catalog].
	Type string `json:"type"`
	// Settings is handed to the factory, which decodes and validates it.
	Settings json.RawMessage `json:"settings,omitempty"`
	// Include, if set, keeps only tools whose name matches one of these
	// globs (path.Match syntax: *, ?, [a-z]). Each glob must match a tool.
	Include []string `json:"include,omitempty"`
	// Exclude removes tools whose name matches one of these globs, after
	// Include is applied. Each glob must match a tool, to catch typos that
	// would leave a tool exposed.
	Exclude []string `json:"exclude,omitempty"`
	// Optional makes a toolset that fails to build be skipped and reported as
	// a [Problem] instead of failing [Build].
	Optional bool `json:"optional,omitempty"`
	// Credential, if set, names the credential slot the toolset's tools
	// call their upstream with: the factory gets a [credentials.Source] of
	// that slot alone, which picks the caller's credential at call time.
	// No secret is ever in config.
	Credential *CredentialRef `json:"credential,omitempty"`
}

// CredentialRef names a credential slot and whose credential in it a
// toolset uses. Several toolsets may share a slot; two accounts of one
// service (two Salesforce orgs) are two slots.
type CredentialRef struct {
	// Slot is the slot's name, with the toolset name rule.
	Slot string `json:"slot"`
	// Scope is [credentials.ScopeTenant] (the default: the tenant's shared
	// credential) or [credentials.ScopeUser] (the calling user's own).
	Scope credentials.Scope `json:"scope,omitempty"`
}

// UnmarshalJSON decodes a Spec, rejecting unknown fields: a misspelled
// "inclde" must not silently expose every tool.
func (s *Spec) UnmarshalJSON(data []byte) error {
	type plain Spec // no methods, so no recursion
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var p plain
	if err := dec.Decode(&p); err != nil {
		return err
	}
	*s = Spec(p)
	return nil
}
