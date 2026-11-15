import type { Point } from "./types";

// The geometry of drawing a plot and dividing it into areas, in metres on
// a flat plane around the plot (a farm is small enough for that to be
// within centimetres). Pure functions: the editor and the area editor call
// them, the server checks what they make (the plot verifier, the plan
// check) as it checks anything else.

/** A point on the local plane: x east and y north, in metres. */
export interface XY {
  x: number;
  y: number;
}

const earth = 6371008.8;
const rad = Math.PI / 180;

/** A flat plane around origin: points to metres and back. */
export function plane(origin: Point) {
  const kx = earth * rad * Math.cos(origin.lat * rad);
  const ky = earth * rad;
  return {
    xy: (p: Point): XY => ({ x: (p.lng - origin.lng) * kx, y: (p.lat - origin.lat) * ky }),
    point: (q: XY): Point => ({ lat: round(origin.lat + q.y / ky), lng: round(origin.lng + q.x / kx) }),
  };
}

/** Rounds a coordinate to 7 decimals (about 1 cm), as the map does. */
function round(x: number): number {
  return Math.round(x * 1e7) / 1e7;
}

const sub = (a: XY, b: XY): XY => ({ x: a.x - b.x, y: a.y - b.y });
const cross = (a: XY, b: XY) => a.x * b.y - a.y * b.x;
const dot = (a: XY, b: XY) => a.x * b.x + a.y * b.y;
const len = (a: XY) => Math.hypot(a.x, a.y);

function segmentDistance(p: XY, a: XY, b: XY): number {
  const d = sub(b, a);
  const l2 = dot(d, d);
  const t = l2 === 0 ? 0 : Math.max(0, Math.min(1, dot(sub(p, a), d) / l2));
  return len(sub(p, { x: a.x + t * d.x, y: a.y + t * d.y }));
}

function signedArea(ps: XY[]): number {
  let s = 0;
  ps.forEach((p, i) => {
    const q = ps[(i + 1) % ps.length] as XY;
    s += cross(p, q);
  });
  return s / 2;
}

/** The area of a ring in square metres. */
export function areaM2(ring: Point[]): number {
  const [o] = ring;
  if (!o || ring.length < 3) return 0;
  const pl = plane(o);
  return Math.abs(signedArea(ring.map(pl.xy)));
}

/** Douglas-Peucker on a line in metres: the points that keep it within tol of the original. */
function douglasPeucker(ps: XY[], tol: number): XY[] {
  if (ps.length < 3) return ps;
  const keep = new Array<boolean>(ps.length).fill(false);
  keep[0] = keep[ps.length - 1] = true;
  const stack: [number, number][] = [[0, ps.length - 1]];
  while (stack.length > 0) {
    const [i, j] = stack.pop() as [number, number];
    let far = -1;
    let most = tol;
    for (let k = i + 1; k < j; k++) {
      const d = segmentDistance(ps[k] as XY, ps[i] as XY, ps[j] as XY);
      if (d > most) {
        most = d;
        far = k;
      }
    }
    if (far >= 0) {
      keep[far] = true;
      stack.push([i, far], [far, j]);
    }
  }
  return ps.filter((_, k) => keep[k]);
}

/** Simplifies an open line to within tol metres of the original (Douglas-Peucker). */
export function simplify(line: Point[], tol: number): Point[] {
  const [o] = line;
  if (!o) return [];
  const pl = plane(o);
  return douglasPeucker(line.map(pl.xy), tol).map(pl.point);
}

/** The most corners a freehand stroke becomes. */
export const maxFreehandCorners = 24;

/** Why a freehand stroke makes no plot, in the farmer's words. */
export const strokeTooSmall = "That is too small for a plot: draw right round the field.";
export const strokeCrosses =
  "The line crosses itself: draw round the field once, without crossing your own line, or use the lines tool.";

/** Whether two edges of a ring (not neighbours) cross. */
function crossesItself(ps: XY[]): boolean {
  const n = ps.length;
  const side = (a: XY, b: XY, c: XY) => Math.sign(cross(sub(b, a), sub(c, a)));
  for (let i = 0; i < n; i++)
    for (let j = i + 2; j < n; j++) {
      if (i === 0 && j === n - 1) continue;
      const a = ps[i] as XY;
      const b = ps[(i + 1) % n] as XY;
      const c = ps[j] as XY;
      const d = ps[(j + 1) % n] as XY;
      if (side(a, b, c) * side(a, b, d) < 0 && side(c, d, a) * side(c, d, b) < 0) return true;
    }
  return false;
}

/**
 * Turns a freehand stroke into a plot's corners (see freehandRing);
 * undefined when it makes no plot.
 */
export function freehand(stroke: Point[], maxCorners = maxFreehandCorners): Point[] | undefined {
  return freehandRing(stroke, maxCorners).ring;
}

/**
 * Turns a freehand stroke into a plot's corners: the stroke is closed back
 * to where it started and simplified (Douglas-Peucker) to within a few per
 * cent of its size, coarser until it has at most maxCorners. No ring, and
 * the problem to tell the farmer, when the stroke is too short or too
 * small to be a plot, or its loop crosses itself (the server refuses such
 * a boundary, so the farmer is told at once).
 */
export function freehandRing(stroke: Point[], maxCorners = maxFreehandCorners): { ring?: Point[]; problem?: string } {
  const small = { problem: strokeTooSmall };
  const [o] = stroke;
  if (!o) return small;
  const pl = plane(o);
  // Drop jitter: points closer than half a metre to the last one kept.
  const ps: XY[] = [];
  for (const p of stroke.map(pl.xy)) {
    const last = ps[ps.length - 1];
    if (!last || len(sub(p, last)) >= 0.5) ps.push(p);
  }
  if (ps.length < 3) return small;
  const xs = ps.map((p) => p.x);
  const ys = ps.map((p) => p.y);
  const diag = Math.hypot(Math.max(...xs) - Math.min(...xs), Math.max(...ys) - Math.min(...ys));
  if (diag < 3) return small;
  // Close it: the line runs back to its start, so the simplification
  // keeps the corners of the whole loop.
  const loop = [...ps, ps[0] as XY];
  let tol = Math.min(20, Math.max(0.5, diag * 0.02));
  let out = douglasPeucker(loop, tol).slice(0, -1);
  while (out.length > maxCorners) {
    tol *= 1.5;
    out = douglasPeucker(loop, tol).slice(0, -1);
  }
  // A stroke that ends near its start does not leave a corner there.
  const first = out[0] as XY;
  const last = out[out.length - 1] as XY;
  if (out.length > 3 && len(sub(last, first)) < tol) out.pop();
  if (crossesItself(out)) return { problem: strokeCrosses }; // a bow tie's lobes cancel out: say why first
  if (out.length < 3 || Math.abs(signedArea(out)) < 1) return small;
  return { ring: out.map(pl.point) };
}

/** A rectangle on the ground: its centre, the bearing of its length (degrees from north), and its sides in metres. */
export interface Rect {
  center: Point;
  bearing: number;
  length: number;
  width: number;
}

/** The handles of a rectangle: one turns it, one stretches its length, one its width. */
export type RectHandle = "turn" | "length" | "width";

/** The smallest side a rectangle can have, in metres. */
const minSide = 1;

function axes(bearing: number): { u: XY; v: XY } {
  const b = bearing * rad;
  return { u: { x: Math.sin(b), y: Math.cos(b) }, v: { x: Math.cos(b), y: -Math.sin(b) } };
}

const at = (c: XY, ...terms: [XY, number][]): XY =>
  terms.reduce((p, [d, k]) => ({ x: p.x + d.x * k, y: p.y + d.y * k }), c);

/** The rectangle dragged from corner a to corner b, north up; undefined when a side is under 2 m. */
export function rectFromDrag(a: Point, b: Point): Rect | undefined {
  const pl = plane(a);
  const q = pl.xy(b);
  if (Math.abs(q.x) < 2 || Math.abs(q.y) < 2) return undefined;
  return { center: pl.point({ x: q.x / 2, y: q.y / 2 }), bearing: 0, length: Math.abs(q.y), width: Math.abs(q.x) };
}

/** A rectangle's four corners, clockwise from the front left. */
export function rectCorners(r: Rect): Point[] {
  const pl = plane(r.center);
  const { u, v } = axes(r.bearing);
  const c = { x: 0, y: 0 };
  const l = r.length / 2;
  const w = r.width / 2;
  return [at(c, [u, l], [v, -w]), at(c, [u, l], [v, w]), at(c, [u, -l], [v, w]), at(c, [u, -l], [v, -w])].map(pl.point);
}

/** Where a rectangle's handles are: turn beyond its front, length on its front edge, width on its right edge. */
export function rectHandles(r: Rect): Record<RectHandle, Point> {
  const pl = plane(r.center);
  const { u, v } = axes(r.bearing);
  const c = { x: 0, y: 0 };
  return {
    turn: pl.point(at(c, [u, r.length / 2 + Math.max(6, r.length * 0.25)])),
    length: pl.point(at(c, [u, r.length / 2])),
    width: pl.point(at(c, [v, r.width / 2])),
  };
}

/**
 * Moves one of a rectangle's handles to a point: turn points the
 * rectangle's length at it; length and width move that edge, the opposite
 * edge staying where it is.
 */
export function dragRect(r: Rect, h: RectHandle, to: Point): Rect {
  const pl = plane(r.center);
  const p = pl.xy(to);
  const { u, v } = axes(r.bearing);
  const c = { x: 0, y: 0 };
  switch (h) {
    case "turn": {
      if (len(p) < 0.5) return r;
      const b = Math.atan2(p.x, p.y) / rad;
      return { ...r, bearing: Math.round(((b % 360) + 360) % 360) };
    }
    case "length": {
      const back = at(c, [u, -r.length / 2]);
      const l = Math.max(minSide, dot(sub(p, back), u));
      return { ...r, length: l, center: pl.point(at(back, [u, l / 2])) };
    }
    case "width": {
      const left = at(c, [v, -r.width / 2]);
      const w = Math.max(minSide, dot(sub(p, left), v));
      return { ...r, width: w, center: pl.point(at(left, [v, w / 2])) };
    }
  }
}

// --- Areas inside a plot -------------------------------------------------

/** The smallest piece worth keeping as an area, in square metres. */
const minPiece = 1;

function containsXY(ps: XY[], q: XY): boolean {
  let inside = false;
  ps.forEach((a, i) => {
    const b = ps[(i + 1) % ps.length] as XY;
    if (a.y > q.y !== b.y > q.y && q.x < a.x + ((q.y - a.y) * (b.x - a.x)) / (b.y - a.y)) inside = !inside;
  });
  return inside;
}

interface Crossing {
  edge: number;
  t: number;
  p: XY;
}

/** Where the line through a and b crosses the ring's edges, in order along the line. */
function crossings(ps: XY[], a: XY, b: XY): Crossing[] {
  const d = sub(b, a);
  // A corner exactly on the line counts as on its left: no edge then
  // touches the line without crossing it.
  const side = (p: XY) => cross(d, sub(p, a)) >= 0;
  const out: Crossing[] = [];
  ps.forEach((p, i) => {
    const q = ps[(i + 1) % ps.length] as XY;
    if (side(p) === side(q)) return;
    const e = sub(q, p);
    const den = cross(d, e);
    if (den === 0) return;
    const s = cross(sub(p, a), d) / den;
    const x = at(p, [e, Math.max(0, Math.min(1, s))]);
    out.push({ edge: i, t: dot(sub(x, a), d) / dot(d, d), p: x });
  });
  return out.sort((x, y) => x.t - y.t);
}

/**
 * Cuts a ring in two along the line from a to b. The line is taken across
 * the stretch of the ring it is drawn over (for a ring it crosses more
 * than twice, the stretch it covers most); it must cover at least half of
 * that stretch. Undefined when it does not cross the ring or a piece would
 * be under a square metre.
 */
export function splitByLine(ring: Point[], a: Point, b: Point): [Point[], Point[]] | undefined {
  const [o] = ring;
  if (!o || ring.length < 3) return undefined;
  const pl = plane(o);
  const ps = ring.map(pl.xy);
  const pa = pl.xy(a);
  const pb = pl.xy(b);
  if (len(sub(pb, pa)) < 0.5) return undefined;
  const cs = crossings(ps, pa, pb);
  let best: [Crossing, Crossing] | undefined;
  let most = 0;
  for (let k = 0; k + 1 < cs.length; k += 2) {
    const c1 = cs[k] as Crossing;
    const c2 = cs[k + 1] as Crossing;
    const covered = (Math.min(c2.t, 1) - Math.max(c1.t, 0)) / (c2.t - c1.t);
    if (covered > most) {
      most = covered;
      best = [c1, c2];
    }
  }
  if (!best || most < 0.5) return undefined;
  const [c1, c2] = best[0].edge <= best[1].edge ? best : [best[1], best[0]];
  const n = ps.length;
  const one = [c1.p, ...ps.slice(c1.edge + 1, c2.edge + 1), c2.p];
  const two = [c2.p, ...ps.slice(c2.edge + 1), ...ps.slice(0, c1.edge + 1), c1.p];
  if (n < 3 || Math.abs(signedArea(one)) < minPiece || Math.abs(signedArea(two)) < minPiece) return undefined;
  return [clean(one).map(pl.point), clean(two).map(pl.point)];
}

/** Drops repeated points (closer than a centimetre). */
function clean(ps: XY[]): XY[] {
  const out = ps.filter((p, i) => len(sub(p, ps[(i + 1) % ps.length] as XY)) > 0.01);
  return out.length >= 3 ? out : ps;
}

// Greiner-Hormann clipping of two simple rings: their intersection, or the
// first less the second. A corner of one exactly on an edge of the other
// is moved by a millimetre first, which it does not handle.

interface Node {
  p: XY;
  next: Node;
  prev: Node;
  inter: boolean;
  entry: boolean;
  visited: boolean;
  neighbor?: Node;
  alpha: number;
}

function node(p: XY, inter = false, alpha = 0): Node {
  const n = { p, inter, alpha, entry: false, visited: false } as Node;
  n.next = n.prev = n;
  return n;
}

function link(ns: Node[]): Node {
  ns.forEach((n, i) => {
    n.next = ns[(i + 1) % ns.length] as Node;
    n.prev = ns[(i + ns.length - 1) % ns.length] as Node;
  });
  return ns[0] as Node;
}

const eps = 1e-9;

/** Whether some corner of a lies on an edge of b (within a millimetre), which clipping cannot take. */
function touches(a: XY[], b: XY[]): boolean {
  return a.some((p) => b.some((q, i) => segmentDistance(p, q, b[(i + 1) % b.length] as XY) < 1e-3));
}

type Clipped = XY[][] | "hole";

function clip(s: XY[], c: XY[], op: "and" | "minus"): Clipped {
  // Nudge c off s's corners, and s's corners off c's edges.
  for (let k = 1; k <= 8 && (touches(s, c) || touches(c, s)); k++) {
    c = c.map((p) => ({ x: p.x + 0.0007 * k, y: p.y + 0.0011 * k }));
  }
  const sInter: Node[][] = s.map(() => []);
  const cInter: Node[][] = c.map(() => []);
  s.forEach((s1, i) => {
    const s2 = s[(i + 1) % s.length] as XY;
    const ds = sub(s2, s1);
    c.forEach((c1, j) => {
      const c2 = c[(j + 1) % c.length] as XY;
      const dc = sub(c2, c1);
      const den = cross(ds, dc);
      if (Math.abs(den) < eps) return;
      const a = cross(sub(c1, s1), dc) / den;
      const b = cross(sub(c1, s1), ds) / den;
      if (a <= 0 || a >= 1 || b <= 0 || b >= 1) return;
      const p = at(s1, [ds, a]);
      const ns = node(p, true, a);
      const nc = node(p, true, b);
      ns.neighbor = nc;
      nc.neighbor = ns;
      sInter[i]?.push(ns);
      cInter[j]?.push(nc);
    });
  });
  const build = (ps: XY[], inter: Node[][]) =>
    link(ps.flatMap((p, i) => [node(p), ...(inter[i] ?? []).sort((x, y) => x.alpha - y.alpha)]));
  const hs = build(s, sInter);
  const hc = build(c, cInter);
  if (sInter.every((x) => x.length === 0)) {
    const sInC = containsXY(c, s[0] as XY);
    const cInS = containsXY(s, c[0] as XY);
    if (op === "and") return sInC ? [s] : cInS ? [c] : [];
    return sInC ? [] : cInS ? "hole" : [s];
  }
  const mark = (h: Node, other: XY[], flip: boolean) => {
    let entry = !containsXY(other, h.p) !== flip;
    let n = h;
    do {
      if (n.inter) {
        n.entry = entry;
        entry = !entry;
      }
      n = n.next;
    } while (n !== h);
  };
  mark(hs, c, op === "minus");
  mark(hc, s, false);
  const out: XY[][] = [];
  for (let start = hs.next; start !== hs; start = start.next) {
    if (!start.inter || start.visited) continue;
    const ring: XY[] = [start.p];
    let cur = start;
    do {
      cur.visited = true;
      if (cur.neighbor) cur.neighbor.visited = true;
      const forward = cur.entry;
      do {
        cur = forward ? cur.next : cur.prev;
        ring.push(cur.p);
      } while (!cur.inter);
      cur = cur.neighbor as Node;
    } while (!cur.visited);
    ring.pop();
    const r = clean(ring);
    if (r.length >= 3 && Math.abs(signedArea(r)) >= minPiece) out.push(r);
  }
  return out;
}

/**
 * The line across ps through the middle of shape, east to west: cutting
 * along it leaves no piece of ps with shape as a hole.
 */
function cutThrough(ps: XY[], shape: XY[]): [XY[], XY[]] | undefined {
  const ys = shape.map((p) => p.y);
  let y = (Math.min(...ys) + Math.max(...ys)) / 2;
  if (shape.some((p) => Math.abs(p.y - y) < 1e-3)) y += 0.0123;
  const xs = ps.map((p) => p.x);
  const a = { x: Math.min(...xs) - 1, y };
  const b = { x: Math.max(...xs) + 1, y };
  const cs = crossings(ps, a, b);
  // The stretch of ps the shape is in.
  const mid = shape.find((p) => p.y > y) as XY;
  const sx = crossings(shape, a, b);
  const inside = sx[0]?.p.x ?? mid.x;
  for (let k = 0; k + 1 < cs.length; k += 2) {
    const c1 = cs[k] as Crossing;
    const c2 = cs[k + 1] as Crossing;
    if (c1.p.x <= inside && inside <= c2.p.x) {
      const [d1, d2] = c1.edge <= c2.edge ? [c1, c2] : [c2, c1];
      return [
        [d1.p, ...ps.slice(d1.edge + 1, d2.edge + 1), d2.p],
        [d2.p, ...ps.slice(d2.edge + 1), ...ps.slice(0, d1.edge + 1), d1.p],
      ];
    }
  }
  return undefined;
}

/**
 * Cuts the areas of a plot along the line from a to b: every area the line
 * is drawn across is cut in two. Undefined when it crosses none.
 */
export function cutAreas(areas: Point[][], a: Point, b: Point): Point[][] | undefined {
  let cut = false;
  const out = areas.flatMap((r) => {
    const two = splitByLine(r, a, b);
    if (!two) return [r];
    cut = true;
    return two;
  });
  return cut ? out : undefined;
}

/**
 * Adds a shape drawn over a plot's areas: the part of each area inside the
 * shape becomes an area of its own, the rest stays. An area the shape
 * lies wholly inside is first cut across the shape, so no area has a hole
 * (the plan's zones are simple rings). Undefined when the shape covers no
 * area.
 */
export function addShape(areas: Point[][], shape: Point[]): Point[][] | undefined {
  const o = areas[0]?.[0];
  if (!o || shape.length < 3) return undefined;
  const pl = plane(o);
  const sh = shape.map(pl.xy);
  const rest: XY[][] = [];
  const pieces: XY[][] = [];
  for (const r of areas.map((x) => x.map(pl.xy))) {
    const inside = clip(r, sh, "and");
    if (inside === "hole" || inside.length === 0) {
      rest.push(r);
      continue;
    }
    pieces.push(...inside);
    let left = clip(r, sh, "minus");
    if (left === "hole") {
      const halves = cutThrough(r, sh);
      if (!halves) return undefined;
      left = halves.flatMap((h) => {
        const l = clip(h, sh, "minus");
        return l === "hole" ? [h] : l;
      });
    }
    rest.push(...left);
  }
  if (pieces.length === 0) return undefined;
  return [...rest, ...pieces].map((r) => r.map(pl.point));
}
