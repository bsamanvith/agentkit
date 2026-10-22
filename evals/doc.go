// Package evals holds the tool-use evals (T050): real Claude, through
// `agentkit serve` and `agentkit ask -json`, with the search connectors
// (tavily, exa, parallel) talking to faked upstreams that serve recorded
// cassettes and scripted errors. They measure whether the model uses the
// tools well: the right tool, valid and fitting arguments, few calls,
// citations from results, and recovery after an error or a truncation.
//
// The scenarios are connectors/*.json; `make eval` runs them (the eval
// build tag, never in CI) and writes results/<date>.json. README.md says
// how to read and extend them. This package's untagged code (scenarios,
// checks, the faked upstream and the report) is tested by `make test`.
package evals
