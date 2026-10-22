package gmail

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/bsamanvith/agentkit/connectors/google/common"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// Type is the factory's type, the "type" of a toolset spec.
const Type = "gmail"

// DefaultBaseURL is the Gmail API's address.
const DefaultBaseURL = "https://gmail.googleapis.com"

// OAuth scopes the tools need (the connect flow asks for the ones the
// toolset's settings use; see [Scopes]).
const (
	ScopeReadonly = "https://www.googleapis.com/auth/gmail.readonly"
	ScopeModify   = "https://www.googleapis.com/auth/gmail.modify"
	ScopeCompose  = "https://www.googleapis.com/auth/gmail.compose"
	ScopeSend     = "https://www.googleapis.com/auth/gmail.send"
)

const (
	defaultBodyChars = 8000
	maxBodyChars     = 100_000
)

// Option configures [Factory].
type Option func(*common.Env)

// WithHTTPClient sets the client requests go through (default: a client on
// [http.DefaultTransport], which honors the proxy environment variables).
func WithHTTPClient(c *http.Client) Option { return func(e *common.Env) { e.HTTPClient = c } }

// WithBaseURL sets the API's address (default [DefaultBaseURL]; "" keeps the
// default); tests point it at a fake. A base_url setting overrides it for its
// toolset.
func WithBaseURL(u string) Option { return func(e *common.Env) { e.BaseURL = u } }

// WithLookupEnv sets how the token's environment variable is read (default:
// none is ever set). cmd/agentkit passes [os.LookupEnv].
func WithLookupEnv(lookup func(string) (string, bool)) Option {
	return func(e *common.Env) { e.LookupEnv = lookup }
}

// WithClock sets the clock the wait before a retry uses (default
// [clock.Real]).
func WithClock(c clock.Clock) Option { return func(e *common.Env) { e.Clock = c } }

// WithLogger sets the logger (default: discard). Build logs where each
// toolset's token comes from at info, and requests at debug; never a token.
func WithLogger(l *slog.Logger) Option { return func(e *common.Env) { e.Logger = l } }

// WithTokenSource sets where the access token of the user the tools act for
// comes from. Without it the toolset reads the token from the environment
// variable its settings name.
func WithTokenSource(s common.TokenSource) Option { return func(e *common.Env) { e.Tokens = s } }

// Factory returns the "gmail" toolset factory. See the package documentation
// for its tools and settings.
func Factory(opts ...Option) toolset.Factory {
	f := &factory{}
	for _, o := range opts {
		o(&f.env)
	}
	f.env.Defaults()
	return f
}

type factory struct{ env common.Env }

func (*factory) Type() string { return Type }

type settings struct {
	common.Settings
	AllowSend    bool  `json:"allow_send"`
	AllowModify  *bool `json:"allow_modify"`
	MaxBodyChars int   `json:"max_body_chars"`
}

func (s settings) modifyAllowed() bool { return s.AllowModify == nil || *s.AllowModify }

func decodeSettings(raw json.RawMessage) (settings, error) {
	var s settings
	if err := common.RefuseLiteralToken(Type, raw); err != nil {
		return s, err
	}
	if err := toolset.DecodeSettings(raw, &s); err != nil {
		return s, err
	}
	if err := s.Validate(Type); err != nil {
		return s, err
	}
	if s.MaxBodyChars == 0 {
		s.MaxBodyChars = defaultBodyChars
	}
	if s.MaxBodyChars < 100 || s.MaxBodyChars > maxBodyChars {
		return s, fmt.Errorf("%w: gmail: max_body_chars %d: want 100..%d", toolset.ErrInvalidSettings, s.MaxBodyChars, maxBodyChars)
	}
	return s, nil
}

// Scopes returns the OAuth scopes the tools a toolset with these settings
// builds need, so that the connect flow asks for no more.
func Scopes(raw json.RawMessage) ([]string, error) {
	s, err := decodeSettings(raw)
	if err != nil {
		return nil, err
	}
	scopes := []string{ScopeReadonly, ScopeCompose}
	if s.modifyAllowed() {
		scopes = append(scopes, ScopeModify)
	}
	if s.AllowSend {
		scopes = append(scopes, ScopeSend)
	}
	return scopes, nil
}

func (f *factory) Build(_ context.Context, in toolset.BuildInput) ([]tool.Tool, error) {
	raw := in.Settings
	s, err := decodeSettings(raw)
	if err != nil {
		return nil, err
	}
	env := f.env // the toolset's own copy: settings override the base URL
	tokens, err := env.TokensFor(Type, s.AccessTokenEnv)
	if err != nil {
		return nil, fmt.Errorf("%w: gmail: %w", toolset.ErrInvalidSettings, err)
	}
	base := DefaultBaseURL
	if env.BaseURL != "" {
		base = env.BaseURL
	}
	if s.BaseURL != "" {
		base = s.BaseURL
	}
	c := common.NewClient(common.ClientConfig{
		Service: Type, BaseURL: base, Tokens: tokens, Timeout: s.RequestTimeout(), Env: env,
	})
	return newTools(&api{c: c, bodyChars: s.MaxBodyChars}, s)
}
