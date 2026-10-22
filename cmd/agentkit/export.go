package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
)

// agentsDir is the folder of an export that holds the agents, one
// <name>.json each, in the shape of an entry of "agents" in the serve
// config file.
const agentsDir = "agents"

// exportCmd writes the published version of each agent the caller's tenant
// stored through the API (T075) to <dir>/agents/<name>.json. The agents of
// the server's config file are not exported: they are in that file.
func (c cli) exportCmd(ctx context.Context, args []string) int {
	const cmd = "agentkit export"
	fs := c.newFlagSet(cmd, "<dir>")
	var f clientFlags
	f.registerServer(fs, c)
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		if err == nil {
			fs.Usage()
		}
		return exitClient
	}
	dir := filepath.Join(fs.Arg(0), agentsDir)
	client, err := c.client(f)
	if err != nil {
		fmt.Fprintln(c.stderr, cmd+":", err)
		return exitClient
	}
	agents, err := client.Agents(ctx)
	if err != nil {
		return c.requestFailed(ctx, cmd, err, "")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(c.stderr, cmd+":", err)
		return exitClient
	}
	n := 0
	for _, a := range agents {
		if a.Version == 0 { // an agent of the config file
			continue
		}
		v, err := client.AgentVersion(ctx, a.Name, a.Version)
		if err != nil {
			return c.requestFailed(ctx, cmd, err, "")
		}
		var out bytes.Buffer
		if err := json.Indent(&out, v.Definition, "", "  "); err != nil {
			fmt.Fprintf(c.stderr, "%s: agent %s: %v\n", cmd, a.Name, err)
			return exitClient
		}
		out.WriteByte('\n')
		path := filepath.Join(dir, a.Name+".json")
		if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
			fmt.Fprintln(c.stderr, cmd+":", err)
			return exitClient
		}
		fmt.Fprintf(c.stdout, "exported %s (version %d) to %s\n", a.Name, a.Version, path)
		n++
	}
	if n == 0 {
		fmt.Fprintln(c.stdout, "no stored agents to export")
	}
	return exitDone
}

// importCmd saves each <dir>/agents/<name>.json as a new version of the
// agent <name>, published unless -draft. A file the server refuses is
// reported with its fields, and the others are still imported; the exit
// code is then exitClient.
func (c cli) importCmd(ctx context.Context, args []string) int {
	const cmd = "agentkit import"
	fs := c.newFlagSet(cmd, "<dir>")
	var f clientFlags
	f.registerServer(fs, c)
	draft := fs.Bool("draft", false, "save each agent as a draft, not published")
	message := fs.String("message", "imported", "why, for each agent's version list")
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		if err == nil {
			fs.Usage()
		}
		return exitClient
	}
	files, err := filepath.Glob(filepath.Join(fs.Arg(0), agentsDir, "*.json"))
	if err != nil {
		fmt.Fprintln(c.stderr, cmd+":", err)
		return exitClient
	}
	if len(files) == 0 {
		fmt.Fprintf(c.stderr, "%s: no agents in %s\n", cmd, filepath.Join(fs.Arg(0), agentsDir))
		return exitClient
	}
	slices.Sort(files)
	client, err := c.client(f)
	if err != nil {
		fmt.Fprintln(c.stderr, cmd+":", err)
		return exitClient
	}
	code := exitDone
	for _, path := range files {
		name := strings.TrimSuffix(filepath.Base(path), ".json")
		def, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(c.stderr, cmd+":", err)
			code = exitClient
			continue
		}
		v, err := client.SaveAgent(ctx, name, wire.SaveAgentRequest{Definition: def, Message: *message, Publish: !*draft})
		if err != nil {
			if ctx.Err() != nil {
				return c.requestFailed(ctx, cmd, err, "")
			}
			c.importFailed(cmd, path, err)
			code = exitClient
			continue
		}
		state := "published"
		if !v.Published {
			state = "draft"
		}
		fmt.Fprintf(c.stdout, "imported %s: version %d, %s\n", name, v.Version, state)
	}
	return code
}

// importFailed reports a file the server refused, field by field.
func (c cli) importFailed(cmd, path string, err error) {
	var api *agentclient.APIError
	if !errors.As(err, &api) || len(api.Fields) == 0 {
		fmt.Fprintf(c.stderr, "%s: %s: %s\n", cmd, path, withTokenHint(err))
		return
	}
	fmt.Fprintf(c.stderr, "%s: %s: refused:\n", cmd, path)
	for _, fe := range api.Fields {
		field := fe.Field
		if field == "" {
			field = "(definition)"
		}
		fmt.Fprintf(c.stderr, "  %s: %s\n", field, fe.Message)
	}
}
