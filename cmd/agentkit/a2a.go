//go:build !noa2a

package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/a2a"
	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/component"
)

// a2aDirectoryName names the component that serves the well-known Agent
// Card beside the a2a component's /a2a/.
const a2aDirectoryName = a2aName + "-directory"

// newA2A is the a2a component (AGENTKIT_COMPONENTS=agent,a2a): every
// agent of the agent API served as an A2A agent at /a2a/{agent}, and the
// default agent's card at /.well-known/agent-card.json. It calls the agent
// API in process with each request's own credential, so the API's keys,
// tokens, tenants, scopes and agent visibility apply as they do on /v1.
//
// Its mounts go before the agent API's: the runtime shuts components down
// in reverse order, so the agent API drains the runs that A2A follows
// before A2A stops following them.
func (c cli) newA2A(agents *agentapi.Server, cfg serveConfig, authn auth.Authenticator, log *slog.Logger) ([]component.Mount, error) {
	if agents == nil {
		return nil, fmt.Errorf("needs the %s component in the same process ($%s)", agentapi.Name, envComponents)
	}
	opts := []a2a.Option{a2a.WithVersion(version), a2a.WithLogger(log.With("component", a2a.Name))}
	if v := c.env(envA2AURL); v != "" {
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || (u.Path != "" && u.Path != "/") {
			return nil, fmt.Errorf("$%s: %q is not an origin such as https://agents.example.com", envA2AURL, v)
		}
		opts = append(opts, a2a.WithPublicURL(v))
	}
	if schemes := a2aSchemes(cfg, authn); schemes != nil {
		opts = append(opts, a2a.WithSecuritySchemes(schemes))
	}
	srv, err := a2a.New(agents.Handler(), opts...)
	if err != nil {
		return nil, err
	}
	return []component.Mount{
		{Pattern: a2a.PathPrefix, Component: srv},
		{Pattern: a2a.WellKnownPath, Component: a2aDirectory{srv.Handler()}},
	}, nil
}

// a2aSchemes are the security schemes the cards name: the bearer
// credential the agent API takes (an API key, or a token of the identity
// provider), and the provider's discovery document when JWTs are on. Nil
// when the server takes no credentials.
func a2aSchemes(cfg serveConfig, authn auth.Authenticator) map[string]a2a.SecurityScheme {
	if authn == nil {
		return nil
	}
	s := map[string]a2a.SecurityScheme{"bearer": {HTTPAuth: &a2a.HTTPAuthSecurityScheme{
		Scheme: "Bearer", Description: "An agentkit API key, or a token of the server's identity provider.",
	}}}
	if j := cfg.Auth.JWT; j != nil && j.Issuer != "" {
		s["oidc"] = a2a.SecurityScheme{OpenIDConnect: &a2a.OpenIDConnectSecurityScheme{
			OpenIDConnectURL: strings.TrimRight(j.Issuer, "/") + "/.well-known/openid-configuration",
		}}
	}
	return s
}

// a2aDirectory serves the well-known card: the a2a component's handler at
// a second pattern, with nothing of its own to ready or shut down.
type a2aDirectory struct{ h http.Handler }

func (d a2aDirectory) Name() string                   { return a2aDirectoryName }
func (d a2aDirectory) Handler() http.Handler          { return d.h }
func (d a2aDirectory) Ready(context.Context) error    { return nil }
func (d a2aDirectory) Shutdown(context.Context) error { return nil }
