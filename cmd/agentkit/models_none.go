//go:build nomodels

package main

import (
	"errors"

	"github.com/bsamanvith/agentkit/pkg/llm"
)

var errNoModels = errors.New("this agentkit is built without model adapters (-tags nomodels): run agents on a runner ($" + envRunnerURL + "), or use an image that has them")

func newClaude(modelFlags) (llm.Model, error) { return nil, errNoModels }

func newOpenAI(modelFlags) (llm.Model, error) { return nil, errNoModels }
