//go:build !nomcp

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/mcp/client"
	"github.com/bsamanvith/agentkit/pkg/mcp/servers"
	"github.com/bsamanvith/agentkit/pkg/netguard"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// mcpClientFactories is the toolset type "mcp": other MCP servers' tools
// (T094).
func mcpClientFactories(logger *slog.Logger) []toolset.Factory {
	return []toolset.Factory{client.Factory(client.WithLookupEnv(os.LookupEnv), client.WithLogger(logger))}
}

// mcpTools lists an MCP server's tools with their fingerprints and prints
// the "pins" setting that allows exactly these definitions.
func (c cli) mcpTools(ctx context.Context, args []string) error {
	fs := c.newFlagSet("agentkit mcp-tools", "<url>")
	transport := fs.String("transport", string(client.TransportAuto), "auto, http or sse")
	var headers headerFlags
	fs.Var(&headers, "header", `a header to send, "Name: value" (repeatable)`)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errUsage
	}
	cl, err := client.Connect(ctx, client.Config{URL: fs.Arg(0), Transport: client.Transport(*transport), Header: http.Header(headers)})
	if err != nil {
		return err
	}
	defer cl.Close()
	defs, err := cl.Tools(ctx)
	if err != nil {
		return err
	}
	pins := map[string]string{}
	w := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	for _, d := range defs {
		fp, err := client.Fingerprint(d)
		if err != nil {
			return fmt.Errorf("tool %q: %w", d.Name, err)
		}
		pins[d.Name] = fp
		desc, _, _ := strings.Cut(client.SanitizeDescription(d.Description), "\n")
		fmt.Fprintf(w, "%s\t%s\t%s\n", d.Name, fp, desc)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(map[string]any{"pins": pins}, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, "\nTo allow exactly these definitions, add to the toolset's settings:\n%s\n", b)
	return nil
}

// headerFlags collect -header "Name: value" flags.
type headerFlags http.Header

func (h *headerFlags) String() string { return "" }

func (h *headerFlags) Set(v string) error {
	name, value, ok := strings.Cut(v, ":")
	if !ok || strings.TrimSpace(name) == "" {
		return errors.New(`want "Name: value"`)
	}
	if *h == nil {
		*h = headerFlags{}
	}
	http.Header(*h).Add(strings.TrimSpace(name), strings.TrimSpace(value))
	return nil
}

// newToolsetAdmin is the keeper of the toolsets added at run time, on st;
// nil when they are off.
func (c cli) newToolsetAdmin(cfg serveConfig, st agentapi.Store, log *slog.Logger) (toolsetAdmin, error) {
	rc := cfg.Runtime
	if !rc.on() {
		return nil, nil
	}
	ts, ok := st.(store.ToolsetStore)
	if !ok {
		return nil, errNoRuntimeStore
	}
	guard, err := netguard.New(rc.AllowHosts)
	if err != nil {
		return nil, fmt.Errorf("runtime_toolsets.allow_hosts: %w", err)
	}
	opts := []servers.Option{
		servers.WithGuard(guard),
		servers.WithHeaderEnv(rc.HeaderEnv, func(name string) (string, bool) { v := c.env(name); return v, v != "" }),
		servers.WithLogger(log.With("component", agentapi.Name)),
	}
	if rc.MaxToolsets < 0 || rc.ConnectTimeout < 0 {
		return nil, errors.New("runtime_toolsets: negative limit")
	}
	if rc.MaxToolsets > 0 {
		opts = append(opts, servers.WithMaxToolsets(rc.MaxToolsets))
	}
	if rc.ConnectTimeout > 0 {
		opts = append(opts, servers.WithConnectTimeout(time.Duration(rc.ConnectTimeout)))
	}
	return servers.New(ts, opts...)
}
