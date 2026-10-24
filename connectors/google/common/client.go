package common

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/bsamanvith/agentkit/connectors/internal/rest"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/credentials"
)

const (
	// MaxBodyBytes is the largest response body the client reads.
	MaxBodyBytes = rest.MaxBodyBytes
)

// Env is what a Google connector's factory options set, and what it builds a
// [Client] from. The connector's options (WithHTTPClient, WithBaseURL, ...)
// fill it in; [Env.Defaults] fills the rest.
type Env struct {
	HTTPClient *http.Client
	BaseURL    string // "" is the connector's default
	LookupEnv  func(string) (string, bool)
	Clock      clock.Clock
	Logger     *slog.Logger
	// Tokens, if set, supplies the access token of the user the tools act
	// for. Without it a connector reads the token from the environment.
	Tokens TokenSource
}

// Defaults fills in what the options left out, so a nil option means the
// default rather than a nil that a toolset would dereference.
func (e *Env) Defaults() {
	if e.HTTPClient == nil {
		e.HTTPClient = &http.Client{}
	}
	if e.LookupEnv == nil {
		e.LookupEnv = func(string) (string, bool) { return "", false }
	}
	if e.Clock == nil {
		e.Clock = clock.Real()
	}
	if e.Logger == nil {
		e.Logger = slog.New(slog.DiscardHandler)
	}
}

// TokensFor returns the source for a toolset: the injected one, else the
// environment variable envName. It logs which, never the token.
func (e *Env) TokensFor(service, envName string) (TokenSource, error) {
	if e.Tokens != nil {
		e.Logger.Info(service+" toolset auth", "mode", "token source")
		return e.Tokens, nil
	}
	src, err := EnvTokens(e.LookupEnv, envName)
	if err != nil {
		return nil, err
	}
	e.Logger.Info(service+" toolset auth", "mode", "environment", "access_token_env", envName)
	return src, nil
}

// TokensForSpec returns the source for a toolset built from s (after
// [Settings.Validate]) and src, the credential slot its spec names (nil for
// none): the slot's per-user token when there is one, else the injected
// source or the environment variable, as [Env.TokensFor]. A slot and
// access_token_env together are two places for one token, and refused.
func (e *Env) TokensForSpec(service string, s Settings, src credentials.Source) (TokenSource, error) {
	if src == nil {
		return e.TokensFor(service, s.AccessTokenEnv)
	}
	if s.tokenEnvSet {
		return nil, fmt.Errorf("access_token_env and the toolset's credential slot are two places for the token: give one")
	}
	e.Logger.Info(service+" toolset auth", "mode", "credential slot")
	return FromSource(src), nil
}

// Client sends requests to one Google API: the shared token connectors'
// client (connectors/internal/rest), reading Google's error bodies. It is
// shared by a toolset's tools and is safe for concurrent use.
type Client = rest.Client

// ClientConfig configures [NewClient].
type ClientConfig struct {
	// Service names the API in logs and error messages: "gmail".
	Service string
	// BaseURL is the API's address, without a trailing slash.
	BaseURL string
	Tokens  TokenSource
	// Timeout limits one request (zero is 30 s).
	Timeout time.Duration
	Env     Env
}

// NewClient returns a client for cfg. The env must have had its defaults set.
func NewClient(cfg ClientConfig) *Client {
	return rest.New(rest.Config{
		Service:     cfg.Service,
		Account:     account,
		BaseURL:     cfg.BaseURL,
		Tokens:      cfg.Tokens,
		Timeout:     cfg.Timeout,
		HTTP:        cfg.Env.HTTPClient,
		Clock:       cfg.Env.Clock,
		Logger:      cfg.Env.Logger,
		ParseError:  parseError,
		RateLimited: isRateLimited,
	})
}

// account is what a user connects, as messages name it.
const account = "Google account"

// Request is one call to the API.
type Request = rest.Request

// Path joins path segments under a prefix with each segment escaped, so an id
// from the model cannot reach another endpoint: Path("/gmail/v1/users/me/messages", id).
func Path(prefix string, segments ...string) string { return rest.Path(prefix, segments...) }
