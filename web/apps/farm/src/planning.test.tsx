import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cutAreas, plane } from "./draw";
import { Planning } from "./planning";
import type { Farm, Plan, Point } from "./types";

const pl = plane({ lat: 16.29, lng: 80.45 });
const at = (x: number, y: number) => pl.point({ x, y });
const plot = [at(0, 0), at(100, 0), at(100, 100), at(0, 100)];
const halves = cutAreas([plot], at(30, -5), at(30, 105)) ?? [];

// The area editor draws on a map; here it hands back the plot cut in two.
vi.mock("./areas", () => ({
  AreaEditor: ({ onDone }: { onDone: (a: Point[][]) => void }) => (
    <button onClick={() => onDone(halves)}>Use the drawn areas</button>
  ),
}));

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

const farm = {
  id: "f1",
  name: "East field",
  boundary: { corners: plot, source: "map" },
  report: { status: "verified", checks: [], checked_at: "" },
  created_at: "",
  updated_at: "",
} as unknown as Farm;

afterEach(() => vi.unstubAllGlobals());

describe("Planning with areas", () => {
  it("makes each drawn area a zone of the plan, as drawn, and leaves an area without a crop unplanned", async () => {
    const checked: Plan[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string, init?: RequestInit) => {
        const u = String(url);
        if (u === "/api/crops")
          return json({
            crops: [
              { id: "maize", name: "Maize" },
              { id: "chickpea", name: "Chickpea" },
            ],
          });
        if (u === "/api/soils") return json({ soils: [] });
        if (u.endsWith("/plan/check")) {
          checked.push(JSON.parse(String(init?.body)));
          return json({ status: "sound", checks: [], zones: [{ area_m2: 3000, share: 0.3, rows: 1, plants: 1 }] });
        }
        if (u.endsWith("/layout")) throw new Error("drawn areas are not laid out again");
        return json({});
      }),
    );
    const user = userEvent.setup();
    const onZones = vi.fn();
    render(<Planning farm={farm} onFarm={() => {}} onZones={onZones} />);
    const plan = screen.getByRole("region", { name: "Season plan" });
    await user.click(within(plan).getByRole("button", { name: /divide into areas/i }));
    await user.click(await screen.findByRole("button", { name: "Use the drawn areas" }));

    const crops = await screen.findAllByRole("combobox", { name: "Zone crop" });
    expect(crops).toHaveLength(2);
    await waitFor(() => expect(within(crops[0] as HTMLElement).getAllByRole("option")).toHaveLength(3));
    await user.selectOptions(crops[0] as HTMLElement, "maize");
    const sow = screen.getAllByLabelText("Sow on")[0] as HTMLElement;
    await user.type(sow, "2026-11-25");
    await user.click(screen.getByRole("button", { name: "Check the plan" }));

    await waitFor(() => expect(checked).toHaveLength(1));
    expect(checked[0]?.zones).toHaveLength(1);
    expect(checked[0]?.zones[0]).toMatchObject({ crop: "maize", sow: "2026-11-25", ring: halves[0] });
    expect(onZones).toHaveBeenLastCalledWith([expect.objectContaining({ ring: halves[0], label: "Maize" })]);
  });
});
