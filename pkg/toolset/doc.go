// Package toolset builds named toolsets from configuration: the "what to
// serve" half of running many connectors in one process (the "how to serve"
// half is pkg/mcp/server).
//
// Go cannot load packages at runtime, so the split is:
//
//   - Build time decides which connector packages are compiled in. Each one
//     provides a [Factory]; the binary collects them into a [Catalog].
//   - Config decides which toolsets run, under which names, with which
//     settings. Each entry is a [Spec]; [Build] turns specs into one
//     [tool.Registry] per name.
//
// Two specs of the same type are independent instances (two Salesforce orgs).
// Include and Exclude globs expose a subset of a connector's tools.
//
// There is no global registration: a Catalog is a value, built explicitly and
// immutable afterwards. The built-in [Demo] factory (echo, current time,
// integer arithmetic) needs no external accounts, for examples and smoke tests.
package toolset
