import { describe, expect, it } from "vitest";
import {
  addPart,
  areaParts,
  areaPlan,
  areasOf,
  colors,
  fillArea,
  formatDate,
  partsOf,
  scoreTone,
  shapes,
} from "./plan";
import { cutAreas, plane } from "./draw";
import type { Crop, Plan, PlanReport } from "./types";

const crop = (id: string, name: string): Crop => ({
  id,
  name,
  family: "",
  kind: "",
  perennial: false,
  days: [90, 120],
  row_cm: 60,
  plant_cm: 20,
  seasons: [],
  grown_in: [],
  climate_analog: false,
  tall: false,
  height_m: 0.5,
  water_mm: [300, 500],
});
const crops = [crop("cotton", "Cotton"), crop("green_gram", "Green gram"), crop("soybean", "Soybean")];
const ring = [
  { lat: 17, lng: 78 },
  { lat: 17, lng: 78.001 },
  { lat: 17.001, lng: 78.001 },
];
const plan: Plan = {
  zones: [
    { crop: "cotton", with: "green_gram", pattern: [2, 1], ring, row_bearing: 0, sow: "2027-06-10" },
    { crop: "soybean", ring, row_bearing: 0, sow: "2027-06-20" },
  ],
};

describe("plan helpers", () => {
  it("draws each zone with its crops and a colour of its own", () => {
    const z = shapes(plan, crops);
    expect(z.map((s) => s.label)).toEqual(["Cotton + Green gram", "Soybean"]);
    expect(z[0]?.color).toBe(colors[0]);
    expect(z[1]?.color).not.toBe(z[0]?.color);
    expect(shapes(undefined, crops)).toEqual([]);
  });

  it("turns a saved plan back into parts with the report's shares", () => {
    const report: PlanReport = {
      status: "sound",
      checks: [],
      used_m2: 100,
      plot_m2: 100,
      zones: [
        { area_m2: 60, share: 0.6, length_m: 1, width_m: 1, rows: 1, plants: 1 },
        { area_m2: 40, share: 0.4, length_m: 1, width_m: 1, rows: 1, plants: 1 },
      ],
    };
    expect(partsOf(plan, report)).toEqual([
      { crop: "cotton", with: "green_gram", pattern: [2, 1], share: 0.6, sow: "2027-06-10" },
      { crop: "soybean", with: undefined, pattern: undefined, share: 0.4, sow: "2027-06-20" },
    ]);
    expect(partsOf(plan, undefined).map((p) => p.share)).toEqual([0.5, 0.5]);
  });

  it("grades scores and writes dates without shifting the day", () => {
    expect([scoreTone(85), scoreTone(60), scoreTone(45), scoreTone(10)]).toEqual(["ok", "ok", "warn", "bad"]);
    expect(formatDate("2027-06-01")).toContain("2027");
    expect(formatDate("2027-06-01")).toMatch(/1/);
    expect(formatDate("soon")).toBe("soon");
  });
});

describe("addPart", () => {
  const sow = "2027-06-01";
  it("shares the plot equally, so the shares never add up past the whole plot", () => {
    let ps = addPart([], { crop: "bajra", share: 1, sow });
    expect(ps.map((p) => p.share)).toEqual([1]);
    ps = addPart(ps, { crop: "moong", share: 1, sow });
    expect(ps.map((p) => p.share)).toEqual([0.5, 0.5]);
    ps = addPart(ps, { crop: "urad", share: 1, sow });
    expect(ps.map((p) => p.share)).toEqual([0.33, 0.33, 0.34]);
    expect(ps.reduce((s, p) => s + p.share, 0)).toBeCloseTo(1);
  });
  it("replaces a crop already in the plan rather than adding it twice", () => {
    const ps = addPart([{ crop: "bajra", share: 1, sow }], { crop: "bajra", share: 1, sow: "2027-07-01" });
    expect(ps).toEqual([{ crop: "bajra", share: 1, sow: "2027-07-01" }]);
  });
});

describe("areas as the plan's zones", () => {
  const pl = plane({ lat: 16.29, lng: 80.45 });
  const plot = [
    pl.point({ x: 0, y: 0 }),
    pl.point({ x: 100, y: 0 }),
    pl.point({ x: 100, y: 100 }),
    pl.point({ x: 0, y: 100 }),
  ];
  const areas = cutAreas([plot], pl.point({ x: 25, y: -5 }), pl.point({ x: 25, y: 105 })) ?? [];

  it("gives each area a part with its share of the plot, the first area's crop for a new one", () => {
    const parts = areaParts(areas, [{ crop: "cotton", share: 1, sow: "2027-06-10" }], plot);
    expect(parts.map((p) => p.share).sort()).toEqual([0.25, 0.75]);
    expect(parts.every((p) => p.crop === "cotton" && p.sow === "2027-06-10")).toBe(true);
  });

  it("makes a zone of each area with a crop and a date, its ring the area, and leaves the rest unplanned", () => {
    const p = areaPlan(areas, [
      { crop: "cotton", share: 0.25, sow: "2027-06-10" },
      { crop: "", share: 0.75, sow: "" },
    ]);
    expect(p.zones).toHaveLength(1);
    expect(p.zones[0]?.ring).toEqual(areas[0]);
    expect(p.zones[0]?.row_bearing).toBe(0);
  });

  it("puts a suggested crop in the first area without one, else the last", () => {
    const ps = [
      { crop: "cotton", share: 0.5, sow: "2027-06-10" },
      { crop: "", share: 0.5, sow: "" },
    ];
    const once = fillArea(ps, { crop: "soybean", share: 1, sow: "2027-06-20" });
    expect(once.map((p) => p.crop)).toEqual(["cotton", "soybean"]);
    expect(once[1]?.share).toBe(0.5);
    expect(fillArea(once, { crop: "green_gram", share: 1, sow: "2027-06-21" }).map((p) => p.crop)).toEqual([
      "cotton",
      "green_gram",
    ]);
  });

  it("reads a saved plan's zones back as areas only when they cover the plot", () => {
    const whole = areaPlan(areas, [
      { crop: "cotton", share: 0.25, sow: "2027-06-10" },
      { crop: "soybean", share: 0.75, sow: "2027-06-10" },
    ]);
    expect(areasOf(whole, plot)).toHaveLength(2);
    // Strips that leave part of the plot unplanned are not areas.
    const strip = whole.zones[0];
    if (!strip) throw new Error("no zone");
    expect(areasOf({ zones: [strip, strip] }, plot)).toBeUndefined();
    expect(areasOf(undefined, plot)).toBeUndefined();
  });
});
