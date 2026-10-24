package common

import (
	"encoding/json"
	"time"

	"github.com/bsamanvith/agentkit/connectors/internal/rest"
)

// DefaultTokenEnv is the variable "access_token_env" names by default.
const DefaultTokenEnv = "GOOGLE_ACCESS_TOKEN"

// Settings are the settings every Google connector has. A connector embeds it
// in its own settings struct, which [toolset.DecodeSettings] decodes strictly:
//
//	{"access_token_env": "GOOGLE_ACCESS_TOKEN",
//	 "base_url": "https://gmail.googleapis.com",
//	 "timeout": "30s"}
//
// All are optional.
type Settings struct {
	// AccessTokenEnv names the environment variable holding an access token,
	// used when no [TokenSource] was given to the factory.
	AccessTokenEnv string `json:"access_token_env,omitempty"`
	// BaseURL replaces the API's address: https, or http to localhost or a
	// loopback IP (a local replay server).
	BaseURL string `json:"base_url,omitempty"`
	// Timeout limits one request, a duration such as "30s" (default 30s, at
	// most 5m).
	Timeout string `json:"timeout,omitempty"`

	// tokenEnvSet says the settings named access_token_env themselves
	// (Validate fills in the default).
	tokenEnvSet bool
}

// Validate checks the settings of the named service and fills in defaults.
// Errors match [toolset.ErrInvalidSettings]. The offending value of
// access_token_env and base_url is never echoed: either can hold a secret
// pasted in the wrong place.
func (s *Settings) Validate(service string) error {
	s.tokenEnvSet = s.tokenEnvSet || s.AccessTokenEnv != ""
	if s.AccessTokenEnv == "" {
		s.AccessTokenEnv = DefaultTokenEnv
	}
	base, err := rest.CheckSettings(service, s.AccessTokenEnv, s.BaseURL, s.Timeout)
	s.BaseURL = base
	return err
}

// RequestTimeout is the limit on one request: the timeout setting, else 30 s.
// Call it after [Settings.Validate].
func (s Settings) RequestTimeout() time.Duration { return rest.Timeout(s.Timeout) }

// CheckBaseURL accepts an https URL, or an http one whose host is localhost or
// a loopback IP, and returns it without a trailing slash. The token goes
// wherever base_url points, hence https.
func CheckBaseURL(raw string) (string, bool) { return rest.CheckBaseURL(raw) }

// RefuseLiteralToken fails raw settings that hold a credential instead of
// naming its environment variable. [toolset.DecodeSettings] would refuse the
// unknown field too, but this says what to do instead and never echoes the
// value. Call it before decoding.
func RefuseLiteralToken(service string, raw json.RawMessage) error {
	return rest.RefuseLiteralToken(service, DefaultTokenEnv, raw)
}
