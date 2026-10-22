//go:build noagent

package main

import "context"

// memoryCmd, in a build without the agent component, has no memory to
// re-embed: the image that serves the agent API runs it.
func (c cli) memoryCmd(context.Context, []string) error {
	return notBuilt("agentkit memory", "the agent component's memory", "noagent")
}
