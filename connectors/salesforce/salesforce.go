package salesforce

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/bsamanvith/agentkit/connectors/internal/rest"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// Type is the factory's type, the "type" of a toolset spec.
const Type = "salesforce"

// DefaultTokenEnv is the variable "access_token_env" names by default.
const DefaultTokenEnv = "SALESFORCE_ACCESS_TOKEN"

// DefaultAPIVersion is the REST API version the tools call.
const DefaultAPIVersion = "v61.0"

// Scopes are the OAuth scopes the tools need: the REST API, and a refresh
// token.
var Scopes = []string{"api", "refresh_token"}

// account is what a user connects, as messages name it.
const account = "Salesforce org"

const (
	defaultMaxRecords    = 200
	maxMaxRecords        = 2_000
	defaultMaxValueChars = 4_000
	maxMaxValueChars     = 50_000
)

var versionPattern = regexp.MustCompile(`^v[0-9]{2,3}\.0$`)

// salesforceDomains are the hosts an org's instance_url may be on.
var salesforceDomains = []string{".salesforce.com", ".force.com", ".salesforce.mil"}

// ErrNoInstance reports a token without the org's address: a slot whose
// OAuth2 app does not keep instance_url (use the "salesforce" profile).
var ErrNoInstance = fmt.Errorf("the connected Salesforce token has no instance_url")

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

// WithBaseURL sends every request to u instead of the org's instance_url
// ("" keeps the instance); tests point it at a fake. A base_url setting does
// the same.
func WithBaseURL(u string) Option { return func(e *env) { e.base = u } }

// WithLookupEnv sets how access_token_env is read; cmd/agentkit passes
// [os.LookupEnv].
func WithLookupEnv(f func(string) (string, bool)) Option { return func(e *env) { e.lookup = f } }

// WithClock sets the clock a rate-limit wait uses.
func WithClock(c clock.Clock) Option { return func(e *env) { e.clock = c } }

// WithLogger sets the logger (default: discard). Build logs where the token
// comes from, never the token.
func WithLogger(l *slog.Logger) Option { return func(e *env) { e.log = l } }

// Factory returns the "salesforce" toolset factory.
//
// Settings (all optional):
//
//	{"access_token_env": "SALESFORCE_ACCESS_TOKEN", "instance_url": "", "base_url": "",
//	 "timeout": "30s", "api_version": "v61.0", "read_only": true,
//	 "max_records": 200, "max_value_chars": 4000}
//
// instance_url is the org for the token from access_token_env (with a
// credential slot it comes with each user's token). read_only (default
// true) leaves out every tool that changes something.
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
	InstanceURL   string `json:"instance_url,omitempty"`
	APIVersion    string `json:"api_version,omitempty"`
	ReadOnly      *bool  `json:"read_only,omitempty"`
	MaxRecords    int    `json:"max_records,omitempty"`
	MaxValueChars int    `json:"max_value_chars,omitempty"`
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
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: salesforce: %s", toolset.ErrInvalidSettings, fmt.Sprintf(format, args...))
	}
	if s.InstanceURL != "" {
		u, err := checkInstance(s.InstanceURL)
		if err != nil {
			return s, bad("instance_url: %v", err)
		}
		s.InstanceURL = u
	}
	if s.APIVersion == "" {
		s.APIVersion = DefaultAPIVersion
	}
	if !versionPattern.MatchString(s.APIVersion) {
		return s, bad("api_version %q: want a version such as %q", s.APIVersion, DefaultAPIVersion)
	}
	if s.MaxRecords == 0 {
		s.MaxRecords = defaultMaxRecords
	}
	if s.MaxValueChars == 0 {
		s.MaxValueChars = defaultMaxValueChars
	}
	switch {
	case s.MaxRecords < 1 || s.MaxRecords > maxMaxRecords:
		return s, bad("max_records must be in 1..%d", maxMaxRecords)
	case s.MaxValueChars < 10 || s.MaxValueChars > maxMaxValueChars:
		return s, bad("max_value_chars must be in 10..%d", maxMaxValueChars)
	}
	return s, nil
}

// checkInstance accepts an org's address: https on a Salesforce domain,
// nothing but the host. It returns it without a trailing slash.
func checkInstance(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") != "" || u.Port() != "" {
		return "", fmt.Errorf("want https://<org host> with no path")
	}
	host := strings.ToLower(u.Hostname())
	for _, d := range salesforceDomains {
		if strings.HasSuffix(host, d) {
			return "https://" + host, nil
		}
	}
	return "", fmt.Errorf("host %q is not on a Salesforce domain (%s)", host, strings.Join(salesforceDomains, ", "))
}

func (f *factory) Build(_ context.Context, in toolset.BuildInput) ([]tool.Tool, error) {
	s, err := decodeSettings(in.Settings)
	if err != nil {
		return nil, err
	}
	fixed := s.BaseURL // tests: every request goes here
	if fixed == "" {
		fixed = f.env.base
	}
	sf := &salesforce{
		prefix:        "/services/data/" + s.APIVersion,
		fixed:         fixed,
		maxRecords:    s.MaxRecords,
		maxValueChars: s.MaxValueChars,
		write:         !s.readOnly(),
	}
	cfg := rest.Config{
		Service: Type, Account: account, Timeout: s.RequestTimeout(),
		HTTP: f.env.http, Clock: f.env.clock, Logger: f.env.log,
		ParseError: parseError, RateLimited: rateLimited,
	}
	switch {
	case in.Credentials != nil && (s.TokenEnvSet() || s.InstanceURL != ""):
		return nil, fmt.Errorf("%w: salesforce: with a credential slot the token and instance_url come from each user's connection; leave out access_token_env and instance_url", toolset.ErrInvalidSettings)
	case in.Credentials != nil:
		f.env.log.Info("salesforce toolset auth", "mode", "credential slot")
		sf.slot = in.Credentials
	default:
		tokens, err := rest.EnvTokens(f.env.lookup, s.AccessTokenEnv)
		if err != nil {
			return nil, fmt.Errorf("%w: salesforce: %w", toolset.ErrInvalidSettings, err)
		}
		if s.InstanceURL == "" && fixed == "" {
			return nil, fmt.Errorf("%w: salesforce: instance_url names the org the token in $%s is for", toolset.ErrInvalidSettings, s.AccessTokenEnv)
		}
		f.env.log.Info("salesforce toolset auth", "mode", "environment", "access_token_env", s.AccessTokenEnv)
		cfg.Tokens = tokens
		cfg.BaseURL = s.InstanceURL
	}
	if fixed != "" {
		cfg.BaseURL = fixed
	}
	sf.client = rest.New(cfg)
	return sf.tools()
}

// forCall returns the client of one call: with a slot, the user's token
// sent to the user's org.
func (s *salesforce) forCall(ctx context.Context) (*rest.Client, error) {
	if s.slot == nil {
		return s.client, nil
	}
	tok, err := rest.SlotToken(ctx, s.slot, account)
	if err != nil {
		// The client words these as the model sees them.
		return s.client.With(s.fixed, rest.TokenSourceFunc(func(context.Context) (string, error) { return "", err })), nil
	}
	base := s.fixed
	if base == "" {
		if base, err = instanceOf(tok); err != nil {
			return nil, err
		}
	}
	return s.client.With(base, rest.Static(tok.AccessToken.Reveal())), nil
}

func instanceOf(tok credentials.OAuth2) (string, error) {
	raw := tok.Extra["instance_url"]
	if raw == "" {
		return "", fmt.Errorf("salesforce: %w; connect the org again through an app with the \"salesforce\" profile", ErrNoInstance)
	}
	u, err := checkInstance(raw)
	if err != nil {
		return "", fmt.Errorf("salesforce: the connected token's instance_url is refused: %w", err)
	}
	return u, nil
}

// parseError reads Salesforce's error body, a list:
//
//	[{"message": "...", "errorCode": "INVALID_FIELD", "fields": []}]
func parseError(body []byte) (string, []string) {
	var es []struct {
		Message   string `json:"message"`
		ErrorCode string `json:"errorCode"`
	}
	if json.Unmarshal(body, &es) != nil {
		return "", nil
	}
	var msgs, codes []string
	for _, e := range es {
		msgs = append(msgs, e.Message)
		codes = append(codes, e.ErrorCode)
	}
	return strings.Join(msgs, "; "), codes
}

// rateLimited: Salesforce answers 403 REQUEST_LIMIT_EXCEEDED when the org's
// API calls for the day are spent.
func rateLimited(status int, codes []string) bool {
	if status != http.StatusForbidden {
		return false
	}
	for _, c := range codes {
		if c == "REQUEST_LIMIT_EXCEEDED" {
			return true
		}
	}
	return false
}
