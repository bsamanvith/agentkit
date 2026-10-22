package main

import (
	"fmt"
	"strings"
	"time"
)

// Environment of background runs (T071).
const (
	// envWebhookSecret is the master secret completion webhooks are signed
	// with: each tenant's secret is derived from it (webhook.DeriveSecret),
	// so every replica signs alike and nothing is stored. Set, it turns
	// webhooks on; at least 32 bytes.
	envWebhookSecret = "AGENTKIT_WEBHOOK_SECRET"
	// envWebhookAllowHosts lists, comma-separated, hosts webhooks may reach
	// although they resolve to a private or loopback address (package
	// netguard); every other private address is refused.
	envWebhookAllowHosts = "AGENTKIT_WEBHOOK_ALLOW_HOSTS"
	// envRecoveryInterval is how often the recovery loop looks for
	// background runs a process left (default 30s).
	envRecoveryInterval = "AGENTKIT_RECOVERY_INTERVAL"
)

// notifyConfig is an agent's default webhook ("notify" of an agent).
type notifyConfig struct {
	URL string `json:"url"`
}

// minWebhookSecret is the shortest master secret envWebhookSecret takes.
const minWebhookSecret = 32

// webhookEnv reads the environment of background runs into e. The
// webhooks themselves are the agent component's (webhooks_agent.go, left
// out with -tags noagent).
func (c cli) webhookEnv(e *serveEnv) error {
	if v := c.env(envWebhookSecret); v != "" {
		if len(v) < minWebhookSecret {
			return fmt.Errorf("$%s: the secret must be at least %d bytes", envWebhookSecret, minWebhookSecret)
		}
		e.webhookSecret = []byte(v)
	}
	for h := range strings.SplitSeq(c.env(envWebhookAllowHosts), ",") {
		if h = strings.TrimSpace(h); h != "" {
			e.webhookAllow = append(e.webhookAllow, h)
		}
	}
	if len(e.webhookAllow) > 0 && e.webhookSecret == nil {
		return fmt.Errorf("$%s is set, and webhooks are off: set $%s", envWebhookAllowHosts, envWebhookSecret)
	}
	if v := c.env(envRecoveryInterval); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 100*time.Millisecond {
			return fmt.Errorf("%s: %q is not a duration of 100ms or more", envRecoveryInterval, v)
		}
		e.recoveryInterval = d
	}
	return nil
}
