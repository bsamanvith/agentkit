//go:build !noagent

package main

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"

	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/credentials/connect"
	"github.com/bsamanvith/agentkit/pkg/credentials/credrpc"
	"github.com/bsamanvith/agentkit/pkg/netguard"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// newConnect is the agent API's credentials (GET, PUT and DELETE
// /v1/credentials, and connecting accounts) over the vault, for the slots
// the served toolsets name; nil when there is no vault. The slots of
// credentials.oauth2 with an auth_url can be connected; an entry for a
// slot no served toolset uses is left out. dyn, when not nil, adds the
// toolsets added at run time with auth "oauth" (T095), each its own slot.
func (c cli) newConnect(cfg serveConfig, specs []toolset.Spec, env serveEnv, dyn connect.Dynamic, log *slog.Logger) (*connect.Service, error) {
	if env.vaultParts == nil {
		return nil, nil
	}
	var slots []connect.Slot
	used := map[string]bool{}
	for _, s := range specs {
		if s.Credential == nil {
			continue
		}
		slots = append(slots, connect.Slot{Name: s.Credential.Slot, Scope: cmp.Or(s.Credential.Scope, credentials.ScopeTenant), Toolsets: []string{s.Name}})
		used[s.Credential.Slot] = true
	}
	all, err := c.oauth2Apps(cfg.Credentials)
	if err != nil {
		return nil, err
	}
	apps := map[string]connect.App{}
	for _, a := range all {
		switch {
		case a.authURL == "":
		case !used[a.slot]:
			log.Warn("credentials.oauth2: no toolset served here uses the slot; its accounts cannot be connected here", "slot", a.slot)
		default:
			apps[a.slot] = connect.App{
				ClientID: a.app.ClientID, ClientSecret: a.app.ClientSecret, AuthStyle: a.app.AuthStyle,
				AuthURL: a.authURL, TokenURL: a.app.TokenURL, RedirectURL: a.redirectURL,
				Scopes: slices.Clone(a.scope), AuthParams: a.params,
				KeepExtras: a.app.KeepExtras, DefaultTTL: a.app.DefaultTTL,
			}
		}
	}
	guard, err := netguard.New(cfg.Credentials.AllowHosts)
	if err != nil {
		return nil, fmt.Errorf("credentials.allow_hosts: %w", err)
	}
	svc, err := connect.New(connect.Config{
		Vault: env.vaultParts.vault, Store: env.vaultParts.store, Sealer: env.vaultParts.sealer,
		Slots: slots, Apps: apps, Dynamic: dyn, ReturnURLs: cfg.Credentials.ReturnURLs,
		CheckTokenURL: func(raw string) error { _, err := guard.CheckURL(raw); return err },
		Logger:        log.With("component", "credentials"),
	})
	if err != nil {
		return nil, fmt.Errorf("credentials: %w", err)
	}
	return svc, nil
}

// oauthCallback is the component that serves the providers' redirects at
// /oauth/callback: public (the state names the flow), always ready,
// nothing to shut down.
type oauthCallback struct{ h http.Handler }

func (o oauthCallback) Name() string                   { return "oauth-callback" }
func (o oauthCallback) Handler() http.Handler          { return o.h }
func (o oauthCallback) Ready(context.Context) error    { return nil }
func (o oauthCallback) Shutdown(context.Context) error { return nil }

// oauthClientMetadata serves the MCP OAuth client's metadata document:
// public, always ready, nothing to shut down.
type oauthClientMetadata struct{ h http.Handler }

func (o oauthClientMetadata) Name() string                   { return "oauth-client-metadata" }
func (o oauthClientMetadata) Handler() http.Handler          { return o.h }
func (o oauthClientMetadata) Ready(context.Context) error    { return nil }
func (o oauthClientMetadata) Shutdown(context.Context) error { return nil }

// credentialsServer serves the vault's sources to a toolsets component
// elsewhere ($AGENTKIT_TOOLSETS_URL), for the slots the served toolsets
// name (package credrpc): the agent API refreshes, the toolsets component
// gets fresh tokens. nil when the toolsets run here or there is no vault.
func (c cli) credentialsServer(cfg serveConfig, specs []toolset.Spec, env serveEnv, log *slog.Logger) (*rpcComponent, error) {
	if env.vault == nil || env.toolsetsURL == "" {
		return nil, nil
	}
	slots := map[string]credentials.Scope{}
	for _, s := range specs {
		if s.Credential != nil {
			slots[s.Credential.Slot] = cmp.Or(s.Credential.Scope, credentials.ScopeTenant)
		}
	}
	if len(slots) == 0 {
		return nil, nil
	}
	sources, err := c.credentialSources(env.vault, cfg.Credentials, log)
	if err != nil {
		return nil, err
	}
	srv, err := newRPCServer(credentialsService, env, log)
	if err != nil {
		return nil, err
	}
	if err := credrpc.Register(srv, sources, slots); err != nil {
		return nil, err
	}
	return &rpcComponent{name: credentialsService, server: srv}, nil
}
