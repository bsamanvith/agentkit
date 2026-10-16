//go:build noweb

package main

import "github.com/bsamanvith/agentkit/pkg/component"

func newWeb() (component.Mount, error) {
	return component.Mount{}, notBuilt(envComponents, "the console", "noweb")
}
