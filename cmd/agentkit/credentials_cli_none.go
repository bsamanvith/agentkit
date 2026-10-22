//go:build noagent

package main

import (
	"context"
	"fmt"
)

// credentialsCmd, in a build without the agent component (noagent: the
// runner and toolsets images), refuses: credentials are put in where the
// agent API runs, which opens the vault.
func (c cli) credentialsCmd(context.Context, []string) int {
	fmt.Fprintln(c.stderr, "agentkit:", notBuilt("agentkit credentials", "the agent API's credentials", "noagent"))
	return 1
}
