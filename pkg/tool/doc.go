// Package tool defines agentkit's tool abstraction: a named, described
// operation with JSON Schema input and output that a model can call.
//
// The harness, the MCP server and remote tool servers all go through the same
// two interfaces:
//
//   - [Tool]: one callable tool. Build one from a Go function with [Func],
//     which derives the schemas from the function's input and output types and
//     validates every call's input before running it.
//   - [Registry]: a set of tools, listed and invoked by name. [NewRegistry]
//     builds an in-process registry; a remote client implements the same
//     interface, so callers cannot tell where a tool runs.
//
// Errors are classified with sentinel values, checked with [errors.Is]:
// [ErrInvalidInput] means the caller (usually a model) sent bad input and can
// retry; [ErrToolFailed] means the tool ran and failed; [ErrNotFound] means no
// tool has that name. A cancelled or expired context is reported as the
// context's error, never as [ErrToolFailed], so a stopped run can be told
// apart from a failed tool.
//
// Conformance tests for any Registry implementation live in package tooltest.
package tool
