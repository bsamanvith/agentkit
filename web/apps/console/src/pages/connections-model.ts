// The Connections page's rows (docs/UI.md "Connections"): one per credential
// a connector that an agent uses needs. A slot (GET /v1/credentials) is a
// row per scope: the caller's own key or account, or the tenant's shared
// one; a toolset that reads a server key from the environment (GET
// /v1/toolsets) is a row the console shows but doesn't manage. Rows that
// need you come first. No row holds a value: the API sends none.

import type { Agent, CredentialInfo, Toolset } from "@agentkit/agent-ui/api";
import type { StatusTone } from "@agentkit/ui";
import { credentialWords, shortDate } from "./credential";

export type Action = "connect" | "replace" | "reconnect" | "disconnect";

export type Row = {
  /** Unique on the page: "slot:mail:user", "env:exa". */
  id: string;
  /** The slot, or the toolset that reads a server key. */
  name: string;
  /** "API key", "Account", "Server key". */
  kind: string;
  /** "Yours", "Shared": whose credential it is; "" for a server key. */
  owner: string;
  tone: StatusTone;
  word: string;
  /** What the status means, or how to fix it. */
  detail: string;
  /** The agents that use it. */
  usedBy: string[];
  actions: Action[];
  /** Whether it needs the caller (sorted first). */
  needsYou: boolean;
  /** A slot's: for the API calls. */
  slot?: Slot;
};

/** A credential slot as the API calls name it. */
export type Slot = { name: string; scope: string; oauth: boolean };

const kindWords: Record<string, string> = {
  api_key: "key",
  basic: "user name and password",
  client_credentials: "client credentials",
};

function slotRow(c: CredentialInfo, mayManage: boolean, usedBy: string[]): Row {
  const mine = c.scope === "user";
  const base = {
    id: `slot:${c.slot}:${c.scope}`,
    name: c.slot,
    kind: c.oauth || c.kind === "oauth2" ? "Account" : "API key",
    owner: mine ? "Yours" : "Shared",
    usedBy,
    slot: { name: c.slot, scope: c.scope, oauth: c.oauth },
  };
  const when = shortDate(c.updated_at ?? c.created_at);
  const stored = (c.kind && kindWords[c.kind]) || "";
  if (c.status === "connected") {
    const what =
      c.kind === "oauth2"
        ? `${mine ? "Your" : "The tenant's"} account, connected`
        : `${mine ? "Your" : "The tenant's"} ${stored || "credential"}, added`;
    return {
      ...base,
      tone: "ok",
      word: "Connected",
      detail: when ? `${what} ${when}` : what,
      actions: !mayManage ? [] : c.kind === "oauth2" ? ["reconnect", "disconnect"] : ["replace", "disconnect"],
      needsYou: false,
    };
  }
  if (c.status === "needs_reconnect") {
    return {
      ...base,
      tone: "warn",
      word: "Needs reconnect",
      detail: mayManage ? "Access expired or was revoked." : "Access expired or was revoked. An admin reconnects it.",
      actions: mayManage ? ["reconnect", "disconnect"] : [],
      needsYou: mayManage,
    };
  }
  return {
    ...base,
    tone: mine ? "warn" : "neutral",
    word: "Not connected",
    detail: !mayManage
      ? "An admin sets it for everyone."
      : c.oauth
        ? `Connect ${mine ? "your" : "the tenant's"} account to use it.`
        : `Add ${mine ? "your" : "the tenant's"} API key to use it.`,
    actions: mayManage ? ["connect"] : [],
    needsYou: mayManage && mine,
  };
}

/**
 * The rows. credentials is null when the server keeps none (404): then a
 * toolset with a slot can't be connected, and says so.
 */
export function connectionRows(
  credentials: CredentialInfo[] | null,
  toolsets: Toolset[],
  agents: Agent[],
  mayManageTenant: boolean,
): Row[] {
  const usedBy = (names: string[]) =>
    agents.filter((a) => a.toolsets.some((t) => names.includes(t.name))).map((a) => a.name);
  const rows: Row[] = [];
  for (const c of credentials ?? []) {
    rows.push(slotRow(c, c.scope === "user" || mayManageTenant, usedBy(c.toolsets)));
  }
  for (const t of toolsets) {
    const cred = t.credential;
    if (!cred || cred.status === "not_needed") continue;
    if (cred.slot && credentials !== null) continue; // its slot's row says it
    // Without a vault a slot can't be set, whatever the toolset says.
    const w = credentialWords(cred.slot ? { ...cred, status: "missing" } : cred);
    if (!w) continue;
    rows.push({
      id: cred.slot ? `slot:${cred.slot}:${cred.scope ?? ""}` : `env:${t.name}`,
      name: cred.slot ?? t.name,
      kind: cred.slot ? "Key or account" : "Server key",
      owner: "",
      tone: w.tone,
      word: w.word,
      detail: [
        cred.env && !cred.slot && cred.status === "configured" ? `From ${cred.env}, on the server.` : "",
        w.detail,
      ]
        .filter(Boolean)
        .join(" "),
      usedBy: usedBy([t.name]),
      actions: [],
      needsYou: false,
    });
  }
  // One row per connector an agent uses (a slot shared by two toolsets is one row already).
  const seen = new Set<string>();
  return rows
    .filter((r) => r.usedBy.length > 0 && !seen.has(r.id) && seen.add(r.id))
    .sort((a, b) => Number(b.needsYou) - Number(a.needsYou) || a.name.localeCompare(b.name));
}

/** Where /connections/done goes back to when the provider was opened in this tab. */
export const connectReturnKey = "agentkit.connect.return";
/** The channel /connections/done reports a popup's result on (same origin only). */
export const connectChannel = "agentkit.connect";
/** The popup's window name. */
export const connectWindow = "agentkit-connect";
/**
 * Set in sessionStorage while the popup opens, which gives the popup a copy:
 * /connections/done knows it is in the popup by it, even after a provider
 * whose opener policy cut window.opener and a browser that cleared
 * window.name on the way.
 */
export const connectPopupKey = "agentkit.connect.popup";

/**
 * Whether path is a route of this console: it starts with one "/", never
 * "//" or "/\" (which a URL reads as another host).
 */
export function isLocalPath(path: string): boolean {
  return path.startsWith("/") && !path.startsWith("//") && !path.startsWith("/\\");
}

/** Whether this window is the connect popup. */
export function inPopup(): boolean {
  if (window.opener || window.name === connectWindow) return true;
  try {
    return sessionStorage.getItem(connectPopupKey) !== null;
  } catch {
    return false;
  }
}

export type ConnectResult = { slot: string; result: string };

/** What a connect flow's result says (docs/UI.md "Connecting an account"). */
export function resultText(r: ConnectResult): { text: string; ok: boolean } {
  switch (r.result) {
    case "connected":
      return { text: `${r.slot} connected.`, ok: true };
    case "denied":
      return { text: "You didn't allow access. Nothing was saved.", ok: false };
    default:
      return { text: "Connecting took too long or failed. Try again.", ok: false };
  }
}
