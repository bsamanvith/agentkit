import type { StatusTone } from "@agentkit/ui";
import type { ZoneShape } from "./map";
import type { Crop, Part, Plan, PlanReport } from "./types";

// Helpers of the planning page, apart so the page module exports only
// components.

/** The zones' colours on the map and in the report, in order. */
export const colors = ["#16a34a", "#ca8a04", "#2563eb", "#db2777", "#7c3aed", "#0891b2", "#ea580c", "#65a30d"];

/** The crop's name in the browser's language when we have it. */
export function localName(names?: Record<string, string>): string {
  const lang = typeof navigator === "undefined" ? "" : navigator.language.slice(0, 2);
  return (names && names[lang]) ?? "";
}

export function formatDate(d: string): string {
  const t = new Date(d + "T00:00:00Z");
  return Number.isNaN(t.getTime())
    ? d
    : t.toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric", timeZone: "UTC" });
}

export function scoreTone(score: number): StatusTone {
  return score >= 60 ? "ok" : score >= 40 ? "warn" : "bad";
}

export function cropLabel(crops: Crop[], id: string, withID?: string): string {
  const name = (c: string) => crops.find((x) => x.id === c)?.name ?? c;
  return withID ? `${name(id)} + ${name(withID)}` : name(id);
}

/** The plan's zones as the map draws them. */
export function shapes(plan: Plan | undefined, crops: Crop[]): ZoneShape[] {
  return (plan?.zones ?? []).map((z, i) => ({
    ring: z.ring,
    label: cropLabel(crops, z.crop, z.with),
    color: colors[i % colors.length] ?? "#16a34a",
  }));
}

/** Parts that lay the plan out again: its crops, shares and dates. */
export function partsOf(plan: Plan | undefined, report: PlanReport | undefined): Part[] {
  return (plan?.zones ?? []).map((z, i) => ({
    crop: z.crop,
    with: z.with,
    pattern: z.pattern,
    share: report?.zones?.[i]?.share ?? 1 / (plan?.zones.length ?? 1),
    sow: z.sow,
  }));
}
