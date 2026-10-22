// Using a toolset from Claude Code: its MCP URL on this server and the
// `claude mcp add` command (docs/UI.md "Toolsets").

import type { Toolset } from "@agentkit/agent-ui/api";

/** The toolset's MCP URL on this server, or "" when the server doesn't serve it over MCP. */
export function mcpURL(t: Toolset, origin: string): string {
  if (!t.mcp_path) return "";
  try {
    return new URL(t.mcp_path, origin).href;
  } catch {
    return "";
  }
}

/**
 * The command that adds the toolset to Claude Code. The token is a
 * placeholder for the shell to fill in, never a real one: the page has none
 * (a session's cookie is not a token, and a pasted token stays in the
 * client).
 */
export function mcpAddCommand(name: string, url: string): string {
  return `claude mcp add --transport http ${shellWord(name)} ${shellWord(url)} --header "Authorization: Bearer $AGENTKIT_API_TOKEN"`;
}

/** s as one word for a POSIX shell: as it is when it is plain, else single-quoted. */
export function shellWord(s: string): string {
  if (/^[A-Za-z0-9_./:@%+=,-]+$/.test(s)) return s;
  return `'${s.replace(/'/g, `'\\''`)}'`;
}
