package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
)

const toolsetsUsage = `usage: agentkit toolsets <command> [flags] [args]

  list                                  the server's toolsets: from its config, and added at run time
  add -name <name> [flags] <url>        add an MCP server as a toolset; its tools are off until approved
  review <name>                         a toolset's tools: approved, new, changed (and how), removed, rejected
  approve <name> [tool...]              approve the tools as the review shows them (default: every new and changed one)
  reject <name> <tool>...               turn tools down; they stay off until the server changes them
  enable <name> | disable <name>        turn a toolset added at run time on or off
  delete <name>                         remove a toolset added at run time

Each command takes -server (default $AGENTKIT_SERVER) and -json; all but list
need a key with the scope agent:admin in $AGENTKIT_API_TOKEN.
`

// toolsetsCmd manages the toolsets admins add at run time: MCP servers, by URL
// (T096).
func (c cli) toolsetsCmd(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(c.stderr, toolsetsUsage)
		return exitClient
	}
	verb, args := args[0], args[1:]
	cmd := "agentkit toolsets " + verb
	operands := map[string]string{
		"list": "", "add": "<url>", "review": "<name>", "approve": "<name> [tool...]", "reject": "<name> <tool>...",
		"enable": "<name>", "disable": "<name>", "delete": "<name>",
	}
	operand, ok := operands[verb]
	if !ok {
		fmt.Fprint(c.stderr, toolsetsUsage)
		return exitClient
	}
	fs := c.newFlagSet(cmd, operand)
	server := c.env(envServer)
	if server == "" {
		server = defaultServer
	}
	fs.StringVar(&server, "server", server, "the agent API's base URL, from $"+envServer+" if set")
	asJSON := fs.Bool("json", false, "print the server's answer as JSON")
	var add wire.AddToolsetRequest
	var headers headerList
	if verb == "add" {
		fs.StringVar(&add.Name, "name", "", "the toolset's name, which prefixes its tools (<name>__<tool>)")
		fs.StringVar(&add.Transport, "transport", "", "auto (default), http or sse")
		fs.Var(&headers, "header", `a header to send, "Name: value" (repeatable); a secret must be "${VAR}", a variable the server allows`)
	}
	if err := fs.Parse(args); err != nil {
		return exitClient
	}
	n := fs.NArg()
	if (verb == "list" && n != 0) || (verb == "approve" && n < 1) || (verb == "reject" && n < 2) ||
		(!slices.Contains([]string{"list", "approve", "reject"}, verb) && n != 1) || (verb == "add" && add.Name == "") {
		fs.Usage()
		return exitClient
	}
	client, err := c.client(clientFlags{server: server})
	if err != nil {
		fmt.Fprintln(c.stderr, cmd+":", err)
		return exitClient
	}
	var out any
	switch verb {
	case "list":
		out, err = client.Toolsets(ctx)
	case "add":
		add.Type, add.URL = "mcp", fs.Arg(0)
		if len(headers) > 0 {
			add.Auth, add.Headers = wire.AuthHeaders, map[string]string(headers)
		}
		out, err = client.AddToolset(ctx, add)
	case "review":
		out, err = client.ToolsetReview(ctx, fs.Arg(0))
	case "approve", "reject":
		out, err = c.decide(ctx, client, verb, fs.Arg(0), fs.Args()[1:])
	case "enable", "disable":
		on := verb == "enable"
		out, err = client.UpdateToolset(ctx, fs.Arg(0), wire.UpdateToolsetRequest{Enabled: &on})
	case "delete":
		err = client.DeleteToolset(ctx, fs.Arg(0))
	}
	if err != nil {
		return c.requestFailed(ctx, cmd, err, "")
	}
	switch {
	case out == nil:
		_, err = fmt.Fprintf(c.stdout, "deleted %s\n", fs.Arg(0))
	case *asJSON:
		enc := json.NewEncoder(c.stdout)
		enc.SetIndent("", "  ")
		err = enc.Encode(out)
	default:
		err = printToolsets(c.stdout, out)
	}
	if err != nil {
		fmt.Fprintln(c.stderr, cmd+": writing stdout:", err)
		return exitClient
	}
	return exitDone
}

// decide approves or rejects the named tools (approve with none: every new
// and changed one) at the fingerprints a fresh review shows, so what is
// approved is what the server lists now; a tool that changes in between
// fails with a conflict.
func (c cli) decide(ctx context.Context, client *agentclient.Client, verb, name string, tools []string) (wire.ServerToolset, error) {
	ts, err := client.ToolsetReview(ctx, name)
	if err != nil {
		return ts, err
	}
	decision := wire.DecisionApprove
	if verb == "reject" {
		decision = wire.DecisionReject
	}
	var req wire.ReviewRequest
	for _, t := range ts.Tools {
		pending := t.Status == wire.ToolNew || t.Status == wire.ToolChanged
		if (len(tools) == 0 && pending) || slices.Contains(tools, t.Name) {
			req.Tools = append(req.Tools, wire.ToolDecision{Name: t.Name, Fingerprint: t.Fingerprint, Decision: decision})
		}
	}
	for _, want := range tools {
		if !slices.ContainsFunc(req.Tools, func(d wire.ToolDecision) bool { return d.Name == want }) {
			return ts, fmt.Errorf("toolset %s has no tool %q", name, want)
		}
	}
	if len(req.Tools) == 0 {
		return ts, fmt.Errorf("toolset %s has no new or changed tool to %s", name, verb)
	}
	return client.ReviewToolset(ctx, name, req)
}

// printToolsets prints a toolset list, or a toolset with its review.
func printToolsets(w io.Writer, v any) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	switch v := v.(type) {
	case []wire.Toolset:
		fmt.Fprintln(tw, "NAME\tTYPE\tSTATUS\tTOOLS\tSOURCE")
		for _, ts := range v {
			source := "config"
			if ts.Runtime {
				source = "added"
			}
			status := ts.Status
			if ts.Reason != "" {
				status += " (" + ts.Reason + ")"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n", ts.Name, ts.Type, status, ts.ToolCount, source)
		}
	case wire.ServerToolset:
		status := v.Status
		if v.Reason != "" {
			status += ": " + v.Reason
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", v.Name, v.URL, status)
		if v.Server.Name != "" {
			fmt.Fprintf(tw, "server\t%s %s\t\n", v.Server.Name, v.Server.Version)
		}
		fmt.Fprintln(tw, "\nTOOL\tSTATUS\tCLASS\tDESCRIPTION")
		for _, t := range v.Tools {
			status := t.Status
			if len(t.Changes) > 0 {
				status += " (" + strings.Join(t.Changes, ", ") + ")"
			}
			desc, _, _ := strings.Cut(t.Description, "\n")
			fmt.Fprintf(tw, "%s\t%s\t%s/%s\t%s\n", t.Name, status, t.Kind, t.Risk, desc)
		}
		if v.Status == wire.ToolsetReviewNeeded {
			fmt.Fprintf(tw, "\nNew and changed tools are off. Check them with -json, then: agentkit toolsets approve %s\n", v.Name)
		}
	}
	return tw.Flush()
}

// headerList collects -header "Name: value" flags for a request body.
type headerList map[string]string

func (h *headerList) String() string { return "" }

func (h *headerList) Set(v string) error {
	name, value, ok := strings.Cut(v, ":")
	if !ok || strings.TrimSpace(name) == "" {
		return fmt.Errorf(`%q is not "Name: value"`, v)
	}
	if *h == nil {
		*h = headerList{}
	}
	(*h)[http.CanonicalHeaderKey(strings.TrimSpace(name))] = strings.TrimSpace(value)
	return nil
}

var _ flag.Value = (*headerList)(nil)
