//go:build nomodels && !nogemini

package main

import "github.com/bsamanvith/agentkit/pkg/llm"

var newGemini = func(modelFlags) (llm.Model, error) { return nil, errNoModels }
