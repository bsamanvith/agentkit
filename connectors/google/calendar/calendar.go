package calendar

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/bsamanvith/agentkit/connectors/google/common"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// Type is the toolset type of this connector.
const Type = "calendar"

// DefaultBaseURL is the Google Calendar API v3.
const DefaultBaseURL = "https://www.googleapis.com/calendar/v3"

// The OAuth scopes the tools need. Use [Scopes] to ask for exactly those a
// toolset's settings turn on.
const (
	// ScopeReadOnly reads calendars, events and free/busy.
	ScopeReadOnly = "https://www.googleapis.com/auth/calendar.readonly"
	// ScopeEvents creates, changes and deletes events (and nothing about
	// the calendars themselves).
	ScopeEvents = "https://www.googleapis.com/auth/calendar.events"
)

// Scopes returns the scopes a toolset needs: the read-only scope, and with
// the write tools (read_only false) the events scope too.
func Scopes(readOnly bool) []string {
	if readOnly {
		return []string{ScopeReadOnly}
	}
	return []string{ScopeReadOnly, ScopeEvents}
}

const (
	defaultCalendarID = "primary"
	defaultDescChars  = 4000
	maxDescChars      = 100_000
)

var zonePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+\-]*(/[A-Za-z0-9_+\-]+){0,2}$`)

// Option configures [Factory].
type Option func(*factory)

// WithHTTPClient sets the HTTP client (default: one with no timeout of its
// own; each attempt is limited by the timeout setting).
func WithHTTPClient(c *http.Client) Option { return func(f *factory) { f.env.HTTPClient = c } }

// WithBaseURL sets the API's base URL (default [DefaultBaseURL]; "" keeps
// the default); tests point it at a fake.
func WithBaseURL(u string) Option { return func(f *factory) { f.env.BaseURL = u } }

// WithLookupEnv sets how access_token_env is read; cmd/agentkit passes
// [os.LookupEnv]. Without it every variable reads as unset.
func WithLookupEnv(fn func(string) (string, bool)) Option {
	return func(f *factory) { f.env.LookupEnv = fn }
}

// WithClock sets the clock for retry waits and for the default time window
// (default [clock.Real]).
func WithClock(c clock.Clock) Option { return func(f *factory) { f.env.Clock = c } }

// WithLogger sets the logger (default: discard). It never sees tokens.
func WithLogger(l *slog.Logger) Option { return func(f *factory) { f.env.Logger = l } }

// WithTokenSource sets where access tokens come from, in place of
// access_token_env: the OAuth core hands each user's token to it.
func WithTokenSource(t common.TokenSource) Option { return func(f *factory) { f.env.Tokens = t } }

// Factory returns the "calendar" toolset factory. See the package
// documentation for its settings and tools.
func Factory(opts ...Option) toolset.Factory {
	f := &factory{}
	for _, o := range opts {
		o(f)
	}
	f.env.Defaults()
	return f
}

type factory struct{ env common.Env }

func (*factory) Type() string { return Type }

type settings struct {
	common.Settings
	ReadOnly          *bool  `json:"read_only,omitempty"`
	DefaultCalendarID string `json:"default_calendar_id,omitempty"`
	TimeZone          string `json:"time_zone,omitempty"`
	MaxDescription    int    `json:"max_description_chars,omitempty"`
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
	if s.DefaultCalendarID == "" {
		s.DefaultCalendarID = defaultCalendarID
	}
	if len(s.DefaultCalendarID) > maxIDChars || strings.TrimSpace(s.DefaultCalendarID) == "" {
		return s, fmt.Errorf("%w: %s: default_calendar_id must be a calendar id (an email address, or \"primary\")", toolset.ErrInvalidSettings, Type)
	}
	if s.TimeZone != "" && !zonePattern.MatchString(s.TimeZone) {
		return s, fmt.Errorf("%w: %s: time_zone must be an IANA time zone name such as \"Asia/Kolkata\"", toolset.ErrInvalidSettings, Type)
	}
	if s.MaxDescription == 0 {
		s.MaxDescription = defaultDescChars
	}
	if s.MaxDescription < 1 || s.MaxDescription > maxDescChars {
		return s, fmt.Errorf("%w: %s: max_description_chars must be between 1 and %d", toolset.ErrInvalidSettings, Type, maxDescChars)
	}
	return s, nil
}

func (f *factory) Build(_ context.Context, raw json.RawMessage) ([]tool.Tool, error) {
	s, err := decodeSettings(raw)
	if err != nil {
		return nil, err
	}
	env := f.env // a copy: BaseURL below is per toolset
	tokens, err := env.TokensFor(Type, s.AccessTokenEnv)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", toolset.ErrInvalidSettings, Type, err)
	}
	base := DefaultBaseURL
	switch {
	case s.BaseURL != "":
		base = s.BaseURL
	case env.BaseURL != "":
		u, ok := common.CheckBaseURL(env.BaseURL)
		if !ok {
			return nil, fmt.Errorf("%w: %s: WithBaseURL is not an https URL (http only to localhost)", toolset.ErrInvalidSettings, Type)
		}
		base = u
	}
	c := &calendar{
		api: common.NewClient(common.ClientConfig{
			Service: "google calendar", BaseURL: base, Tokens: tokens, Timeout: s.RequestTimeout(), Env: env,
		}),
		clock:      env.Clock,
		calendarID: s.DefaultCalendarID,
		zone:       s.TimeZone,
		descChars:  s.MaxDescription,
	}
	tools, err := c.tools(s.readOnly())
	if err != nil {
		return nil, err
	}
	env.Logger.Info("calendar toolset built", "read_only", s.readOnly(), "tools", len(tools))
	return tools, nil
}
