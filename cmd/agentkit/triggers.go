//go:build !noagent

package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
	"github.com/bsamanvith/agentkit/pkg/trigger"
	"github.com/bsamanvith/agentkit/pkg/webhook"
)

// triggerOptions are the agent API's triggers (T072) on st, with admin to
// find them across tenants: none when the store keeps no triggers. Inbound
// webhooks are on with completion webhooks ($AGENTKIT_WEBHOOK_SECRET): each
// trigger's signing secret is derived from the master secret, its tenant
// and its webhook's ID, so nothing is stored and a new webhook ID (PATCH
// with rotate) is a new secret.
func triggerOptions(env serveEnv, st agentapi.Store, admin store.TriggerAdmin) []agentapi.Option {
	ts, ok := st.(store.TriggerStore)
	if !ok || admin == nil {
		return nil
	}
	tc := agentapi.Triggers{Store: ts}
	if master := env.webhookSecret; master != nil {
		tc.ByHook = admin.TriggerByHook
		tc.Secret = func(t tenancy.Tenant, hookID string) ([]byte, error) {
			return hookSecret(master, t, hookID), nil
		}
	}
	return []agentapi.Option{agentapi.WithTriggers(tc)}
}

// hookSecret is the signing secret of the inbound webhook hookID of tenant
// t: derived as completion webhooks' are, under a name no tenant has (a
// tenant ID has no NUL).
func hookSecret(master []byte, t tenancy.Tenant, hookID string) []byte {
	return webhook.DeriveSecret(master, "trigger\x00"+t.ID()+"\x00"+hookID)
}

// staticTriggers are the triggers of the config's agents, in the default
// tenant.
func staticTriggers(cfg serveConfig) []agentapi.StaticTrigger {
	var out []agentapi.StaticTrigger
	for _, a := range cfg.Agents {
		for _, t := range a.Triggers {
			out = append(out, agentapi.StaticTrigger{ID: t.ID, Definition: trigger.Definition{
				Agent: a.Name, Prompt: t.Prompt, Schedule: t.Schedule, Webhook: t.Webhook,
			}})
		}
	}
	return out
}

// syncTriggers stores the config's triggers (agentapi.Server.SyncTriggers).
func syncTriggers(ctx context.Context, cfg serveConfig, srv *agentapi.Server) error {
	if err := srv.SyncTriggers(ctx, staticTriggers(cfg)); err != nil {
		return fmt.Errorf("triggers: %w", err)
	}
	return nil
}

// runScheduler fires the due schedules of every tenant until ctx is done:
// one scheduler per process, the store's claims making each firing happen
// once across replicas. admin is nil when the server has no triggers: it
// returns at once.
func runScheduler(ctx context.Context, admin store.TriggerAdmin, agents *agentapi.Server, log *slog.Logger) error {
	if admin == nil {
		return nil
	}
	s, err := trigger.NewScheduler(admin, agents.FireDue)
	if err != nil {
		return err
	}
	return s.Run(ctx, func(fired int, err error) {
		if err != nil {
			log.Error("trigger pass", "fired", fired, "err", err)
			return
		}
		log.Info("trigger pass", "fired", fired)
	})
}
