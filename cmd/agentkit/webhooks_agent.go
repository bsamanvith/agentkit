//go:build !noagent

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/netguard"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
	"github.com/bsamanvith/agentkit/pkg/webhook"
)

// webhooks are the agent API's webhooks of env: nil when they are off.
func webhooks(env serveEnv) (*agentapi.Webhooks, *netguard.Guard, error) {
	if env.webhookSecret == nil {
		return nil, nil, nil
	}
	guard, err := netguard.New(env.webhookAllow)
	if err != nil {
		return nil, nil, fmt.Errorf("$%s: %w", envWebhookAllowHosts, err)
	}
	master := env.webhookSecret
	return &agentapi.Webhooks{
		Secret: func(t tenancy.Tenant) (string, error) {
			return webhook.EncodeSecret(webhook.DeriveSecret(master, t.ID())), nil
		},
		CheckURL: func(raw string) error {
			_, err := guard.CheckURL(raw)
			return err
		},
	}, guard, nil
}

// runNotifier delivers the completion webhooks of every tenant's runs until
// ctx is done; it returns at once when webhooks are off.
func runNotifier(ctx context.Context, env serveEnv, admin store.Admin, agents *agentapi.Server, log *slog.Logger) error {
	if env.webhookSecret == nil {
		return nil
	}
	_, guard, err := webhooks(env)
	if err != nil {
		return err
	}
	master := env.webhookSecret
	host, _ := os.Hostname()
	n, err := webhook.New(admin, agents.WebhookPayload,
		func(t tenancy.Tenant) ([]byte, error) { return webhook.DeriveSecret(master, t.ID()), nil },
		webhook.WithClient(guard.Client(nil)),
		webhook.WithOwner(fmt.Sprintf("%s-%d-webhooks", host, os.Getpid())),
		webhook.WithLogger(log),
	)
	if err != nil {
		return err
	}
	return n.Run(ctx, func(r webhook.Report, err error) {
		switch {
		case err != nil:
			log.Error("webhook pass", "delivered", r.Delivered, "retrying", r.Retrying, "failed", r.Failed, "err", err)
		case r.Delivered+r.Retrying+r.Failed > 0:
			log.Info("webhook pass", "delivered", r.Delivered, "retrying", r.Retrying, "failed", r.Failed)
		}
	})
}
