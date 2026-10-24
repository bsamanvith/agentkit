//go:build !noagent

package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/memory"
)

// storeMemory is an agent's context store as the agent API shows it to its
// users (GET and DELETE /v1/agents/{name}/memory).
type storeMemory struct {
	s   memory.Store
	cfg memory.Config // resolved
}

var _ agentapi.Memory = storeMemory{}

func (m storeMemory) Scope() string {
	if m.cfg.Scope == memory.ScopeUser {
		return wire.ScopeUser
	}
	return wire.ScopeTenant
}

func (m storeMemory) View(ctx context.Context, path string) (wire.MemoryView, error) {
	v, err := m.s.View(ctx, path)
	if err != nil {
		return wire.MemoryView{}, memoryAPIError(err)
	}
	out := wire.MemoryView{
		Path: v.Path, Dir: v.Dir, Size: v.Size, Text: v.Text,
		Limits: wire.MemoryLimits{FileBytes: m.cfg.Limits.MaxFileBytes, ScopeBytes: m.cfg.Limits.MaxScopeBytes, Files: m.cfg.Limits.MaxFiles},
	}
	for _, e := range v.Entries {
		out.Entries = append(out.Entries, wire.MemoryEntry{Path: e.Path, Dir: e.Dir, Size: e.Size})
	}
	if out.Usage, err = m.usage(ctx); err != nil {
		return wire.MemoryView{}, memoryAPIError(err)
	}
	return out, nil
}

// usage is the scope's bytes and files.
func (m storeMemory) usage(ctx context.Context) (wire.MemoryUsage, error) {
	files, err := memory.Files(ctx, m.s, memory.Root)
	if err != nil {
		return wire.MemoryUsage{}, err
	}
	u := wire.MemoryUsage{Files: len(files)}
	for _, f := range files {
		u.Bytes += f.Size
	}
	return u, nil
}

func (m storeMemory) Delete(ctx context.Context, path string) error {
	return memoryAPIError(m.s.Delete(ctx, path))
}

// memoryAPIError is a store's error as the agent API's.
func memoryAPIError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, memory.ErrNotFound):
		return fmt.Errorf("%w: %w", agentapi.ErrMemoryNotFound, err)
	case errors.Is(err, memory.ErrInvalidPath):
		return fmt.Errorf("%w: %w", agentapi.ErrMemoryInvalid, err)
	}
	return err
}
