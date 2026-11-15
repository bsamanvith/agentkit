import { call } from "./api";
import type { Plan } from "./types";

// The Cycles tab is a lazy chunk: its types and calls live here, not in
// api.ts and types.ts, so the first load does not grow.

export type Method = "conventional" | "organic";
export type DateKind = "planned" | "actual";

export interface Note {
  id: string;
  message: string;
}

export interface CycleArea {
  zone: number;
  crop: string;
  crop_name: string;
  with?: string;
  area_m2: number;
  area_ha: number;
  sow: string;
  sow_kind: DateKind;
  harvest: string;
}

export interface Cycle {
  origin: "suggested" | "farmer";
  season: string;
  method: Method;
  plan: Plan;
  areas: CycleArea[];
  sow: string;
  sow_kind: DateKind;
  harvest: string;
  perennial?: boolean;
  score?: number;
  reasons?: Note[];
  risks?: Note[];
}

export interface CycleCheck {
  id: string;
  severity: "info" | "warning" | "error";
  area: number;
  message: string;
}

export interface Position {
  position: number;
  from: string;
  suggested?: Cycle;
  own?: Cycle;
  planned?: Cycle;
  checks?: CycleCheck[];
  /** Nothing suits sowing soon: the land rests until the suggestion's season. */
  rest?: Note;
  /** What other features computed from the planned cycle, by name (a shopping list, sprays, sell timing). */
  addons?: Record<string, unknown>;
}

export interface Line {
  farm: string;
  today: string;
  future: boolean;
  cycles: Position[];
  notes?: Note[];
  /** The plot's water and soil were never given: the suggestions assume rain only. */
  assumed?: boolean;
}

export interface Change {
  accept?: boolean;
  plan?: Plan;
  clear?: boolean;
  method?: Method;
}

const base = (farm: string) => `/api/farms/${encodeURIComponent(farm)}/cycles`;

export const cyclesApi = {
  get: (farm: string, signal?: AbortSignal) => call<Line>("GET", base(farm), undefined, signal),
  set: (farm: string, pos: number, ch: Change) => call<Line>("PUT", `${base(farm)}/${pos}`, ch),
  clear: (farm: string, pos: number) => call<Line>("DELETE", `${base(farm)}/${pos}`),
  sown: (farm: string, day: string) => call<Line>("POST", `${base(farm)}/sown`, { day }),
  future: (farm: string, on: boolean) => call<Line>("PUT", `${base(farm)}/settings`, { future: on }),
  advance: (farm: string) => call<Line>("POST", `${base(farm)}/advance`),
};

/** The crops of a cycle, as the farmer says them: "Maize", "Maize, Green gram". */
export function cropsOf(c: Cycle): string {
  return [...new Set(c.areas.map((a) => a.crop_name))].join(", ");
}

/** Whether two cycles grow the same crops on the same days. */
export function sameCycle(a?: Cycle, b?: Cycle): boolean {
  if (!a || !b || a.areas.length !== b.areas.length) return false;
  return a.areas.every((x, i) => x.crop === b.areas[i]?.crop && x.sow === b.areas[i]?.sow);
}

/**
 * A plan with each area's crop and sowing date changed: what stays (the
 * area's boundary, the row direction) is kept; a changed crop drops the
 * old crop's spacing and intercrop.
 */
export function edited(plan: Plan, areas: { crop: string; sow: string }[]): Plan {
  return {
    zones: plan.zones.map((z, i) => {
      const a = areas[i] ?? { crop: z.crop, sow: z.sow };
      if (a.crop === z.crop) return { ...z, sow: a.sow };
      return { ring: z.ring, row_bearing: z.row_bearing, crop: a.crop, sow: a.sow };
    }),
  };
}

/**
 * Where a cycle sits on a timeline from start to end, as left and width
 * in percent; clamped to the timeline.
 */
export function span(start: string, end: string, from: string, to: string): { left: number; width: number } {
  const t = (d: string) => Date.parse(d + "T00:00:00Z");
  const total = Math.max(1, t(end) - t(start));
  const left = Math.min(100, Math.max(0, ((t(from) - t(start)) / total) * 100));
  const right = Math.min(100, Math.max(left, ((t(to) - t(start)) / total) * 100));
  return { left, width: right - left };
}

/** A task of the season roadmap, as the current cycle's week shows it. */
export interface WeekTask {
  id: string;
  title: string;
  date: string;
  state: string;
  overdue?: boolean;
  passed?: boolean;
  /** Dated before today, not overdue: to do now, before the sowing. */
  late?: boolean;
}

// dayLabel and addDays are the roadmap's (roadmap-data.ts), repeated so
// this chunk does not split that one out of the roadmap's.

export function dayLabel(day: string): string {
  const d = new Date(day + "T00:00:00");
  if (Number.isNaN(d.getTime())) return day;
  const now = new Date();
  // A day in another year says its year: a suggestion can be months ahead.
  const year = d.getFullYear() !== now.getFullYear() ? "numeric" : undefined;
  // The weekday helps only for the next fortnight or so.
  const near = Math.abs(d.getTime() - now.getTime()) < 15 * 864e5;
  return d.toLocaleDateString("en-IN", { weekday: near ? "short" : undefined, day: "numeric", month: "short", year });
}

export function addDays(day: string, n: number): string {
  const d = new Date(day + "T00:00:00Z");
  d.setUTCDate(d.getUTCDate() + n);
  return d.toISOString().slice(0, 10);
}
