package main

// The model adapters serve can build. They are in their own file, which the
// nomodels tag leaves out, so an image whose agents run on a remote runner
// (agentkit-api) links no model SDK:
//
//	nomodels  no Anthropic adapter (models_anthropic.go); agent-api needs
//	          $AGENTKIT_RUNNER_URL, and run and flow demo fail
//
// defaultModelID is anthropic.DefaultModel, which a nomodels build cannot
// name; TestDefaultModelIsTheAdapters keeps them equal.
const defaultModelID = "claude-haiku-5-5"
