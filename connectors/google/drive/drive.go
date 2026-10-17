package drive

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
const Type = "drive"

// DefaultBaseURL is Google's API host; Drive's paths, uploads included, hang
// under it.
const DefaultBaseURL = "https://www.googleapis.com"

// The OAuth scopes the tools need ([Scopes] picks them for a configuration).
const (
	ScopeReadonly = "https://www.googleapis.com/auth/drive.readonly"
	ScopeFile     = "https://www.googleapis.com/auth/drive.file"
	Scope         = "https://www.googleapis.com/auth/drive"
)

const (
	defaultContentChars  = 20_000
	maxContentChars      = 200_000
	defaultDownloadBytes = 5 << 20
	maxDownloadBytes     = common.MaxBodyBytes
)

// Option configures [Factory].
type Option func(*common.Env)

// WithHTTPClient sets the client requests go through.
func WithHTTPClient(c *http.Client) Option { return func(e *common.Env) { e.HTTPClient = c } }

// WithBaseURL sets Google's address (default [DefaultBaseURL]; "" keeps the
// default); tests point it at a fake. A base_url setting overrides it.
func WithBaseURL(u string) Option { return func(e *common.Env) { e.BaseURL = u } }

// WithLookupEnv sets how access_token_env is read; cmd/agentkit passes
// [os.LookupEnv].
func WithLookupEnv(f func(string) (string, bool)) Option {
	return func(e *common.Env) { e.LookupEnv = f }
}

// WithClock sets the clock a rate-limit wait uses.
func WithClock(c clock.Clock) Option { return func(e *common.Env) { e.Clock = c } }

// WithLogger sets the logger (default: discard). Build logs where the token
// comes from, never the token.
func WithLogger(l *slog.Logger) Option { return func(e *common.Env) { e.Logger = l } }

// WithTokenSource sets where the acting user's access token comes from. It
// takes the place of the environment variable.
func WithTokenSource(ts common.TokenSource) Option { return func(e *common.Env) { e.Tokens = ts } }

// Factory returns the "drive" toolset factory.
//
// Settings (all optional):
//
//	{"access_token_env": "GOOGLE_ACCESS_TOKEN", "base_url": "", "timeout": "30s",
//	 "read_only": true, "allow_share": false, "narrow_scopes": false,
//	 "max_content_chars": 20000, "max_download_bytes": 5242880}
//
// read_only (default true) leaves out every tool that changes something;
// allow_share adds drive_share_file and needs read_only false; narrow_scopes
// asks for drive.file instead of drive when the write tools are on.
// max_content_chars caps the text drive_read_file returns (at most 200,000);
// max_download_bytes is the largest file it downloads or exports (at most
// 8 MiB).
func Factory(opts ...Option) toolset.Factory {
	var env common.Env
	for _, o := range opts {
		if o != nil {
			o(&env)
		}
	}
	env.Defaults()
	return &factory{env: env}
}

type factory struct{ env common.Env }

func (*factory) Type() string { return Type }

type settings struct {
	common.Settings
	ReadOnly         *bool `json:"read_only,omitempty"`
	AllowShare       bool  `json:"allow_share,omitempty"`
	NarrowScopes     bool  `json:"narrow_scopes,omitempty"`
	MaxContentChars  int   `json:"max_content_chars,omitempty"`
	MaxDownloadBytes int   `json:"max_download_bytes,omitempty"`
}

func (s settings) readOnly() bool { return s.ReadOnly == nil || *s.ReadOnly }

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
	bad := func(format string, args ...any) (settings, error) {
		return s, fmt.Errorf("%w: drive: %s", toolset.ErrInvalidSettings, fmt.Sprintf(format, args...))
	}
	if s.MaxContentChars == 0 {
		s.MaxContentChars = defaultContentChars
	}
	if s.MaxDownloadBytes == 0 {
		s.MaxDownloadBytes = defaultDownloadBytes
	}
	switch {
	case s.MaxContentChars < 100 || s.MaxContentChars > maxContentChars:
		return bad("max_content_chars must be in 100..%d", maxContentChars)
	case s.MaxDownloadBytes < 1 || s.MaxDownloadBytes > maxDownloadBytes:
		return bad("max_download_bytes must be in 1..%d", maxDownloadBytes)
	case s.AllowShare && s.readOnly():
		return bad("allow_share needs \"read_only\": false")
	}
	return s, nil
}

// Scopes returns the OAuth scopes the tools of a configuration need, so that
// the connect flow asks for exactly those. raw is the toolset's settings.
func Scopes(raw json.RawMessage) ([]string, error) {
	s, err := decodeSettings(raw)
	if err != nil {
		return nil, err
	}
	switch {
	case s.readOnly():
		return []string{ScopeReadonly}, nil
	case s.NarrowScopes:
		return []string{ScopeReadonly, ScopeFile}, nil
	}
	return []string{Scope}, nil
}

func (f *factory) Build(_ context.Context, raw json.RawMessage) ([]tool.Tool, error) {
	s, err := decodeSettings(raw)
	if err != nil {
		return nil, err
	}
	env := f.env // options are shared by every toolset, so copy
	tokens, err := env.TokensFor(Type, s.AccessTokenEnv)
	if err != nil {
		return nil, fmt.Errorf("%w: drive: %w", toolset.ErrInvalidSettings, err)
	}
	base := DefaultBaseURL
	switch {
	case s.BaseURL != "":
		base = s.BaseURL
	case env.BaseURL != "":
		base = env.BaseURL
	}
	d := &drive{
		client: common.NewClient(common.ClientConfig{
			Service: Type, BaseURL: base, Tokens: tokens, Timeout: s.RequestTimeout(), Env: env,
		}),
		maxChars:    s.MaxContentChars,
		maxDownload: s.MaxDownloadBytes,
		write:       !s.readOnly(),
		share:       s.AllowShare,
	}
	return d.tools()
}
