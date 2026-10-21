//go:build nomodels

package main

import (
	"context"
	"fmt"

	"github.com/bsamanvith/agentkit/pkg/tool"
)

// addSkills fails for an agent that names skill folders: a build without
// models runs no harness, so it leaves pkg/skill out (skills_load.go).
func addSkills(_ context.Context, label, system string, tools tool.Registry, sources []skillSource) (string, tool.Registry, agentSkills, error) {
	if len(sources) > 0 {
		return "", nil, agentSkills{}, fmt.Errorf("%s.skills: this agentkit is built without model adapters (-tags nomodels), so it loads no skills: run agents on a runner ($%s), or use an image that has them", label, envRunnerURL)
	}
	return system, tools, agentSkills{}, nil
}
