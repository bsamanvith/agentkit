# agentkit

A modular Go framework for building agentic harnesses, tools, MCP servers/clients, and secure credential handling.

- **Harness**: provider-agnostic agent loop with streaming, cancellation, and parallel tool calls.
- **Tools**: our own lightweight tool protocol (gRPC internally, JSON/HTTP via ConnectRPC).
- **MCP**: connect to external MCP servers and expose any tool registry as a spec-compliant MCP server.
- **Credentials**: API key, OAuth2, JWT, basic, and mTLS with pluggable secure storage backends.

Each package can be imported on its own or run as a lightweight server.

## Status

**Planning.** No code yet. See the scope and module plan in [docs/PLAN.md](docs/PLAN.md).

## License

[Apache-2.0](LICENSE)
