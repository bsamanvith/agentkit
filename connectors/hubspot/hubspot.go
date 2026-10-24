package hubspot

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/bsamanvith/agentkit/connectors/internal/rest"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// Type is the factory's type, the "type" of a toolset spec.
const Type = "hubspot"

// DefaultBaseURL is HubSpot's API host.
const DefaultBaseURL = "https://api.hubapi.com"

// DefaultTokenEnv is the variable "access_token_env" names by default.
const DefaultTokenEnv = "HUBSPOT_ACCESS_TOKEN"

// account is what a user connects, as messages name it.
const account = "HubSpot account"

const (
	defaultMaxValueChars = 4_000
	maxMaxValueChars     = 50_000
)

type env struct {
	http   *http.Client
	base   string
	lookup func(string) (string, bool)
	clock  clock.Clock
	log    *slog.Logger
}

// Option configures [Factory].
type Option func(*env)

// WithHTTPClient sets the client requests go through.
func WithHTTPClient(c *http.Client) Option { return func(e *env) { e.http = c } }

// WithBaseURL sets HubSpot's address (default [DefaultBaseURL]; "" keeps
// the default); tests point it at a fake. A base_url setting overrides it.
func WithBaseURL(u string) Option { return func(e *env) { e.base = u } }

// WithLookupEnv sets how access_token_env is read; cmd/agentkit passes
// [os.LookupEnv].
func WithLookupEnv(f func(string) (string, bool)) Option { return func(e *env) { e.lookup = f } }

// WithClock sets the clock a rate-limit wait and a note's time use.
func WithClock(c clock.Clock) Option { return func(e *env) { e.clock = c } }

// WithLogger sets the logger (default: discard). Build logs where the token
// comes from, never the token.
func WithLogger(l *slog.Logger) Option { return func(e *env) { e.log = l } }

// Factory returns the "hubspot" toolset factory.
//
// Settings (all optional):
//
//	{"access_token_env": "HUBSPOT_ACCESS_TOKEN", "base_url": "", "timeout": "30s",
//	 "read_only": true, "max_value_chars": 4000}
//
// read_only (default true) leaves out every tool that changes something;
// max_value_chars caps each property value shown (at most 50,000).
func Factory(opts ...Option) toolset.Factory {
	e := env{lookup: func(string) (string, bool) { return "", false }, clock: clock.Real(), log: slog.New(slog.DiscardHandler)}
	for _, o := range opts {
		if o != nil {
			o(&e)
		}
	}
	return &factory{env: e}
}

type factory struct{ env env }

func (*factory) Type() string { return Type }

type settings struct {
	rest.Settings
	ReadOnly      *bool `json:"read_only,omitempty"`
	MaxValueChars int   `json:"max_value_chars,omitempty"`
}

func (s settings) readOnly() bool { return s.ReadOnly == nil || *s.ReadOnly }

func decodeSettings(raw json.RawMessage) (settings, error) {
	var s settings
	if err := rest.RefuseLiteralToken(Type, DefaultTokenEnv, raw); err != nil {
		return s, err
	}
	if err := toolset.DecodeSettings(raw, &s); err != nil {
		return s, err
	}
	if err := s.Validate(Type, DefaultTokenEnv); err != nil {
		return s, err
	}
	if s.MaxValueChars == 0 {
		s.MaxValueChars = defaultMaxValueChars
	}
	if s.MaxValueChars < 10 || s.MaxValueChars > maxMaxValueChars {
		return s, fmt.Errorf("%w: hubspot: max_value_chars must be in 10..%d", toolset.ErrInvalidSettings, maxMaxValueChars)
	}
	return s, nil
}

// Scopes returns the OAuth scopes the tools of a configuration need, so
// that the connect flow asks for exactly those. raw is the toolset's
// settings. HubSpot asks every app for "oauth"; tickets have one scope for
// reading and writing.
func Scopes(raw json.RawMessage) ([]string, error) {
	s, err := decodeSettings(raw)
	if err != nil {
		return nil, err
	}
	out := []string{"oauth", "tickets"}
	for _, o := range []string{"contacts", "companies", "deals"} {
		out = append(out, "crm.objects."+o+".read")
		if !s.readOnly() {
			out = append(out, "crm.objects."+o+".write")
		}
	}
	return out, nil
}

func (f *factory) Build(_ context.Context, in toolset.BuildInput) ([]tool.Tool, error) {
	s, err := decodeSettings(in.Settings)
	if err != nil {
		return nil, err
	}
	var tokens rest.TokenSource
	switch {
	case in.Credentials != nil && s.TokenEnvSet():
		return nil, fmt.Errorf("%w: hubspot: access_token_env and the toolset's credential slot are two places for the token: give one", toolset.ErrInvalidSettings)
	case in.Credentials != nil:
		f.env.log.Info("hubspot toolset auth", "mode", "credential slot")
		tokens = rest.FromSource(in.Credentials, account)
	default:
		if tokens, err = rest.EnvTokens(f.env.lookup, s.AccessTokenEnv); err != nil {
			return nil, fmt.Errorf("%w: hubspot: %w", toolset.ErrInvalidSettings, err)
		}
		f.env.log.Info("hubspot toolset auth", "mode", "environment", "access_token_env", s.AccessTokenEnv)
	}
	base := DefaultBaseURL
	switch {
	case s.BaseURL != "":
		base = s.BaseURL
	case f.env.base != "":
		base = f.env.base
	}
	h := &hubspot{
		client: rest.New(rest.Config{
			Service: Type, Account: account, BaseURL: base, Tokens: tokens, Timeout: s.RequestTimeout(),
			HTTP: f.env.http, Clock: f.env.clock, Logger: f.env.log, ParseError: parseError,
		}),
		clock:         f.env.clock,
		maxValueChars: s.MaxValueChars,
		write:         !s.readOnly(),
	}
	return h.tools()
}

// parseError reads HubSpot's error body:
//
//	{"status": "error", "message": "...", "category": "VALIDATION_ERROR",
//	 "errors": [{"message": "...", "code": "INVALID_EMAIL"}]}
func parseError(body []byte) (string, []string) {
	var e struct {
		Message  string `json:"message"`
		Category string `json:"category"`
		Errors   []struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &e) != nil {
		return "", nil
	}
	reasons := []string{e.Category}
	msg := e.Message
	for _, d := range e.Errors {
		reasons = append(reasons, d.Code)
		if d.Message != "" && d.Message != msg {
			msg += "; " + d.Message
		}
	}
	return msg, reasons
}
