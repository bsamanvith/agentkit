import type { StatusTone } from "@agentkit/ui";
import { areaM2 } from "./draw";
import type { ZoneShape } from "./map";
import type { Crop, Part, Plan, PlanReport, Point } from "./types";

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

/**
 * addPart adds a suggested crop to the plan, replacing the same crop, and
 * shares the plot equally between the plan's crops, so the shares add up to
 * the whole plot however many are added.
 */
export function addPart(ps: Part[], p: Part): Part[] {
  const next = [...ps.filter((x) => x.crop !== p.crop || x.with), p];
  const each = Math.floor(100 / next.length);
  return next.map((x, i) => ({ ...x, share: (i === next.length - 1 ? 100 - each * (next.length - 1) : each) / 100 }));
}

/**
 * The parts of a plan drawn as areas: one per area, keeping each area's
 * crop and date (a new area takes the first area's), its share the area's
 * part of the plot.
 */
export function areaParts(areas: Point[][], parts: Part[], plot: Point[]): Part[] {
  const whole = areaM2(plot) || 1;
  return areas.map((ring, i) => {
    const p = parts[i] ?? parts[0];
    return {
      crop: p?.crop ?? "",
      with: p?.with,
      pattern: p?.pattern,
      sow: p?.sow ?? "",
      share: Math.round((areaM2(ring) / whole) * 1000) / 1000,
    };
  });
}

/**
 * The plan of a plot drawn as areas: a zone for each area that has a crop
 * and a sowing date, rows north to south as the laid-out plans have them.
 * An area without a crop stays unplanned.
 */
export function areaPlan(areas: Point[][], parts: Part[]): Plan {
  return {
    zones: areas.flatMap((ring, i) => {
      const p = parts[i];
      if (!p?.crop || !p.sow) return [];
      return [{ crop: p.crop, with: p.with, pattern: p.pattern, ring, row_bearing: 0, sow: p.sow }];
    }),
  };
}

/** A suggested crop for a plan drawn as areas: it goes to the first area without a crop, else the last area. */
export function fillArea(ps: Part[], p: Part): Part[] {
  const empty = ps.findIndex((x) => !x.crop);
  const k = empty >= 0 ? empty : ps.length - 1;
  return ps.map((x, i) => (i === k ? { ...x, crop: p.crop, with: p.with, pattern: p.pattern, sow: p.sow } : x));
}

/** A saved plan's zones as areas to divide further, when they cover the whole plot (as drawn areas do). */
export function areasOf(plan: Plan | undefined, plot: Point[]): Point[][] | undefined {
  const rings = (plan?.zones ?? []).map((z) => z.ring);
  if (rings.length < 2) return undefined;
  const sum = rings.reduce((s, r) => s + areaM2(r), 0);
  return Math.abs(sum - areaM2(plot)) <= areaM2(plot) * 0.02 ? rings : undefined;
}
