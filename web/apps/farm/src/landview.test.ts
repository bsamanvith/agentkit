import { describe, expect, it } from "vitest";
import {
  bounds,
  clipSegment,
  cropColor,
  designShapes,
  fitAspect,
  growth,
  metres,
  nearbyLabel,
  planSpan,
  projector,
  scaleLength,
  season,
  treesAlong,
} from "./landview";
import fixture from "./testdata/kothapeta.json";
import type { Design, Nearby, Zone } from "./types";

describe("projector", () => {
  it("puts north up and keeps metres within a few centimetres nearby", () => {
    const xy = projector({ lat: 16.7063, lng: 81.9008 });
    const north = xy({ lat: 16.7073, lng: 81.9008 }); // 0.001° north: about 110.5 m
    const east = xy({ lat: 16.7063, lng: 81.9018 }); // 0.001° east: about 106.6 m here
    expect(north.x).toBeCloseTo(0, 6);
    expect(north.y).toBeCloseTo(-110.54, 1);
    expect(east.x).toBeCloseTo(106.63, 0);
    expect(east.y).toBeCloseTo(0, 6);
  });
});

describe("view boxes", () => {
  it("grows bounds by the margin and fits an aspect about the centre", () => {
    const b = bounds(
      [
        { x: 0, y: 0 },
        { x: 100, y: 50 },
      ],
      10,
    );
    expect(b).toEqual({ x: -10, y: -10, w: 120, h: 70 });
    const f = fitAspect(b, 4 / 3);
    expect(f.w / f.h).toBeCloseTo(4 / 3);
    expect(f.x + f.w / 2).toBeCloseTo(50);
    expect(f.y + f.h / 2).toBeCloseTo(25);
    expect(f.w).toBeGreaterThanOrEqual(b.w);
    expect(f.h).toBeGreaterThanOrEqual(b.h);
  });

  it("clips a callout at the view's edge and drops one that misses it", () => {
    const r = { x: 0, y: 0, w: 100, h: 100 };
    const seg = clipSegment({ x: 50, y: 50 }, { x: 250, y: 50 }, r);
    expect(seg?.[0]).toEqual({ x: 50, y: 50 });
    expect(seg?.[1].x).toBeCloseTo(100);
    expect(clipSegment({ x: 150, y: 150 }, { x: 250, y: 50 }, r)).toBeUndefined();
    expect(clipSegment({ x: 10, y: 10 }, { x: 20, y: 20 }, r)).toEqual([
      { x: 10, y: 10 },
      { x: 20, y: 20 },
    ]);
  });

  it("picks a round scale bar near a fifth of the width", () => {
    expect(scaleLength(130)).toBe(20);
    expect(scaleLength(900)).toBe(100);
    expect(scaleLength(2600)).toBe(500);
    expect(metres(97.6)).toBe("98 m");
    expect(metres(1234)).toBe("1.2 km");
  });
});

describe("border trees", () => {
  it("stand every spacing metres, halfway into the band", () => {
    const outer = [
      { x: 0, y: 0 },
      { x: 40, y: 0 },
      { x: 40, y: 40 },
      { x: 0, y: 40 },
    ];
    const inner = [
      { x: 4, y: 4 },
      { x: 36, y: 4 },
      { x: 36, y: 36 },
      { x: 4, y: 36 },
    ];
    const trees = treesAlong(outer, inner, 4);
    expect(trees).toHaveLength(40); // 160 m round, one every 4 m
    for (const t of trees) {
      const fromEdge = Math.min(t.x, t.y, 40 - t.x, 40 - t.y);
      expect(fromEdge).toBeGreaterThan(0.9);
      expect(fromEdge).toBeLessThan(2.1);
    }
    expect(treesAlong(outer, inner, 4, 10)).toHaveLength(10);
    expect(treesAlong(outer, inner, 0)).toHaveLength(0);
  });
});

describe("the season", () => {
  const zone: Zone = { crop: "groundnut", ring: [], row_bearing: 0, sow: "2027-06-20" };
  const crops = [{ id: "groundnut", days: [100, 120] }] as never;

  it("grows a crop from sowing to harvest and colours it earth, green, gold", () => {
    const s = season(zone, crops);
    expect((s.harvest - s.sow) / 86_400_000).toBe(110);
    expect(growth(s.sow - 1, s)).toBe(0);
    expect(growth((s.sow + s.harvest) / 2, s)).toBeCloseTo(0.5);
    expect(growth(s.harvest + 1e9, s)).toBe(1);
    expect(cropColor(0)).toBe("#b08d68");
    expect(cropColor(0.6)).toBe("rgb(34 139 34)");
    expect(cropColor(1)).toBe("rgb(214 174 54)");
    const span = planSpan([zone], crops)!;
    expect(span.from).toBeLessThan(s.sow);
    expect(span.to).toBeGreaterThan(s.harvest);
    expect(planSpan([], crops)).toBeUndefined();
  });

  it("assumes 120 days for a crop it does not know", () => {
    const s = season({ ...zone, crop: "mystery" }, crops);
    expect((s.harvest - s.sow) / 86_400_000).toBe(120);
  });
});

describe("labels", () => {
  it("says where a feature is in a few words", () => {
    const n = (fixture.survey.nearest as Nearby[]).find((x) => x.kind === "drain")!;
    expect(nearbyLabel(n)).toBe("Drain 98 m E");
    expect(nearbyLabel({ ...n, touches: true })).toBe("Drain along the E side");
    expect(nearbyLabel({ ...n, crosses: true, name: "Pedda kaluva" })).toBe("Pedda kaluva crosses the plot");
    expect(nearbyLabel({ ...n, inside: true, name: "Kothapeta" })).toBe("Inside Kothapeta");
  });

  it("draws a layout's pieces on the satellite map, not the border band", () => {
    const shapes = designShapes(fixture.design as Design);
    expect(shapes.map((s) => s.label)).toEqual(["", "Shed", "Farm pond"]);
    expect(designShapes(undefined)).toEqual([]);
  });
});
