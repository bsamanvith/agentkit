// Package registry searches an MCP Registry (T097, E19): the official one at
// registry.modelcontextprotocol.io, or a company's own subregistry that
// serves the same API (v0.1). It keeps only the servers agentkit can add by
// URL, those with a remote over Streamable HTTP or SSE, and drops the ones
// that are only packages to run (npm, PyPI, OCI, ...), since a web request
// never makes agentkit run a command.
//
//	c, err := registry.New(registry.DefaultURL)
//	found, err := c.Search(ctx, "linear", 20)
//
// Results are cached for a while ([WithCacheTTL]); a failure is not. A
// registry entry says where a server is, not that it is safe: adding one
// still goes through the probe, the review and the pinning of
// package mcp/servers.
package registry
