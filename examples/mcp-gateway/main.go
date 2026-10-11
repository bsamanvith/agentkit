// Command mcp-gateway serves several toolsets from one process and one port,
// one MCP endpoint per toolset: the shape of a real agentkit deployment, using
// the built-in demo connector so it needs no accounts.
//
//	go run ./examples/mcp-gateway                  # uses the embedded config.json
//	go run ./examples/mcp-gateway -config my.json
//
// With the shipped config, three endpoints come up:
//
//	http://127.0.0.1:8080/mcp/demo-eu     echo, current_time, calculate ([eu] prefix, UTC+1)
//	http://127.0.0.1:8080/mcp/demo-ist    echo, current_time, calculate ([ist] prefix, UTC+5:30)
//	http://127.0.0.1:8080/mcp/demo-maths  calculate only
//	http://127.0.0.1:8080/mcp/            lists the toolsets
//
// Add one to Claude Code with:
//
//	claude mcp add --transport http demo-eu http://127.0.0.1:8080/mcp/demo-eu
//
// This is what cmd/agentkit will do from its config: build a catalog of the
// compiled-in connectors, toolset.Build the configured toolsets, and mount
// server.New at the base path.
package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/bsamanvith/agentkit/pkg/mcp/server"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

//go:embed config.json
var defaultConfig []byte

type config struct {
	Addr string `json:"addr"`
	MCP  struct {
		BasePath string         `json:"base_path"`
		Toolsets []toolset.Spec `json:"toolsets"`
	} `json:"mcp"`
}

func main() {
	configPath := flag.String("config", "", "config file (default: the embedded config.json)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	err := run(ctx, *configPath)
	stop()
	if err != nil {
		slog.Error("mcp-gateway", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, configPath string) error {
	raw := defaultConfig
	if configPath != "" {
		var err error
		if raw, err = os.ReadFile(configPath); err != nil {
			return err
		}
	}
	cfg, err := decodeConfig(raw)
	if err != nil {
		return err
	}
	handler, err := newHandler(ctx, cfg)
	if err != nil {
		return err
	}

	srv := &http.Server{Addr: cfg.Addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	slog.Info("serving", "addr", cfg.Addr, "base_path", cfg.MCP.BasePath)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func decodeConfig(raw []byte) (config, error) {
	var cfg config
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return config{}, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}

// newHandler is the whole wiring: catalog -> toolset.Build -> server.New.
func newHandler(ctx context.Context, cfg config) (http.Handler, error) {
	// The catalog is where the binary says which connectors are compiled in.
	catalog, err := toolset.NewCatalog(toolset.Demo())
	if err != nil {
		return nil, err
	}
	registries, problems, err := toolset.Build(ctx, catalog, cfg.MCP.Toolsets)
	if err != nil {
		return nil, err
	}
	for _, p := range problems {
		slog.Warn("toolset skipped", "toolset", p.Toolset, "err", p.Err)
	}
	opts := []server.Option{}
	if cfg.MCP.BasePath != "" {
		opts = append(opts, server.WithBasePath(cfg.MCP.BasePath))
	}
	return server.New(registries, opts...)
}
