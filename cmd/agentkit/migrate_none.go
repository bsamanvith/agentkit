//go:build noagent

package main

import "context"

// migrate, in a build without the agent component, has no store to migrate:
// the image that serves the agent API (agentkit-api) runs it.
func (c cli) migrate(context.Context, []string) error {
	return notBuilt("agentkit migrate", "the agent API's store", "noagent")
}
