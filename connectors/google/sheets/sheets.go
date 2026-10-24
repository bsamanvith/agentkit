package sheets

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
const Type = "sheets"

// DefaultBaseURL is the Sheets API's host.
const DefaultBaseURL = "https://sheets.googleapis.com"

// The OAuth scopes the tools need ([Scopes] picks them for a configuration).
const (
	ScopeReadonly = "https://www.googleapis.com/auth/spreadsheets.readonly"
	Scope         = "https://www.googleapis.com/auth/spreadsheets"
)

const (
	defaultMaxCells     = 2_000
	maxMaxCells         = 20_000
	defaultMaxCellChars = 5_000
	maxMaxCellChars     = 50_000
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

// Factory returns the "sheets" toolset factory.
//
// Settings (all optional):
//
//	{"access_token_env": "GOOGLE_ACCESS_TOKEN", "base_url": "", "timeout": "30s",
//	 "read_only": true, "max_cells": 2000, "max_cell_chars": 5000}
//
// read_only (default true) leaves out every tool that changes something;
// max_cells caps the cells sheets_read_range returns (at most 20,000), and
// max_cell_chars the characters of each (at most 50,000).
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
	ReadOnly     *bool `json:"read_only,omitempty"`
	MaxCells     int   `json:"max_cells,omitempty"`
	MaxCellChars int   `json:"max_cell_chars,omitempty"`
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
	if s.MaxCells == 0 {
		s.MaxCells = defaultMaxCells
	}
	if s.MaxCellChars == 0 {
		s.MaxCellChars = defaultMaxCellChars
	}
	switch {
	case s.MaxCells < 10 || s.MaxCells > maxMaxCells:
		return s, fmt.Errorf("%w: sheets: max_cells must be in 10..%d", toolset.ErrInvalidSettings, maxMaxCells)
	case s.MaxCellChars < 10 || s.MaxCellChars > maxMaxCellChars:
		return s, fmt.Errorf("%w: sheets: max_cell_chars must be in 10..%d", toolset.ErrInvalidSettings, maxMaxCellChars)
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
	if s.readOnly() {
		return []string{ScopeReadonly}, nil
	}
	return []string{Scope}, nil
}

func (f *factory) Build(_ context.Context, in toolset.BuildInput) ([]tool.Tool, error) {
	s, err := decodeSettings(in.Settings)
	if err != nil {
		return nil, err
	}
	env := f.env // options are shared by every toolset, so copy
	tokens, err := env.TokensForSpec(Type, s.Settings, in.Credentials)
	if err != nil {
		return nil, fmt.Errorf("%w: sheets: %w", toolset.ErrInvalidSettings, err)
	}
	base := DefaultBaseURL
	switch {
	case s.BaseURL != "":
		base = s.BaseURL
	case env.BaseURL != "":
		base = env.BaseURL
	}
	sh := &sheets{
		client: common.NewClient(common.ClientConfig{
			Service: Type, BaseURL: base, Tokens: tokens, Timeout: s.RequestTimeout(), Env: env,
		}),
		maxCells:     s.MaxCells,
		maxCellChars: s.MaxCellChars,
		write:        !s.readOnly(),
	}
	return sh.tools()
}
