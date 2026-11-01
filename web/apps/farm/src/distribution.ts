import type { Crop, Distribution, Effect, EffectKind, Farm, Group, PlantGroup, Place, Point } from "./types";

// Helpers of the group pages, apart so they can be tested: plant group
// colours, starting distributions, ids, and what a group adds up to.

/** Plant group colours, in order: earthy, apart from each other, readable on satellite imagery. */
export const plantColors = ["#15803d", "#ca8a04", "#2563eb", "#c2410c", "#7c3aed", "#0f766e", "#be185d", "#4d7c0f"];

export function plantColor(d: Distribution | undefined, id: string): string {
  const i = d?.plants.findIndex((p) => p.id === id) ?? -1;
  return plantColors[(i < 0 ? 0 : i) % plantColors.length] ?? "#15803d";
}

export const placeWords: Record<Place, { label: string; hint: string }> = {
  block: { label: "Block", hint: "A share of the land" },
  border: { label: "Border", hint: "A band round each plot's edge" },
  alley: { label: "Tree rows", hint: "Rows of trees across, crops between" },
};

export const effectWords: Record<EffectKind, string> = {
  shade: "Shade",
  edge: "Edges",
  spacing: "Spacing",
  water: "Water",
  neighbours: "Neighbours",
  wind: "Wind",
  layout: "Farm layout",
};

/** Effects in the order a farmer reads them: problems first, then by kind. */
export function sortEffects(es: Effect[]): Effect[] {
  const sev = { error: 0, warning: 1, info: 2 };
  const kinds = Object.keys(effectWords);
  return [...es].sort((a, b) => sev[a.severity] - sev[b.severity] || kinds.indexOf(a.kind) - kinds.indexOf(b.kind));
}

/** An id for a plant group named name, not in taken. */
export function plantID(name: string, taken: string[]): string {
  const base =
    name
      .toLowerCase()
      .normalize("NFKD")
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-+|-+$/g, "")
      .slice(0, 30) || "plants";
  let id = base;
  for (let n = 2; taken.includes(id); n++) id = `${base}-${n}`;
  return id;
}

/** A plant group of a template, with only the crops the library has. */
function pg(id: string, name: string, crops: string[], have: Set<string>, rest: Partial<PlantGroup> = {}): PlantGroup {
  return { id, name, crops: crops.filter((c) => have.has(c)), ...rest };
}

export interface Template {
  id: string;
  label: string;
  hint: string;
  make: (have: Set<string>) => Distribution;
}

/** Ways to start, each a common pattern on Indian farms. */
export const templates: Template[] = [
  {
    id: "orchard-border",
    label: "Tree border, crops inside",
    hint: "Mango round the edge for shade, wind and fruit; vegetables and pulses inside",
    make: (have) => ({
      arrange: "strips",
      rows: "north_south",
      plants: [
        pg("border", "Border trees", ["mango"], have, { place: "border", width_m: 5 }),
        pg("veg", "Vegetables", ["tomato", "onion"], have, { share: 1 }),
        pg("pulses", "Pulses", ["green_gram"], have, { share: 1 }),
      ],
    }),
  },
  {
    id: "alley",
    label: "Tree rows with crops between",
    hint: "Drumstick every 12 m, field crops in the alleys: alley cropping",
    make: (have) => ({
      arrange: "strips",
      rows: "north_south",
      plants: [
        pg("rows", "Drumstick rows", ["moringa"], have, { place: "alley", width_m: 2, every_m: 12 }),
        pg("crops", "Field crops", ["black_gram"], have, { share: 1 }),
      ],
    }),
  },
  {
    id: "strip-crop",
    label: "Alternating strips",
    hint: "Strips that take turns slow pests: millet with pigeon pea",
    make: (have) => ({
      arrange: "alternate",
      strip_m: 10,
      rows: "north_south",
      plants: [
        pg("cereal", "Millet", ["sorghum"], have, { share: 1 }),
        pg("pulse", "Pigeon pea", ["pigeon_pea"], have, { share: 1 }),
      ],
    }),
  },
  {
    id: "by-plot",
    label: "One crop per plot",
    hint: "Whole plots to a group: paddy on some, groundnut on the rest",
    make: (have) => ({
      arrange: "by_plot",
      plants: [
        pg("paddy", "Paddy", ["rice"], have, { share: 1 }),
        pg("groundnut", "Groundnut", ["groundnut"], have, { share: 1 }),
      ],
    }),
  },
];

/** A blank plant group for a distribution. */
export function newPlant(d: Distribution, crops: Crop[]): PlantGroup {
  const name = `Plants ${d.plants.length + 1}`;
  const used = new Set(d.plants.flatMap((p) => p.crops));
  const crop = crops.find((c) => !used.has(c.id) && !c.perennial) ?? crops[0];
  return {
    id: plantID(
      name,
      d.plants.map((p) => p.id),
    ),
    name,
    crops: crop ? [crop.id] : [],
    place: "block",
    share: 1,
  };
}

/** What a place change keeps: blocks have a share, borders and tree rows a width. */
export function withPlace(p: PlantGroup, place: Place): PlantGroup {
  const { share: _s, width_m: _w, every_m: _e, ...rest } = p;
  void _s;
  void _w;
  void _e;
  if (place === "block") return { ...rest, place, share: 1 };
  if (place === "border") return { ...rest, place, width_m: 4 };
  return { ...rest, place, width_m: 2, every_m: 12 };
}

/**
 * The share that gives a block pct percent of the blocks' land when the
 * other blocks' shares add up to others.
 */
export function shareFor(pct: number, others: number): number {
  const p = Math.min(95, Math.max(5, pct));
  return Math.round((p / (100 - p)) * others * 100) / 100;
}

/** Problems the server would refuse, said before asking it. */
export function problems(d: Distribution): string[] {
  const out: string[] = [];
  if (!d.plants.some((p) => (p.place ?? "block") === "block"))
    out.push("Give at least one plant group a block of land.");
  if (d.plants.filter((p) => p.place === "border").length > 1) out.push("Only one plant group can be the border.");
  if (d.plants.filter((p) => p.place === "alley").length > 1) out.push("Only one plant group can be tree rows.");
  for (const p of d.plants) if (p.crops.length === 0) out.push(`Pick a crop for ${p.name || "each plant group"}.`);
  return out;
}

/** The plots of a group, in its order, that still exist. */
export function plotsOf(g: Group, farms: Farm[]): Farm[] {
  return g.plots.flatMap((id) => farms.filter((f) => f.id === id));
}

export function acresOf(farms: Farm[]): number {
  return farms.reduce((s, f) => s + (f.report.measures?.acres ?? 0), 0);
}

/** Every corner of the farms, for fitting a picture. */
export function cornersOf(farms: Farm[]): Point[] {
  return farms.flatMap((f) => f.boundary.corners);
}

/** Square metres as a farmer reads them, with acres past a quarter acre. */
export function area(m2: number): string {
  if (m2 >= 1012) return `${(m2 / 4046.8564224).toFixed(2)} ac`;
  return `${Math.round(m2).toLocaleString()} m²`;
}
