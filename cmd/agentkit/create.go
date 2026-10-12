package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/bsamanvith/agentkit/pkg/agentclient"
)

// create creates an empty conversation and prints its ID, or with -json the
// conversation: for a script or client that needs the ID before the first
// message, which agentkit chat or ask -conversation ID then sends.
func (c cli) create(ctx context.Context, args []string) int {
	fs := c.newFlagSet("agentkit create", "")
	var f clientFlags
	f.registerServer(fs, c)
	asJSON := fs.Bool("json", false, "print the conversation as JSON, instead of its ID")
	if err := fs.Parse(args); err != nil {
		return exitClient
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return exitClient
	}
	client, err := c.client(f)
	if err != nil {
		fmt.Fprintln(c.stderr, "agentkit create:", err)
		return exitClient
	}
	conv, err := client.CreateConversation(ctx, agentclient.ConversationRequest{Agent: f.agent})
	switch {
	case err == nil:
	case ctx.Err() != nil:
		fmt.Fprintln(c.stderr, "agentkit create: interrupted")
		return exitInterrupted
	case errors.Is(err, agentclient.ErrUnauthorized):
		fmt.Fprintf(c.stderr, "agentkit create: %v (set $%s to the server's API token)\n", err, envAPIToken)
		return exitClient
	default:
		fmt.Fprintln(c.stderr, "agentkit create:", err)
		return exitClient
	}
	out := conv.ID
	if *asJSON {
		b, err := json.Marshal(conv)
		if err != nil { // cannot happen: the conversation was just decoded
			fmt.Fprintln(c.stderr, "agentkit create:", err)
			return exitClient
		}
		out = string(b)
	}
	if _, err := fmt.Fprintln(c.stdout, out); err != nil {
		fmt.Fprintln(c.stderr, "agentkit create: writing stdout:", err)
		return exitClient
	}
	return exitDone
}
