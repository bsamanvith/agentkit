import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { plane } from "./draw";
import { FirstRun } from "./firstrun";
import type { Corner, Point } from "./types";

interface MapProps {
  goto?: Point;
  corners: Corner[];
  onStroke?: (pts: Point[]) => void;
}

// Leaflet cannot run in jsdom: a stand-in shows where the map went and
// lets the test draw on it.
const map = vi.hoisted(() => ({ props: undefined as unknown as MapProps }));
vi.mock("./map", () => ({
  FarmMap: (p: MapProps) => {
    map.props = p;
    return <div data-testid="map">{p.goto ? `${p.goto.lat},${p.goto.lng}` : "India"}</div>;
  },
}));

const pl = plane({ lat: 16.29, lng: 80.45 });
const at = (x: number, y: number) => pl.point({ x, y });

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

const report = {
  status: "verified",
  measures: {
    area_m2: 2400,
    hectares: 0.24,
    acres: 0.59,
    guntas: 23.7,
    cents: 59.3,
    acres_text: "0.59 acre",
    perimeter_m: 220,
    centroid: { lat: 16.2902, lng: 80.4501 },
    corners: 4,
  },
  checks: [],
  checked_at: "2026-11-15T06:00:00Z",
};
const option = (crop: string, name: string, sow: string, harvest: string) => ({
  crop,
  name,
  kind: "cereal",
  sow,
  harvest,
  season: "rabi",
  score: 80,
  water: { need_mm: 400, rain_mm: 100, irrigation_mm: 300, gap_mm: 0, short_mm: 0 },
  risks: [],
  reasons: [],
  window: { from: "2026-11-20", to: "2026-12-15" },
});
const options = [
  option("maize", "Maize", "2026-11-25", "2027-03-10"),
  option("chickpea", "Chickpea", "2026-11-28", "2027-02-25"),
];

/** The farm API for a farmer with no plots yet; calls records each method and path. */
function server({ recommend = true } = {}) {
  const calls: string[] = [];
  let farm: Record<string, unknown> = {};
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init?: RequestInit) => {
      const u = String(url);
      const method = init?.method ?? "GET";
      calls.push(`${method} ${u}`);
      const body = init?.body ? JSON.parse(String(init.body)) : undefined;
      if (u.startsWith("/api/places"))
        return json({ places: [{ name: "Lalapet", kind: "village", point: { lat: 16.29, lng: 80.45 }, address: "" }] });
      if (u === "/api/verify") return json(report);
      if (u === "/api/farms" && method === "POST") {
        farm = { id: "f1", ...body, report, created_at: "", updated_at: "" };
        return json(farm);
      }
      if (u === "/api/crops") return json({ crops: [{ id: "maize", name: "Maize" }] });
      if (u.endsWith("/recommend")) {
        if (!recommend) return json({ error: "climate unavailable" }, 502);
        return json({ farm: "f1", today: "2026-11-15", options: body.crops ? options.slice(0, 1) : options });
      }
      if (u.endsWith("/layout"))
        return json({
          plan: { zones: [{ crop: body.parts[0].crop, ring: [], row_bearing: 0, sow: body.parts[0].sow }] },
          report: { status: "sound", checks: [] },
        });
      if (u.endsWith("/plan") && method === "PUT") return json({ ...farm, plan: body });
      return json({});
    }),
  );
  return calls;
}

afterEach(() => vi.unstubAllGlobals());

/** Goes through the village, the drawing and the name, as a farmer would. */
async function drawAndSave(user: ReturnType<typeof userEvent.setup>) {
  await user.type(screen.getByRole("textbox", { name: "Village or town" }), "Lalapet{Enter}");
  await waitFor(() => expect(screen.getByTestId("map")).toHaveTextContent("16.29,80.45"));
  await user.click(screen.getByRole("button", { name: "Next: draw the plot" }));
  expect(screen.getByText(/Step 2 of/)).toBeInTheDocument();
  const next = screen.getByRole("button", { name: "Next: name it" });
  expect(next).toBeDisabled();
  await user.click(screen.getByRole("button", { name: /rectangle/i }));
  act(() => map.props.onStroke?.([at(0, 0), at(30, 80)]));
  await waitFor(() => expect(next).toBeEnabled());
  await user.click(next);
  expect(screen.getByRole("textbox", { name: "Name" })).toHaveValue("My field");
  await user.click(screen.getByRole("button", { name: "Save plot" }));
}

describe("FirstRun", () => {
  it("takes a new farmer from the village to a saved plot and suggested dates in four steps", async () => {
    const calls = server();
    const onDone = vi.fn();
    const user = userEvent.setup();
    render(<FirstRun planner roadmap onAdd={() => {}} onDone={onDone} />);
    expect(screen.getByText("Step 1 of 4")).toBeInTheDocument();
    await drawAndSave(user);

    expect(await screen.findByText("Step 4 of 4")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /suggest for me/i }));
    await user.click(await screen.findByRole("button", { name: /Maize/ }));
    // Changed their mind: back to the suggestions.
    await user.click(screen.getByRole("button", { name: "Other suggestions" }));
    await user.click(screen.getByRole("button", { name: /Chickpea/ }));
    const dates = screen.getByRole("region", { name: "Your dates" });
    expect(dates).toHaveTextContent(/Sow.*(Nov 28, 2026|28 Nov 2026)/);
    expect(dates).toHaveTextContent(/Harvest, about.*(Feb 25, 2027|25 Feb 2027)/);
    await user.click(screen.getByRole("button", { name: "Save and see the season" }));

    await waitFor(() => expect(onDone).toHaveBeenCalled());
    const [farm, tab] = onDone.mock.calls[0] ?? [];
    expect(tab).toBe("roadmap");
    expect(farm.plan.zones[0].crop).toBe("chickpea");
    expect(calls).toContain("POST /api/farms");
    expect(calls).toContain("PUT /api/farms/f1/plan");
  });

  it("shows the dates of the crop the farmer names at once", async () => {
    server();
    const user = userEvent.setup();
    render(<FirstRun planner roadmap={false} onAdd={() => {}} onDone={() => {}} />);
    await drawAndSave(user);
    await screen.findByText("Step 4 of 4");
    await user.selectOptions(await screen.findByRole("combobox", { name: "Crop" }), "maize");
    expect(await screen.findByRole("region", { name: "Your dates" })).toHaveTextContent(/Nov 25, 2026|25 Nov 2026/);
  });

  it("never blocks on the crop step: with the suggestions down the farmer still reaches the plot", async () => {
    server({ recommend: false });
    const onDone = vi.fn();
    const user = userEvent.setup();
    render(<FirstRun planner roadmap onAdd={() => {}} onDone={onDone} />);
    await drawAndSave(user);
    await screen.findByText("Step 4 of 4");
    await user.click(screen.getByRole("button", { name: /suggest for me/i }));
    expect(await screen.findByRole("alert")).toHaveTextContent("climate unavailable");
    await user.click(screen.getByRole("button", { name: /skip: go to my plot/i }));
    expect(onDone).toHaveBeenCalledWith(expect.objectContaining({ id: "f1" }), undefined);
  });

  it("ends at the saved plot in three steps when crop planning is off", async () => {
    server();
    const onDone = vi.fn();
    const user = userEvent.setup();
    render(<FirstRun planner={false} roadmap={false} onAdd={() => {}} onDone={onDone} />);
    expect(screen.getByText("Step 1 of 3")).toBeInTheDocument();
    await drawAndSave(user);
    await waitFor(() => expect(onDone).toHaveBeenCalledWith(expect.objectContaining({ id: "f1" })));
  });

  it("lets the farmer skip the guide for the plain plot editor", async () => {
    server();
    const onAdd = vi.fn();
    const user = userEvent.setup();
    render(<FirstRun planner roadmap onAdd={onAdd} onDone={() => {}} />);
    await user.click(screen.getByRole("button", { name: "Skip the guide" }));
    expect(screen.getByRole("heading", { name: "Add your first plot" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /add plot/i }));
    expect(onAdd).toHaveBeenCalled();
  });
});
