import type { ZoneShape } from "./map";
import type { Crop, Design, Element, FeatureKind, Nearby, Point, Zone } from "./types";

// The land view's geometry and styles, apart from the component so they can
// be tested: a flat projection in metres around the plot, view boxes,
// clipping, and where border trees and crop growth go.

/** A point in metres from the projection's origin: x east, y south (SVG). */
export interface XY {
  x: number;
  y: number;
}

const mPerDegLat = 110_540;
const mPerDegLng = 111_320;

/**
 * A projection good to a few centimetres over a few kilometres: metres
 * east and south of origin, so north is up on screen.
 */
export function projector(origin: Point): (p: Point) => XY {
  const k = Math.cos((origin.lat * Math.PI) / 180) * mPerDegLng;
  return (p) => ({ x: (p.lng - origin.lng) * k, y: -(p.lat - origin.lat) * mPerDegLat });
}

/** An SVG path through the points; closed adds Z. */
export function pathOf(xys: XY[], closed: boolean): string {
  if (xys.length === 0) return "";
  const d = xys.map((p, i) => `${i === 0 ? "M" : "L"}${p.x.toFixed(2)} ${p.y.toFixed(2)}`).join("");
  return closed ? d + "Z" : d;
}

export interface Rect {
  x: number;
  y: number;
  w: number;
  h: number;
}

/** The smallest rectangle around the points, grown by margin on every side. */
export function bounds(xys: XY[], margin = 0): Rect {
  if (xys.length === 0) return { x: -margin, y: -margin, w: 2 * margin, h: 2 * margin };
  let [x0, y0, x1, y1] = [Infinity, Infinity, -Infinity, -Infinity];
  for (const p of xys) {
    x0 = Math.min(x0, p.x);
    y0 = Math.min(y0, p.y);
    x1 = Math.max(x1, p.x);
    y1 = Math.max(y1, p.y);
  }
  return { x: x0 - margin, y: y0 - margin, w: x1 - x0 + 2 * margin, h: y1 - y0 + 2 * margin };
}

/** r widened or heightened about its centre to the aspect w/h. */
export function fitAspect(r: Rect, aspect: number): Rect {
  if (r.w / r.h > aspect) {
    const h = r.w / aspect;
    return { x: r.x, y: r.y - (h - r.h) / 2, w: r.w, h };
  }
  const w = r.h * aspect;
  return { x: r.x - (w - r.w) / 2, y: r.y, w, h: r.h };
}

/**
 * The part of the segment a-b inside r (Liang–Barsky), or undefined when
 * it misses r.
 */
export function clipSegment(a: XY, b: XY, r: Rect): [XY, XY] | undefined {
  const dx = b.x - a.x;
  const dy = b.y - a.y;
  let t0 = 0;
  let t1 = 1;
  const edges: [number, number][] = [
    [-dx, a.x - r.x],
    [dx, r.x + r.w - a.x],
    [-dy, a.y - r.y],
    [dy, r.y + r.h - a.y],
  ];
  for (const [p, q] of edges) {
    if (p === 0) {
      if (q < 0) return undefined;
      continue;
    }
    const t = q / p;
    if (p < 0) {
      if (t > t1) return undefined;
      t0 = Math.max(t0, t);
    } else {
      if (t < t0) return undefined;
      t1 = Math.min(t1, t);
    }
  }
  return [
    { x: a.x + t0 * dx, y: a.y + t0 * dy },
    { x: a.x + t1 * dx, y: a.y + t1 * dy },
  ];
}

/** A round length for a scale bar about a fifth of width metres. */
export function scaleLength(width: number): number {
  const target = width / 5;
  const pow = 10 ** Math.floor(Math.log10(target));
  for (const m of [5, 2, 1]) if (m * pow <= target) return m * pow;
  return pow;
}

/** Metres as a farmer reads them: 98 m, 1.2 km. */
export function metres(m: number): string {
  if (m >= 1000) return `${(m / 1000).toFixed(1)} km`;
  return `${Math.round(m)} m`;
}

/** Each side of a ring, as its two ends. */
export function sides<T>(ring: T[]): [T, T][] {
  return ring.map((a, i) => [a, ring[(i + 1) % ring.length] ?? a]);
}

/** A ring's signed area in square metres: negative when drawn clockwise on screen. */
export function ringArea(r: XY[]): number {
  return sides(r).reduce((s, [a, b]) => s + a.x * b.y - b.x * a.y, 0) / 2;
}

function nearestOn(p: XY, ring: XY[]): XY {
  let best = ring[0] ?? p;
  let bestD = Infinity;
  for (const [a, b] of sides(ring)) {
    const dx = b.x - a.x;
    const dy = b.y - a.y;
    const l2 = dx * dx + dy * dy;
    const t = l2 === 0 ? 0 : Math.max(0, Math.min(1, ((p.x - a.x) * dx + (p.y - a.y) * dy) / l2));
    const q = { x: a.x + t * dx, y: a.y + t * dy };
    const d = (q.x - p.x) ** 2 + (q.y - p.y) ** 2;
    if (d < bestD) {
      bestD = d;
      best = q;
    }
  }
  return best;
}

/**
 * Where a border band's trees stand: every spacing metres along the outer
 * ring, halfway to the inner one. At most max trees.
 */
export function treesAlong(outer: XY[], inner: XY[], spacing: number, max = 400): XY[] {
  if (outer.length < 3 || spacing <= 0) return [];
  const out: XY[] = [];
  let carry = 0;
  for (const [a, b] of sides(outer)) {
    if (out.length >= max) break;
    const len = Math.hypot(b.x - a.x, b.y - a.y);
    for (let d = carry; d < len && out.length < max; d += spacing) {
      const t = d / len;
      const p = { x: a.x + t * (b.x - a.x), y: a.y + t * (b.y - a.y) };
      const q = inner.length >= 3 ? nearestOn(p, inner) : p;
      out.push({ x: (p.x + q.x) / 2, y: (p.y + q.y) / 2 });
    }
    carry = (((carry - len) % spacing) + spacing) % spacing;
  }
  return out;
}

/** How a kind of feature is drawn. */
export interface Look {
  /** What it is, for the legend. */
  label: string;
  stroke: string;
  fill?: string;
  /** Line width in metres. */
  width: number;
  dash?: string;
  /** Water that flows: drawn with moving dashes. */
  flows?: boolean;
  /** Drawn standing up out of the ground in the tilted view. */
  rises?: boolean;
  /** Draw order: lower first. */
  z: number;
  /** The colour of its mark in lists and the legend, when not stroke. */
  mark?: string;
}

export const looks: Record<FeatureKind, Look> = {
  farmland: { label: "Fields", stroke: "#a3b18a", fill: "#dfe7c8", width: 0.6, z: 0 },
  settlement: { label: "Village", stroke: "#d6c7b0", fill: "#efe6d8", width: 0.6, z: 0 },
  industry: { label: "Industry", stroke: "#b8a9c9", fill: "#e7e0ef", width: 0.6, z: 0 },
  wetland: { label: "Wetland", stroke: "#7fb3a6", fill: "#cfe7df", width: 0.6, dash: "2 2", z: 1 },
  trees: { label: "Trees", stroke: "#4f7942", fill: "#9cc58a", width: 0.6, z: 1 },
  water: { label: "Pond or lake", stroke: "#3b82c4", fill: "#9cc9ec", width: 1, flows: true, z: 2 },
  river: { label: "River", stroke: "#3b82c4", width: 8, flows: true, z: 2 },
  canal: { label: "Canal", stroke: "#2f7fd0", width: 5, flows: true, z: 2 },
  stream: { label: "Stream", stroke: "#4c93d6", width: 2.5, flows: true, z: 2 },
  drain: { label: "Drain", stroke: "#6aa5d9", width: 1.8, flows: true, z: 2 },
  rail: { label: "Railway", stroke: "#57534e", width: 2.5, dash: "6 4", z: 3 },
  path: { label: "Footpath", stroke: "#a8a29e", width: 1, dash: "3 2", z: 3 },
  track: { label: "Cart track", stroke: "#b08d57", width: 2.5, z: 3 },
  road: { label: "Road", stroke: "#f5f5f4", width: 5, z: 4, mark: "#78716c" },
  building: { label: "Building", stroke: "#9a8478", fill: "#d9c4b8", width: 0.5, rises: true, z: 5 },
  facility: { label: "School, temple or market", stroke: "#b45309", fill: "#fcd9a8", width: 0.6, rises: true, z: 5 },
  power_line: { label: "Power line", stroke: "#e11d48", width: 0.8, dash: "8 3", z: 6 },
  well: { label: "Well", stroke: "#1d4ed8", fill: "#bfdbfe", width: 0.8, z: 6 },
};

/** Wider and darker roads for bigger classes. */
export function roadWidth(cls?: string): number {
  switch (cls) {
    case "motorway":
    case "trunk":
      return 12;
    case "primary":
      return 10;
    case "secondary":
      return 8;
    case "tertiary":
      return 6.5;
    default:
      return looks.road.width;
  }
}

export const elementLooks: Record<Element["kind"], { label: string; stroke: string; fill: string }> = {
  border: { label: "Border trees", stroke: "#3f6212", fill: "#bef264" },
  path: { label: "Cart path", stroke: "#92400e", fill: "#e7c79b" },
  pond: { label: "Farm pond", stroke: "#1d4ed8", fill: "#60a5fa" },
  shed: { label: "Shed", stroke: "#57534e", fill: "#d6d3d1" },
  gate: { label: "Gate", stroke: "#b91c1c", fill: "#fecaca" },
};

/** A nearby feature in a few words: "Drain 98 m E". */
export function nearbyLabel(n: Nearby): string {
  const what = n.name || n.ref || looks[n.kind]?.label || n.kind;
  if (n.inside) return `Inside ${what}`;
  if (n.crosses) return `${what} crosses the plot`;
  if (n.touches) return `${what} along the ${n.direction} side`;
  return `${what} ${metres(n.distance_m)} ${n.direction}`;
}

const day = 86_400_000;

/** When a zone's crop is sown and harvested, in ms since the epoch. */
export function season(z: Zone, crops: Crop[]): { sow: number; harvest: number } {
  const sow = Date.parse(z.sow + "T00:00:00Z");
  const c = crops.find((x) => x.id === z.crop);
  const days = c ? (c.days[0] + c.days[1]) / 2 : 120;
  return { sow, harvest: sow + days * day };
}

/** How grown a crop is at t: 0 before sowing, 1 at harvest, then 1. */
export function growth(t: number, s: { sow: number; harvest: number }): number {
  if (!(s.harvest > s.sow) || t <= s.sow) return 0;
  return Math.min(1, (t - s.sow) / (s.harvest - s.sow));
}

/** Bare earth, through green, to ripe gold as a crop grows. */
export function cropColor(g: number): string {
  if (g <= 0) return "#b08d68";
  type Stop = [number, [number, number, number]];
  const stops: Stop[] = [
    [0, [176, 141, 104]],
    [0.15, [163, 196, 104]],
    [0.6, [34, 139, 34]],
    [0.9, [132, 160, 40]],
    [1, [214, 174, 54]],
  ];
  const ripe: Stop = [1, [214, 174, 54]];
  const pairs = sides(stops).slice(0, -1);
  const [[g0, c0], [g1, c1]] = pairs.find(([, [hi]]) => g <= hi) ?? [ripe, ripe];
  const t = g1 > g0 ? (g - g0) / (g1 - g0) : 0;
  const mix = c0.map((v, k) => Math.round(v + ((c1[k] ?? v) - v) * t));
  return `rgb(${mix[0]} ${mix[1]} ${mix[2]})`;
}

/** The time span the plan covers, for the season player. */
export function planSpan(zones: Zone[], crops: Crop[]): { from: number; to: number } | undefined {
  if (zones.length === 0) return undefined;
  const ss = zones.map((z) => season(z, crops)).filter((s) => !Number.isNaN(s.sow));
  if (ss.length === 0) return undefined;
  return { from: Math.min(...ss.map((s) => s.sow)) - 7 * day, to: Math.max(...ss.map((s) => s.harvest)) + 7 * day };
}

/** A saved layout's pieces as the satellite map draws them. */
export function designShapes(d?: Design): ZoneShape[] {
  return (d?.elements ?? []).flatMap((e) =>
    e.ring && e.ring.length >= 3 && e.kind !== "border"
      ? [
          {
            ring: e.ring,
            // The path is long and thin: its label would sit on a crop's.
            label: e.kind === "path" ? "" : elementLooks[e.kind].label,
            color: elementLooks[e.kind].stroke,
          },
        ]
      : [],
  );
}
