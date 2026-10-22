import type { Me } from "@agentkit/agent-ui/api";
import { Bot, ListChecks, KeyRound, MessagesSquare, Wrench, type LucideIcon } from "lucide-react";

/**
 * Whether the console shows admin navigation (the admin sections, the
 * Triggers tab): only once GET /v1/auth/me says the caller is an admin, or
 * holds agent:admin, or the server has no credentials (everyone may do
 * everything). Unknown is no. Hiding is a convenience: the API answers 403
 * (docs/UI.md "Rules").
 */
export function mayAdmin(me: Me | null): boolean {
  if (!me) return false;
  if (!me.credential) return true;
  return me.roles.includes("admin") || me.scopes.includes("*") || me.scopes.includes("agent:admin");
}

export type Section = { to: string; label: string; icon: LucideIcon; admin?: boolean };

const chats: Section = { to: "/", label: "Chats", icon: MessagesSquare };
const connections: Section = { to: "/connections", label: "Connections", icon: KeyRound };
/** Background runs and the threads between agents (T074; T087 renames it Tasks). */
export const tasks: Section = { to: "/tasks", label: "Tasks", icon: ListChecks };

// Above the divider: everyone's. Below: admin pages (the API enforces roles;
// hiding them is a convenience, docs/UI.md "Rules"). Skills (T087) takes
// its slot later.
export const sections: readonly Section[] = [chats, tasks, connections];
export const adminSections: readonly Section[] = [
  { to: "/agents", label: "Agents", icon: Bot, admin: true },
  { to: "/toolsets", label: "Toolsets", icon: Wrench, admin: true },
];

/** The section a path belongs to: "/c/abc" and "/new" are Chats. */
export function sectionOf(pathname: string): Section {
  return (
    [...adminSections, tasks, connections].find((s) => pathname === s.to || pathname.startsWith(`${s.to}/`)) ?? chats
  );
}
