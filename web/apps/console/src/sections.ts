import { Bot, KeyRound, MessagesSquare, Wrench, type LucideIcon } from "lucide-react";

export type Section = { to: string; label: string; icon: LucideIcon; admin?: boolean };

const chats: Section = { to: "/", label: "Chats", icon: MessagesSquare };
const connections: Section = { to: "/connections", label: "Connections", icon: KeyRound };

// Above the divider: everyone's. Below: admin pages (the API enforces roles;
// hiding them is a convenience, docs/UI.md "Rules"). Activity (T074) and
// Skills (T087) take their slots later.
export const sections: readonly Section[] = [chats, connections];
export const adminSections: readonly Section[] = [
  { to: "/agents", label: "Agents", icon: Bot, admin: true },
  { to: "/toolsets", label: "Toolsets", icon: Wrench, admin: true },
];

/** The section a path belongs to: "/c/abc" and "/new" are Chats. */
export function sectionOf(pathname: string): Section {
  return [...adminSections, connections].find((s) => pathname === s.to || pathname.startsWith(`${s.to}/`)) ?? chats;
}
