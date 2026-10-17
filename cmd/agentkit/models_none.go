//go:build nomodels

package main

import (
	"errors"

	"github.com/bsamanvith/agentkit/pkg/llm"
)

func newClaude(modelFlags) (llm.Model, error) {
	return nil, errors.New("this agentkit is built without model adapters (-tags nomodels): run agents on a runner ($" + envRunnerURL + "), or use an image that has them")
}
