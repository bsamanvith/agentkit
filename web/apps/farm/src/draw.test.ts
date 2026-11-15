import { describe, expect, it } from "vitest";
import {
  addShape,
  areaM2,
  cutAreas,
  dragRect,
  freehand,
  maxFreehandCorners,
  plane,
  rectCorners,
  rectFromDrag,
  rectHandles,
  simplify,
  splitByLine,
  type XY,
} from "./draw";
import type { Point } from "./types";

// A field near Guntur; shapes are given in metres east and north of it.
const origin = { lat: 16.29, lng: 80.45 };
const pl = plane(origin);
const pts = (xys: [number, number][]): Point[] => xys.map(([x, y]) => pl.point({ x, y }));
const m = (p: Point): XY => pl.xy(p);
const square = pts([
  [0, 0],
  [100, 0],
  [100, 100],
  [0, 100],
]);

/** Whether two edges of a ring cross (the server rejects such a zone). */
function selfCrosses(r: Point[]): boolean {
  const ps = r.map(m);
  const n = ps.length;
  const seg = (i: number) => [ps[i] as XY, ps[(i + 1) % n] as XY] as const;
  const orient = (a: XY, b: XY, c: XY) => Math.sign((b.x - a.x) * (c.y - a.y) - (b.y - a.y) * (c.x - a.x));
  for (let i = 0; i < n; i++)
    for (let j = i + 2; j < n; j++) {
      if (i === 0 && j === n - 1) continue;
      const [a, b] = seg(i);
      const [c, d] = seg(j);
      if (orient(a, b, c) * orient(a, b, d) < 0 && orient(c, d, a) * orient(c, d, b) < 0) return true;
    }
  return false;
}

function inside(r: Point[], p: XY): boolean {
  const ps = r.map(m);
  let in_ = false;
  ps.forEach((a, i) => {
    const b = ps[(i + 1) % ps.length] as XY;
    if (a.y > p.y !== b.y > p.y && p.x < a.x + ((p.y - a.y) * (b.x - a.x)) / (b.y - a.y)) in_ = !in_;
  });
  return in_;
}

/** Areas that tile the plot: each simple, none overlapping, together the whole plot (sampled on a grid). */
function expectTiles(areas: Point[][], plot: Point[]) {
  for (const a of areas) expect(selfCrosses(a)).toBe(false);
  const total = areas.reduce((s, a) => s + areaM2(a), 0);
  expect(total).toBeCloseTo(areaM2(plot), -1);
  const ps = plot.map(m);
  const xs = ps.map((p) => p.x);
  const ys = ps.map((p) => p.y);
  for (let i = 0; i < 20; i++)
    for (let j = 0; j < 20; j++) {
      const q = {
        x: Math.min(...xs) + ((i + 0.37) * (Math.max(...xs) - Math.min(...xs))) / 20,
        y: Math.min(...ys) + ((j + 0.41) * (Math.max(...ys) - Math.min(...ys))) / 20,
      };
      const hits = areas.filter((a) => inside(a, q)).length;
      expect([q.x, q.y, hits]).toEqual([q.x, q.y, inside(plot, q) ? 1 : 0]);
    }
}

describe("simplify", () => {
  it("keeps the corners of a line and drops the points along its straight runs", () => {
    const line = pts([
      [0, 0],
      [25, 0.2],
      [50, -0.1],
      [100, 0],
      [100, 50],
      [100.3, 100],
    ]);
    const s = simplify(line, 1).map(m);
    expect(s).toHaveLength(3);
    expect(s[1]?.x).toBeCloseTo(100, 1);
    expect(s[1]?.y).toBeCloseTo(0, 1);
  });
});

describe("freehand", () => {
  // A hand going round a 60 by 40 m field: a point every metre, wobbling.
  const stroke: Point[] = [];
  const wobble = (k: number) => Math.sin(k * 1.7) * 0.4;
  for (let x = 0; x < 60; x++) stroke.push(pl.point({ x, y: wobble(x) }));
  for (let y = 0; y < 40; y++) stroke.push(pl.point({ x: 60 + wobble(y), y }));
  for (let x = 60; x > 0; x--) stroke.push(pl.point({ x, y: 40 + wobble(x) }));
  for (let y = 40; y > 3; y--) stroke.push(pl.point({ x: wobble(y), y }));

  it("smooths a wobbly loop to its four corners and closes it", () => {
    const ring = freehand(stroke);
    expect(ring).toBeDefined();
    const cs = (ring ?? []).map(m);
    expect(cs).toHaveLength(4);
    for (const [x, y] of [
      [0, 0],
      [60, 0],
      [60, 40],
      [0, 40],
    ] as const)
      expect(cs.some((c) => Math.hypot(c.x - x, c.y - y) < 2)).toBe(true);
    expect(areaM2(ring ?? [])).toBeGreaterThan(2300);
    expect(areaM2(ring ?? [])).toBeLessThan(2500);
  });

  it("keeps a round field to a sensible number of corners", () => {
    const circle = Array.from({ length: 720 }, (_, k) =>
      pl.point({ x: 50 * Math.cos((k * Math.PI) / 360), y: 50 * Math.sin((k * Math.PI) / 360) }),
    );
    const ring = freehand(circle) ?? [];
    expect(ring.length).toBeGreaterThanOrEqual(8);
    expect(ring.length).toBeLessThanOrEqual(maxFreehandCorners);
    expect(selfCrosses(ring)).toBe(false);
  });

  it("caps a jagged stroke at the most corners a freehand plot gets", () => {
    // A 200 m square whose edges zig-zag 10 m in and out every 10 m.
    const jag: Point[] = [];
    for (let k = 0; k < 80; k++) {
      const side = Math.floor(k / 20);
      const t = (k % 20) * 10;
      const out = k % 2 === 0 ? 0 : 10;
      const [x, y] = [
        [t, -out],
        [200 + out, t],
        [200 - t, 200 + out],
        [-out, 200 - t],
      ][side] as [number, number];
      jag.push(pl.point({ x, y }));
    }
    const ring = freehand(jag) ?? [];
    expect(ring.length).toBeGreaterThanOrEqual(4);
    expect(ring.length).toBeLessThanOrEqual(maxFreehandCorners);
  });

  it("refuses a tap or a scribble too small to be a plot", () => {
    expect(freehand([origin])).toBeUndefined();
    expect(
      freehand(
        pts([
          [0, 0],
          [1, 0],
          [1, 1],
        ]),
      ),
    ).toBeUndefined();
  });
});

describe("rectangle", () => {
  it("is the box dragged, north up", () => {
    const r = rectFromDrag(pl.point({ x: 0, y: 0 }), pl.point({ x: 30, y: 80 }));
    expect(r).toBeDefined();
    if (!r) return;
    expect(r.length).toBeCloseTo(80, 1);
    expect(r.width).toBeCloseTo(30, 1);
    expect(areaM2(rectCorners(r))).toBeCloseTo(2400, -1);
    expect(rectFromDrag(origin, pl.point({ x: 30, y: 1 }))).toBeUndefined();
  });

  it("turns to point its length at the turn handle, keeping its size", () => {
    const r = rectFromDrag(pl.point({ x: 0, y: 0 }), pl.point({ x: 30, y: 80 }));
    if (!r) throw new Error("no rectangle");
    const c = m(r.center);
    const turned = dragRect(r, "turn", pl.point({ x: c.x + 50, y: c.y }));
    expect(turned.bearing).toBe(90);
    expect(areaM2(rectCorners(turned))).toBeCloseTo(2400, -1);
    // The front edge now faces east.
    const front = m(rectHandles(turned).length);
    expect(front.x - c.x).toBeCloseTo(40, 0);
  });

  it("stretches one edge and keeps the opposite edge where it was", () => {
    const r = rectFromDrag(pl.point({ x: 0, y: 0 }), pl.point({ x: 30, y: 80 }));
    if (!r) throw new Error("no rectangle");
    const longer = dragRect(r, "length", pl.point({ x: 15, y: 120 }));
    expect(longer.length).toBeCloseTo(120, 0);
    const ys = rectCorners(longer).map((p) => m(p).y);
    expect(Math.min(...ys)).toBeCloseTo(0, 0);
    expect(Math.max(...ys)).toBeCloseTo(120, 0);
    const wider = dragRect(r, "width", pl.point({ x: 50, y: 40 }));
    const xs = rectCorners(wider).map((p) => m(p).x);
    expect(Math.min(...xs)).toBeCloseTo(0, 0);
    expect(Math.max(...xs)).toBeCloseTo(50, 0);
    // Never inside out.
    expect(dragRect(r, "width", pl.point({ x: -40, y: 40 })).width).toBeGreaterThan(0);
  });
});

describe("splitByLine", () => {
  it("cuts a plot in two along a line drawn across it", () => {
    const two = splitByLine(square, pl.point({ x: 30, y: -10 }), pl.point({ x: 30, y: 110 }));
    expect(two).toBeDefined();
    const [a, b] = two ?? [[], []];
    expect([areaM2(a), areaM2(b)].sort((x, y) => x - y)).toEqual([expect.closeTo(3000, -1), expect.closeTo(7000, -1)]);
    expectTiles([a, b], square);
  });

  it("takes a line drawn most of the way across, but not one that misses or only nicks the plot", () => {
    expect(splitByLine(square, pl.point({ x: 50, y: 20 }), pl.point({ x: 50, y: 100 }))).toBeDefined();
    expect(splitByLine(square, pl.point({ x: 50, y: 80 }), pl.point({ x: 50, y: 120 }))).toBeUndefined();
    expect(splitByLine(square, pl.point({ x: 150, y: -10 }), pl.point({ x: 150, y: 110 }))).toBeUndefined();
  });

  it("cuts the arm of a U-shaped plot the line is drawn over, not the other", () => {
    const u = pts([
      [0, 0],
      [100, 0],
      [100, 100],
      [70, 100],
      [70, 30],
      [30, 30],
      [30, 100],
      [0, 100],
    ]);
    const two = splitByLine(u, pl.point({ x: -5, y: 60 }), pl.point({ x: 35, y: 60 }));
    expect(two).toBeDefined();
    const small = Math.min(...(two ?? []).map(areaM2));
    expect(small).toBeCloseTo(30 * 40, -1);
    expectTiles(two ?? [], u);
  });
});

describe("areas", () => {
  it("cuts every area a line is drawn across", () => {
    const halves = cutAreas([square], pl.point({ x: 50, y: -5 }), pl.point({ x: 50, y: 105 })) ?? [];
    expect(halves).toHaveLength(2);
    const quarters = cutAreas(halves, pl.point({ x: -5, y: 50 }), pl.point({ x: 105, y: 50 })) ?? [];
    expect(quarters).toHaveLength(4);
    for (const q of quarters) expect(areaM2(q)).toBeCloseTo(2500, -1);
    expectTiles(quarters, square);
    expect(cutAreas([square], pl.point({ x: 200, y: 0 }), pl.point({ x: 200, y: 100 }))).toBeUndefined();
  });

  it("makes a shape over the corner of the plot an area, clipped to the plot", () => {
    const shape = pts([
      [60, 60],
      [130, 60],
      [130, 130],
      [60, 130],
    ]);
    const areas = addShape([square], shape) ?? [];
    expect(areas).toHaveLength(2);
    expect(areaM2(areas[1] ?? [])).toBeCloseTo(1600, -1);
    expectTiles(areas, square);
  });

  it("makes a shape wholly inside the plot an area, and cuts the rest so no area has a hole", () => {
    const shape = pts([
      [30, 30],
      [60, 35],
      [55, 70],
      [35, 65],
    ]);
    const areas = addShape([square], shape) ?? [];
    expect(areas.length).toBe(3);
    expect(areaM2(areas[2] ?? [])).toBeCloseTo(areaM2(shape), 0);
    expectTiles(areas, square);
  });

  it("splits a shape drawn over two areas between them", () => {
    const halves = cutAreas([square], pl.point({ x: 50, y: -5 }), pl.point({ x: 50, y: 105 })) ?? [];
    const shape = pts([
      [40, 10],
      [60, 10],
      [60, 30],
      [40, 30],
    ]);
    const areas = addShape(halves, shape) ?? [];
    expect(areas).toHaveLength(4);
    expectTiles(areas, square);
  });

  it("ignores a shape drawn outside the plot", () => {
    const shape = pts([
      [200, 200],
      [230, 200],
      [230, 230],
    ]);
    expect(addShape([square], shape)).toBeUndefined();
  });

  it("copes with a shape whose corner lies exactly on the plot's edge", () => {
    const shape = pts([
      [0, 20],
      [40, 20],
      [40, 60],
      [0, 60],
    ]);
    const areas = addShape([square], shape) ?? [];
    expect(areas.length).toBeGreaterThanOrEqual(2);
    expectTiles(areas, square);
  });
});
