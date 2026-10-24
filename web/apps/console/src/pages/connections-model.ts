// The Connectors page's cards (docs/UI.md "Connectors"): one per credential
// a connector of the server needs. A slot (GET /v1/credentials) is a card
// per scope, whether or not an agent uses it yet: the caller's own key or
// account, or the tenant's shared one; a toolset an agent uses that reads a
// server key from the environment (GET /v1/toolsets) is a card the console
// shows but doesn't manage. Cards are grouped by provider, and within a
// group those that need you come first. No card holds a value: the API
// sends none.

import type { Agent, CredentialInfo, Toolset } from "@agentkit/agent-ui/api";
import type { StatusTone } from "@agentkit/ui";
import { connectorInfo, groups, type Group } from "./connectors-catalog";
import { credentialWords, shortDate } from "./credential";

export type Action = "connect" | "replace" | "reconnect" | "disconnect";

export type Row = {
  /** Unique on the page: "slot:mail:user", "env:exa". */
  id: string;
  /** The slot, or the toolset that reads a server key. */
  name: string;
  /** The connector's name ("Gmail"), else name. */
  title: string;
  group: Group;
  /** What the connector does, when the console knows it. */
  does: string;
  /** Where to get a key ("dashboard.exa.ai"), for a key connector the console knows. */
  keyHelp?: string;
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

function slotRow(c: CredentialInfo, type: string | undefined, mayManage: boolean, usedBy: string[]): Row {
  const mine = c.scope === "user";
  const info = connectorInfo(type);
  const account = c.oauth || c.kind === "oauth2" || info?.auth === "account";
  const base = {
    id: `slot:${c.slot}:${c.scope}`,
    name: c.slot,
    title: info?.name ?? c.slot,
    group: info?.group ?? "Other",
    does: info?.does ?? "",
    keyHelp: info?.keyHelp,
    kind: account ? "Account" : "API key",
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
      actions: !mayManage
        ? []
        : c.kind === "oauth2"
          ? c.oauth
            ? ["reconnect", "disconnect"]
            : ["disconnect"]
          : ["replace", "disconnect"],
      needsYou: false,
    };
  }
  if (account && !c.oauth) {
    // An account connector whose OAuth app the server doesn't have: a key
    // pasted in would be refused, so there is nothing to do here.
    return {
      ...base,
      tone: "neutral",
      word: "Not set up",
      detail: `This server isn't set up to connect ${base.title} accounts. An admin adds its OAuth app (credentials.oauth2).`,
      actions: mayManage && c.status !== "not_connected" ? ["disconnect"] : [],
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
 * The cards. credentials is null when the server keeps none (404): then a
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
  const typeOf = (names: string[]) => toolsets.find((t) => names.includes(t.name))?.type;
  const rows: Row[] = [];
  for (const c of credentials ?? []) {
    rows.push(slotRow(c, typeOf(c.toolsets), c.scope === "user" || mayManageTenant, usedBy(c.toolsets)));
  }
  for (const t of toolsets) {
    const cred = t.credential;
    if (!cred || cred.status === "not_needed") continue;
    if (cred.slot && credentials !== null) continue; // its slot's card says it
    // A server key is the deployment's business: listed only when an agent uses it.
    if (!cred.slot && usedBy([t.name]).length === 0) continue;
    // Without a vault a slot can't be set, whatever the toolset says.
    const w = credentialWords(cred.slot ? { ...cred, status: "missing" } : cred);
    if (!w) continue;
    const info = connectorInfo(t.type);
    const name = cred.slot ?? t.name;
    rows.push({
      id: cred.slot ? `slot:${cred.slot}:${cred.scope ?? ""}` : `env:${t.name}`,
      name,
      title: info?.name ?? name,
      group: info?.group ?? "Other",
      does: info?.does ?? "",
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
  // One card per credential (a slot shared by two toolsets is one card already).
  const seen = new Set<string>();
  return rows
    .filter((r) => !seen.has(r.id) && seen.add(r.id))
    .sort(
      (a, b) =>
        groups.indexOf(a.group) - groups.indexOf(b.group) ||
        Number(b.needsYou) - Number(a.needsYou) ||
        a.title.localeCompare(b.title),
    );
}

/** Whether a card matches the filter box's text (name, connector, what it does). */
export function matches(row: Row, text: string): boolean {
  const q = text.trim().toLowerCase();
  return q === "" || [row.name, row.title, row.does, row.group].some((s) => s.toLowerCase().includes(q));
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
