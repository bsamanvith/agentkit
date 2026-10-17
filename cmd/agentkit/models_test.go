//go:build !nomodels

package main

import (
	"testing"

	"github.com/bsamanvith/agentkit/pkg/llm/anthropic"
)

func TestDefaultModelIsTheAdapters(t *testing.T) {
	t.Parallel()
	if defaultModelID != anthropic.DefaultModel {
		t.Errorf("defaultModelID = %q, want anthropic.DefaultModel %q", defaultModelID, anthropic.DefaultModel)
	}
}
