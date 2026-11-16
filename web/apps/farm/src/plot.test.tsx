import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import FarmDetail from "./plot";
import type { Farm } from "./types";

// The tabs' pages are lazy chunks of their own: stand-ins say which opened.
vi.mock("./map", () => ({ FarmMap: () => <div>the map</div> }));
vi.mock("./cycles", () => ({ default: () => <div>the cycles</div> }));
vi.mock("./planning", () => ({ Planning: () => null }));

const farm: Farm = {
  id: "farm_1",
  name: "East plot",
  boundary: { corners: [] },
  report: { status: "verified", checks: [], checked_at: "2026-06-01T06:00:00Z" },
  created_at: "2026-06-01T06:00:00Z",
  updated_at: "2026-06-01T06:00:00Z",
};
const planned: Farm = { ...farm, plan: { zones: [{ crop: "maize", ring: [], row_bearing: 0, sow: "2026-06-20" }] } };

function show(f: Farm, cycles: boolean) {
  render(
    <FarmDetail
      farm={f}
      others={[]}
      planner
      cycles={cycles}
      surveyor={false}
      weather={false}
      automations={false}
      prices={false}
      ideas={false}
      roadmap
      onChanged={() => {}}
      onEdit={() => {}}
      onOpenGroup={() => {}}
      onDeleted={() => {}}
    />,
  );
  return screen.getAllByRole("tab").map((t) => t.textContent);
}

afterEach(() => vi.unstubAllGlobals());

describe("FarmDetail", () => {
  it("opens a plot with no plan on its cycles, the first tab", async () => {
    expect(show(farm, true)).toEqual(["Cycles", "Map", "Sun and seasons", "Season plan"]);
    expect(screen.getByRole("tab", { name: "Cycles" })).toHaveAttribute("aria-selected", "true");
    expect(await screen.findByText("the cycles")).toBeInTheDocument();
  });

  it("opens a planned plot on its map, with its cycles and season work beside it", () => {
    expect(show(planned, true)).toEqual(["Map", "Cycles", "Season work", "Sun and seasons", "Season plan"]);
    expect(screen.getByRole("tab", { name: "Map" })).toHaveAttribute("aria-selected", "true");
  });

  it("has no Cycles tab where the server serves no cycles", () => {
    expect(show(farm, false)).not.toContain("Cycles");
    expect(screen.getByRole("tab", { name: "Map" })).toHaveAttribute("aria-selected", "true");
  });
});
