package common

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/toolset"
)

const (
	// DefaultTokenEnv is the variable "access_token_env" names by default.
	DefaultTokenEnv = "GOOGLE_ACCESS_TOKEN"

	defaultTimeout = 30 * time.Second
	maxTimeout     = 5 * time.Minute
)

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

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
}

// Validate checks the settings of the named service and fills in defaults.
// Errors match [toolset.ErrInvalidSettings]. The offending value of
// access_token_env and base_url is never echoed: either can hold a secret
// pasted in the wrong place.
func (s *Settings) Validate(service string) error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s: %s", toolset.ErrInvalidSettings, service, fmt.Sprintf(format, args...))
	}
	if s.AccessTokenEnv == "" {
		s.AccessTokenEnv = DefaultTokenEnv
	}
	if !envNamePattern.MatchString(s.AccessTokenEnv) {
		return bad("access_token_env must name an environment variable (letters, digits and _), not hold the token")
	}
	if s.BaseURL != "" {
		u, ok := CheckBaseURL(s.BaseURL)
		if !ok {
			return bad("base_url must be an https URL with a host and no user, query or fragment (http only for localhost or a loopback IP)")
		}
		s.BaseURL = u
	}
	if s.Timeout != "" {
		d, err := time.ParseDuration(s.Timeout)
		if err != nil || d <= 0 || d > maxTimeout {
			return bad("timeout %q: want a duration such as \"30s\", at most %v", s.Timeout, maxTimeout)
		}
	}
	return nil
}

// RequestTimeout is the limit on one request: the timeout setting, else 30 s.
// Call it after [Settings.Validate].
func (s Settings) RequestTimeout() time.Duration {
	if s.Timeout == "" {
		return defaultTimeout
	}
	d, _ := time.ParseDuration(s.Timeout) // validated
	return d
}

// CheckBaseURL accepts an https URL, or an http one whose host is localhost or
// a loopback IP, and returns it without a trailing slash. The token goes
// wherever base_url points, hence https.
func CheckBaseURL(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return "", false
	}
	switch u.Scheme {
	case "https":
	case "http":
		host := u.Hostname()
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return "", false
		}
	default:
		return "", false
	}
	return strings.TrimSuffix(u.String(), "/"), true
}

// RefuseLiteralToken fails raw settings that hold a credential instead of
// naming its environment variable. [toolset.DecodeSettings] would refuse the
// unknown field too, but this says what to do instead and never echoes the
// value. Call it before decoding.
func RefuseLiteralToken(service string, raw json.RawMessage) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return nil // DecodeSettings reports what is wrong with it
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	for name := range fields {
		switch strings.ToLower(name) {
		case "api_key", "apikey", "key", "token", "access_token", "refresh_token", "client_secret":
			return fmt.Errorf("%w: %s: a literal %q is not accepted, because config files get committed; "+
				"put the token in an environment variable and name it in \"access_token_env\" (default %s)",
				toolset.ErrInvalidSettings, service, name, DefaultTokenEnv)
		}
	}
	return nil
}
