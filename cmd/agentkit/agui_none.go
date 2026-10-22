//go:build noagui

package main

import (
	"log/slog"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/component"
)

func newAGUI(*agentapi.Server, *slog.Logger) ([]component.Mount, error) {
	return nil, notBuilt(envComponents, "the AG-UI server", "noagui")
}
