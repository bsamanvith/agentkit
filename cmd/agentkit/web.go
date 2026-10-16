//go:build !noweb

package main

import (
	"github.com/bsamanvith/agentkit/pkg/component"
	"github.com/bsamanvith/agentkit/pkg/web"
)

// newWeb is the web component: the console, embedded in the binary, at /
// (AGENTKIT_COMPONENTS=agent,web). It calls only /v1, on the same origin.
func newWeb() (component.Mount, error) {
	return component.Mount{Pattern: "/", Component: web.New(web.Dist())}, nil
}
