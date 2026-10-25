//go:build !noconnectors && !noagent

package main

import (
	"encoding/json"
	"log/slog"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/connectors/google/calendar"
	"github.com/bsamanvith/agentkit/connectors/google/drive"
	"github.com/bsamanvith/agentkit/connectors/google/gmail"
	"github.com/bsamanvith/agentkit/connectors/google/sheets"
	"github.com/bsamanvith/agentkit/connectors/hubspot"
	"github.com/bsamanvith/agentkit/connectors/salesforce"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/credentials/memory"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// localConnectors are the account connectors deploy/local/agent/search.json
// serves, each with the scopes its default settings need.
var localConnectors = map[string]func(json.RawMessage) ([]string, error){
	"gmail": gmail.Scopes,
	"calendar": func(raw json.RawMessage) ([]string, error) {
		var s struct {
			ReadOnly *bool `json:"read_only"`
		}
		err := json.Unmarshal(raw, &s)
		return calendar.Scopes(s.ReadOnly == nil || *s.ReadOnly), err
	},
	"drive":      drive.Scopes,
	"sheets":     sheets.Scopes,
	"hubspot":    hubspot.Scopes,
	"salesforce": func(json.RawMessage) ([]string, error) { return salesforce.Scopes, nil },
}

// TestLocalConfigServesConnectors: the local config (scripts/try-agent.sh)
// and the deployed one (Kubernetes and Compose, its URLs paths on
// $AGENTKIT_PUBLIC_URL) serve every connector optional with a per-user
// slot, and an assistant that has them all behind tool search. With no
// provider apps set up each still starts, and lists each slot as one this
// server can't connect; with them, every slot can be connected, asking for
// the scopes its toolset's settings need and coming back to the server's
// callback.
func TestLocalConfigServesConnectors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, path, publicURL string }{
		{"local", filepath.Join("..", "..", "deploy", "local", "agent", "search.json"), "http://localhost:8080"},
		{"deployed", filepath.Join("..", "..", "deploy", "k8s", "base", "config.json"), "http://localhost:30080"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := loadServeConfig(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "deployed" {
				cfg.Credentials = cfg.Credentials.withPublicURL(tc.publicURL)
			}
			servesConnectors(t, cfg, tc.publicURL)
		})
	}
}

// servesConnectors checks cfg as TestLocalConfigServesConnectors says, for
// a server at publicURL.
func servesConnectors(t *testing.T, cfg serveConfig, publicURL string) {
	t.Helper()
	specs := map[string]toolset.Spec{}
	for _, s := range cfg.MCP.Toolsets {
		specs[s.Name] = s
	}
	for _, name := range []string{"gmail", "calendar", "drive", "sheets", "hubspot", "salesforce", "exa", "parallel"} {
		s, ok := specs[name]
		if !ok || s.Type != name || !s.Optional || s.Credential == nil || s.Credential.Slot != name || s.Credential.Scope != credentials.ScopeUser {
			t.Errorf("toolset %s = %+v, want type %[1]s, optional, user slot %[1]s", name, s)
		}
	}
	i := slices.IndexFunc(cfg.Agents, func(a agentDef) bool { return a.Name == "assistant" })
	if i < 0 {
		t.Fatal("no assistant agent")
	}
	if a := cfg.Agents[i]; !a.DeferLoading.all || len(a.Toolsets) != 8 {
		t.Errorf("assistant: toolsets %v, defer_loading %+v; want all the connectors, every tool deferred", a.Toolsets, a.DeferLoading)
	}

	none := cli{getenv: func(string) string { return "" }}
	if apps, err := none.oauth2Apps(cfg.Credentials); err != nil || len(apps) != 0 {
		t.Errorf("no provider apps: %+v, %v; want none and no error", apps, err)
	}
	if _, err := none.credentialSources(nil, cfg.Credentials, slog.New(slog.DiscardHandler)); err != nil {
		t.Errorf("no vault: %v", err)
	}

	set := cli{getenv: func(k string) string {
		if strings.HasSuffix(k, "_OAUTH_CLIENT_ID") || strings.HasSuffix(k, "_OAUTH_CLIENT_SECRET") {
			return "x-" + k
		}
		return ""
	}}
	store := memory.New()
	sealer := credentials.NewSealer(testRing(t))
	for name, withApps := range map[string]cli{"without apps": none, "with apps": set} {
		parts := &vaultParts{vault: credentials.NewVault(store, sealer), store: store, sealer: sealer}
		svc, err := withApps.newConnect(cfg, cfg.MCP.Toolsets, serveEnv{vaultParts: parts}, nil, slog.New(slog.DiscardHandler))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		tn, err := tenancy.NewTenant("t1")
		if err != nil {
			t.Fatal(err)
		}
		ctx := tenancy.WithUser(tenancy.With(t.Context(), tn), "u1")
		list, err := svc.List(ctx, "u1")
		if err != nil {
			t.Fatal(err)
		}
		for slot, scopesOf := range localConnectors {
			j := slices.IndexFunc(list, func(c wire.CredentialInfo) bool { return c.Slot == slot })
			if j < 0 {
				t.Errorf("%s: slot %s not listed", name, slot)
				continue
			}
			if list[j].OAuth != (name == "with apps") {
				t.Errorf("%s: slot %s oauth = %v", name, slot, list[j].OAuth)
			}
			if name == "without apps" {
				continue
			}
			res, _, err := svc.Connect(ctx, "u1", slot, wire.ConnectRequest{ReturnURL: publicURL + "/connections/done"})
			if err != nil {
				t.Errorf("connect %s: %v", slot, err)
				continue
			}
			u, err := url.Parse(res.AuthorizeURL)
			if err != nil {
				t.Fatal(err)
			}
			settings := specs[slot].Settings
			if settings == nil {
				settings = json.RawMessage(`{}`)
			}
			want, err := scopesOf(settings)
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Fields(u.Query().Get("scope"))
			for _, s := range want {
				if !slices.Contains(got, s) {
					t.Errorf("connect %s asks for %v, missing %s its tools need", slot, got, s)
				}
			}
			provider := map[string]string{"hubspot": "HUBSPOT", "salesforce": "SALESFORCE"}[slot]
			if provider == "" {
				provider = "GOOGLE"
			}
			if u.Query().Get("client_id") != "x-"+provider+"_OAUTH_CLIENT_ID" {
				t.Errorf("connect %s: client_id %q", slot, u.Query().Get("client_id"))
			}
			if got := u.Query().Get("redirect_uri"); got != publicURL+"/oauth/callback" {
				t.Errorf("connect %s: redirect_uri %q, want %s/oauth/callback", slot, got, publicURL)
			}
		}
	}
}
