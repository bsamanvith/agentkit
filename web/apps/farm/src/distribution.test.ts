import { describe, expect, it } from "vitest";
import { plantColor, plantID, problems, shareFor, sortEffects, templates, withPlace } from "./distribution";
import type { Effect } from "./types";

describe("plant groups", () => {
  it("makes ids from names, apart from the ones taken", () => {
    expect(plantID("Fruit trees", [])).toBe("fruit-trees");
    expect(plantID("Fruit trees", ["fruit-trees", "fruit-trees-2"])).toBe("fruit-trees-3");
    expect(plantID("  ", [])).toBe("plants");
    expect(plantID("Kūrагаялу", [])).toMatch(/^[a-z0-9-]+$/);
  });

  it("colours each group by its place in the distribution", () => {
    const d = {
      plants: [
        { id: "a", name: "A", crops: [] },
        { id: "b", name: "B", crops: [] },
      ],
    };
    expect(plantColor(d, "b")).not.toBe(plantColor(d, "a"));
    expect(plantColor(d, "nope")).toBe(plantColor(d, "a"));
  });

  it("keeps a share for blocks and widths for borders and tree rows", () => {
    const p = { id: "t", name: "Trees", crops: ["mango"], place: "block" as const, share: 3 };
    expect(withPlace(p, "border")).toEqual({ id: "t", name: "Trees", crops: ["mango"], place: "border", width_m: 4 });
    expect(withPlace(withPlace(p, "alley"), "block")).toEqual({ ...p, share: 1 });
  });

  it("says what the server would refuse", () => {
    expect(problems({ plants: [{ id: "b", name: "B", crops: ["mango"], place: "border" }] })).toEqual([
      "Give at least one plant group a block of land.",
    ]);
    expect(problems({ plants: [{ id: "v", name: "Veg", crops: [] }] })).toEqual(["Pick a crop for Veg."]);
    expect(problems({ plants: [{ id: "v", name: "Veg", crops: ["tomato"] }] })).toEqual([]);
  });

  it("starts from templates with only the crops the library has", () => {
    const have = new Set(["mango", "tomato", "green_gram"]);
    const d = templates.find((t) => t.id === "orchard-border")!.make(have);
    expect(d.plants.map((p) => p.crops)).toEqual([["mango"], ["tomato"], ["green_gram"]]);
    for (const t of templates)
      expect(
        problems(
          t.make(
            new Set([
              "mango",
              "tomato",
              "onion",
              "green_gram",
              "moringa",
              "black_gram",
              "sorghum",
              "pigeon_pea",
              "rice",
              "groundnut",
            ]),
          ),
        ),
      ).toEqual([]);
  });

  it("puts problems first, then shade before water", () => {
    const e = (id: string, kind: Effect["kind"], severity: Effect["severity"]): Effect => ({
      id,
      kind,
      severity,
      message: id,
    });
    const sorted = sortEffects([e("w", "water", "info"), e("s", "shade", "info"), e("x", "edge", "warning")]);
    expect(sorted.map((x) => x.id)).toEqual(["x", "s", "w"]);
  });
});

describe("shareFor", () => {
  it("gives the share that makes a block that percent of the blocks", () => {
    // Two others of 1 each: 50% needs a share of 2.
    expect(shareFor(50, 2)).toBe(2);
    expect((shareFor(25, 3) / (shareFor(25, 3) + 3)) * 100).toBeCloseTo(25, 1);
    expect(shareFor(100, 1)).toBe(19); // capped at 95%
    expect(shareFor(0, 1)).toBeCloseTo(0.05, 2); // at least 5%
  });
});
