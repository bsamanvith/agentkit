//go:build !noagent

package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/bsamanvith/agentkit/pkg/memory"
)

// memoryCmd runs "agentkit memory reembed": for each agent of the config
// whose memory.index has an embedder, it embeds again every item whose
// vector is missing or was made by another model or dimension (after a
// change of embedder, or of an index written without one), and prints how
// many. Nothing else ever re-embeds: until this runs, semantic and hybrid
// searches of such an index fail with memory.ErrEmbedderMismatch. It is
// safe while servers use the index, and to run again.
func (c cli) memoryCmd(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "reembed" {
		fmt.Fprintln(c.stderr, "usage: agentkit memory reembed [-config file] [-agent name]")
		return errUsage
	}
	fs := c.newFlagSet("agentkit memory reembed", "")
	configPath := fs.String("config", c.env(envConfig), "config file (JSON), as serve's (default $"+envConfig+")")
	only := fs.String("agent", "", "re-embed this agent's index only (default: every agent with an embedder)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return errUsage
	}
	cfg, err := loadServeConfig(*configPath)
	if err != nil {
		return err
	}
	found := false
	for _, a := range cfg.Agents {
		if *only != "" && a.Name != *only {
			continue
		}
		label := a.label(cfg.legacy) + ".memory.index"
		if a.Memory == nil || a.Memory.Index == nil || a.Memory.Index.Embedder == nil {
			if *only != "" {
				return fmt.Errorf("%s: no embedder to re-embed with", label)
			}
			continue
		}
		found = true
		if err := c.reembed(ctx, label, *a.Memory.Index); err != nil {
			return err
		}
	}
	if !found {
		if *only != "" {
			return fmt.Errorf("no agent %q in the config", *only)
		}
		return errors.New("no agent of the config has memory.index.embedder")
	}
	return nil
}

func (c cli) reembed(ctx context.Context, label string, ix memoryIndexConfig) error {
	open, ok := memoryIndexBackends()[ix.Backend]
	if !ok {
		return fmt.Errorf("%s.backend %q: unknown", label, ix.Backend)
	}
	cfg, err := indexConfig(label, ix, c.env)
	if err != nil {
		return err
	}
	x, closeFn, err := open(ctx, ix, cfg)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	defer func() { _ = closeOrNil(closeFn) }()
	re, ok := x.(memory.Reembedder)
	if !ok {
		fmt.Fprintf(c.stdout, "%s: backend %q keeps nothing to re-embed\n", label, ix.Backend)
		return nil
	}
	n, err := re.Reembed(ctx)
	name, dim := cfg.Embedder.Model()
	if err != nil {
		return fmt.Errorf("%s: re-embedded %d items with %s, then: %w", label, n, name, err)
	}
	fmt.Fprintf(c.stdout, "%s: %d items re-embedded with %s (%d dimensions)\n", label, n, name, dim)
	return nil
}
