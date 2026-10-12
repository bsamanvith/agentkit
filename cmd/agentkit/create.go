package main

import (
	"context"
	"encoding/json"
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
	if err != nil {
		return c.requestFailed(ctx, "agentkit create", err, "")
	}
	if *asJSON {
		err = json.NewEncoder(c.stdout).Encode(conv)
	} else {
		_, err = fmt.Fprintln(c.stdout, conv.ID)
	}
	if err != nil {
		fmt.Fprintln(c.stderr, "agentkit create: writing stdout:", err)
		return exitClient
	}
	return exitDone
}
