import { describe, expect, it } from "vitest";
import { addPart, colors, formatDate, partsOf, scoreTone, shapes } from "./plan";
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
