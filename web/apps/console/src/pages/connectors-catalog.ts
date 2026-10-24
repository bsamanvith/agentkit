// What the Connectors page says about each connector type (docs/UI.md
// "Connectors"): its name, its provider group, a line on what it does, and
// whether a user connects an account (OAuth) or pastes a key. The server
// knows none of these words; a type not listed here shows its slot's name
// in the "Other" group.

export type Group = "Google" | "CRM" | "Search" | "Other";

/** The order groups are shown in. */
export const groups: Group[] = ["Google", "CRM", "Search", "Other"];

export type ConnectorInfo = {
  name: string;
  group: Group;
  /** What it lets an agent do, in a line. */
  does: string;
  /** "account": connected through the provider (OAuth); "key": an API key pasted in. */
  auth: "account" | "key";
  /** Where to get a key, for a key connector. */
  keyHelp?: string;
};

export const connectorCatalog: Record<string, ConnectorInfo> = {
  gmail: { name: "Gmail", group: "Google", does: "Search, read and label mail; write drafts.", auth: "account" },
  calendar: {
    name: "Google Calendar",
    group: "Google",
    does: "Read calendars and free time; create and change events.",
    auth: "account",
  },
  drive: { name: "Google Drive", group: "Google", does: "Find, read and create files and folders.", auth: "account" },
  sheets: { name: "Google Sheets", group: "Google", does: "Read ranges; add and update rows.", auth: "account" },
  hubspot: {
    name: "HubSpot",
    group: "CRM",
    does: "Search contacts, companies, deals and tickets; update records and add notes.",
    auth: "account",
  },
  salesforce: {
    name: "Salesforce",
    group: "CRM",
    does: "Query and search records, describe objects; create and update records.",
    auth: "account",
  },
  exa: {
    name: "Exa",
    group: "Search",
    does: "Search the web and read pages.",
    auth: "key",
    keyHelp: "dashboard.exa.ai",
  },
  parallel: {
    name: "Parallel",
    group: "Search",
    does: "Search the web and read pages.",
    auth: "key",
    keyHelp: "platform.parallel.ai",
  },
  tavily: {
    name: "Tavily",
    group: "Search",
    does: "Search the web and read pages.",
    auth: "key",
    keyHelp: "tavily.com",
  },
};

/** The words for a toolset type, or null for one the console doesn't know. */
export function connectorInfo(type: string | undefined): ConnectorInfo | null {
  return (type && Object.hasOwn(connectorCatalog, type) && connectorCatalog[type]) || null;
}
