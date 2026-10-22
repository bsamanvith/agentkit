// A toolset's credential in words (docs/UI.md "Toolsets" and "Connections"):
// where it comes from and whether it is there. Only names reach the page,
// never values (the API sends none).

import type { ToolsetCredential } from "@agentkit/agent-ui/api";
import type { StatusTone } from "@agentkit/ui";

export type CredentialWords = {
  /** "API key, each user connects their own", "Server key EXA_API_KEY". */
  source: string;
  /** The status: a tone and a word. */
  tone: StatusTone;
  word: string;
  /** How to fix it, when it needs fixing; else what it means. */
  detail: string;
};

const whose = (scope: string | undefined) =>
  scope === "user" ? "each user connects their own" : "shared by the tenant, set by an admin";

/** The credential of a toolset as GET /v1/toolsets says it, or null when it needs none. */
export function credentialWords(c: ToolsetCredential | undefined): CredentialWords | null {
  if (!c || c.status === "not_needed") return null;
  if (c.slot) {
    const source = `Slot ${c.slot}, ${whose(c.scope)}`;
    switch (c.status) {
      case "connected":
        return { source, tone: "ok", word: "Connected", detail: "" };
      case "needs_reconnect":
        return { source, tone: "warn", word: "Needs reconnect", detail: "Access expired or was revoked." };
      case "not_connected":
        return {
          source,
          tone: "warn",
          word: "Not connected",
          detail: c.scope === "user" ? "Connect it in Connections." : "An admin sets it in Connections.",
        };
      default: // missing: the server keeps no credentials
        return {
          source,
          tone: "warn",
          word: "Not available",
          detail:
            "This server keeps no credentials, so the slot can't be set. An admin sets AGENTKIT_CREDENTIALS_KEY and restarts.",
        };
    }
  }
  const env = c.env ?? "";
  switch (c.status) {
    case "configured":
      return { source: `Server key ${env}`, tone: "ok", word: "Using the server key", detail: "" };
    case "keyless":
      return {
        source: env ? `Server key ${env} (optional)` : "None",
        tone: "neutral",
        word: "Keyless",
        detail: `Works without a key, with lower limits.${env ? ` Set ${env} on the server for a key.` : ""}`,
      };
    case "missing":
      return {
        source: `Server key ${env}`,
        tone: "warn",
        word: "Server key not set",
        detail: env ? `Set ${env} in the server's environment, then restart agentkit.` : "",
      };
    default:
      return { source: env ? `Server key ${env}` : "Unknown", tone: "neutral", word: c.status, detail: "" };
  }
}

/** A date as "22 Oct 2026"; "" when it isn't one. */
export function shortDate(iso: string | undefined): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleDateString("en-GB", { day: "numeric", month: "short", year: "numeric" });
}
