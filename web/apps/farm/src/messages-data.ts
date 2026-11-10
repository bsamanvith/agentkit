import type { Receipt } from "./types";

/** A lane's name for people. */
export function laneLabel(name: string): string {
  return (
    { push: "App alerts", telegram: "Telegram", email: "Email", whatsapp: "WhatsApp", sms: "SMS", log: "Server log" }[
      name
    ] ?? name
  );
}

export const languages: [string, string][] = [
  ["en", "English"],
  ["te", "తెలుగు (Telugu)"],
  ["hi", "हिन्दी (Hindi)"],
];

/** A phone number as the server keeps it: +91 for a 10-digit Indian number. */
export function e164(phone: string): string {
  const p = phone.replace(/[\s()-]/g, "");
  if (/^[6-9]\d{9}$/.test(p)) return "+91" + p;
  if (/^0[6-9]\d{9}$/.test(p)) return "+91" + p.slice(1);
  if (/^91[6-9]\d{9}$/.test(p)) return "+" + p;
  return p;
}

export function rupees(paise: number): string {
  return "₹" + (paise / 100).toFixed(paise % 100 === 0 ? 0 : 2);
}

/**
 * The route tree in words: "fanout(push,telegram,chain(whatsapp,sms))" is
 * "App alerts and Telegram and (WhatsApp, else SMS)".
 */
export function describeTree(tree: string): string {
  let i = 0;
  const node = (): string => {
    const m = /^[a-z0-9_]+/.exec(tree.slice(i));
    if (!m) return tree.slice(i);
    i += m[0].length;
    if (tree[i] !== "(") return laneLabel(m[0]);
    i++;
    const kids: string[] = [];
    while (i < tree.length && tree[i] !== ")") {
      kids.push(node());
      if (tree[i] === ",") i++;
    }
    i++;
    const joined = kids.join(m[0] === "chain" ? ", else " : " and ");
    return kids.length > 1 ? `(${joined})` : joined;
  };
  const out = node();
  return out.startsWith("(") && out.endsWith(")") ? out.slice(1, -1) : out;
}

const routeLabels: Record<string, string> = {
  default: "Everything else",
  urgent: "Urgent alerts",
  warning: "Warnings",
  advice: "Advice",
  digest: "Weekly summaries",
  alert: "Alerts",
  reply: "Replies",
  test: "Tests",
};

/** The routes, one line each, in words. */
export function describeRoutes(routes: string): { key: string; label: string; tree: string }[] {
  return routes
    .split(";")
    .map((r) => r.trim())
    .filter(Boolean)
    .map((r) => {
      const at = r.indexOf("=");
      const key = r.slice(0, at).trim();
      return { key, label: routeLabels[key] ?? key, tree: describeTree(r.slice(at + 1).replace(/\s/g, "")) };
    });
}

/** What happened to a test on a lane, in words. */
export function receiptWords(t: Receipt): string {
  switch (t.status) {
    case "sent":
      return t.repeat ? "sent already" : "sent";
    case "delivered":
      return "delivered";
    case "read":
      return "read";
    case "skipped":
      if (t.error?.includes("consent")) return "skipped: they have not agreed to it";
      if (t.error?.includes("address")) return "skipped: no address for it";
      if (t.error?.includes("off")) return "skipped: the lane is off";
      if (t.error?.includes("cap")) return "skipped: the lane reached its spending cap";
      return `skipped${t.error ? `: ${t.error}` : ""}`;
    case "failed":
      return `failed${t.error ? `: ${t.error}` : ""}`;
  }
  return t.status;
}

/** Lanes in the order people should see them: the free ones first. */
const laneOrder = ["push", "telegram", "email", "whatsapp", "sms"];

export function byLaneOrder(a: { name: string }, b: { name: string }): number {
  const rank = (n: string) => (laneOrder.includes(n) ? laneOrder.indexOf(n) : laneOrder.length);
  return rank(a.name) - rank(b.name) || a.name.localeCompare(b.name);
}
