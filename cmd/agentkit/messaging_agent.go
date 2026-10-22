//go:build !noagent

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentmsg"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// Agent messaging (T073, package agentmsg): an agent whose config has
// can_message gets the messaging tools, every run learns its sender through
// the agent API's run context, and serve runs a dispatcher that delivers
// the messages of every tenant. With no can_message anywhere, none of it is
// built.

// newMessenger is agent messaging over st for cfg's agents, or nil when no
// agent may message another.
func newMessenger(cfg serveConfig, st agentapi.Store, clk clock.Clock) (*agentmsg.Messenger, error) {
	var agents []agentmsg.Agent
	on := false
	for _, a := range cfg.Agents {
		agents = append(agents, agentmsg.Agent{
			Name: a.Name, Description: a.Description, CanMessage: a.CanMessage,
			Toolsets: slices.Concat(a.Toolsets, a.RuntimeToolsets),
		})
		on = on || len(a.CanMessage) > 0
	}
	if !on {
		return nil, nil
	}
	mc := cfg.API.Messaging
	if mc.MaxHops < 0 || mc.MaxTokens < 0 || mc.MaxDuration < 0 || mc.SendEvery < 0 || mc.SendBurst < 0 {
		return nil, errors.New("agent_api.messaging: negative limit")
	}
	m, err := agentmsg.New(st, agents, agentmsg.WithClock(clk), agentmsg.WithLimits(agentmsg.Limits{
		MaxHops: mc.MaxHops, MaxTokens: mc.MaxTokens, MaxDuration: time.Duration(mc.MaxDuration),
		SendEvery: time.Duration(mc.SendEvery), SendBurst: mc.SendBurst,
	}))
	if err != nil {
		return nil, fmt.Errorf("agents[].can_message: %w", err)
	}
	return m, nil
}

// messagingTools is the registry of agent's messaging tools: nil without
// messaging, or for an agent that may message nobody.
func messagingTools(m *agentmsg.Messenger, agent string) (tool.Registry, error) {
	if m == nil {
		return nil, nil
	}
	tools, err := m.Tools(agent)
	if err != nil || len(tools) == 0 {
		return nil, err
	}
	return tool.NewRegistry(tools)
}

// messagingRunContext is the agent API option that tells the messaging
// tools who sends.
func messagingRunContext() agentapi.Option {
	return agentapi.WithRunContext(func(ctx context.Context, rc agentapi.RunContext) context.Context {
		return agentmsg.WithSender(ctx, rc.Agent, rc.ConversationID)
	})
}

// runMessaging delivers the messages of every tenant until ctx is done:
// one dispatcher per process, sharing the work through leases.
func runMessaging(ctx context.Context, m *agentmsg.Messenger, admin store.Admin, srv *agentapi.Server, log *slog.Logger) error {
	ma, ok := admin.(store.MessageAdmin)
	if !ok {
		return errors.New("the store cannot deliver messages between agents across tenants")
	}
	host, _ := os.Hostname()
	d, err := m.Dispatcher(ma, srv, agentmsg.WithOwner(fmt.Sprintf("%s-%d-messages", host, os.Getpid())), agentmsg.WithLogger(log))
	if err != nil {
		return err
	}
	return d.Run(ctx)
}
