//go:build noa2a || noconnectors

package main

import (
	"log/slog"

	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// a2aClientFactories, in a build without A2A or without the catalog of
// connectors, is empty: config naming type "a2a" fails at startup with
// toolset.ErrUnknownType.
func a2aClientFactories(*slog.Logger) []toolset.Factory { return nil }
