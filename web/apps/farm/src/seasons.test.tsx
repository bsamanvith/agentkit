import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Seasons } from "./seasons";
import type { Climate, Crop, Farm, Option, SunReport } from "./types";

const farm: Farm = {
  id: "farm_1",
  name: "East field",
  boundary: { corners: [] },
  report: { status: "verified", checks: [], checked_at: "2026-11-01T06:00:00Z" },
  created_at: "2026-11-01T06:00:00Z",
  updated_at: "2026-11-01T06:00:00Z",
};

// Farooqnagar's year: NASA POWER's light, the sun north at noon from May
// to July, a slope falling north.
const flat = [17.6, 20.7, 22.5, 24.0, 24.1, 19.7, 16.7, 16.5, 17.6, 18.2, 17.5, 16.8];
const sun: SunReport = {
  farm: "farm_1",
  point: { lat: 17.07, lng: 78.2 },
  slope: { tilt_deg: 8.5, aspect_deg: 0 },
  slope_note: "The survey's heights: the ground falls 15% towards the north.",
  months: flat.map((f, i) => ({
    month: i + 1,
    day_length_h: [11.1, 11.5, 11.9, 12.4, 12.8, 13.0, 12.9, 12.6, 12.1, 11.7, 11.2, 11.0][i]!,
    sun_hours: 8 - (i >= 5 && i <= 8 ? 3 : 0),
    noon: { elevation: [52, 60, 70, 82, 88, 84, 86, 84, 76, 64, 55, 50][i]!, azimuth: i >= 4 && i <= 6 ? 0 : 180 },
    noon_incidence: 20,
    flat_mj: f,
    clear_mj: f + 3,
    slope_mj: i === 11 ? f * 0.8 : f,
    slope_factor: i === 11 ? 0.8 : 1,
    cloud_loss: 0.2,
  })),
  seasons: [
    { season: "kharif", from: 6, to: 9 },
    { season: "rabi", from: 10, to: 2 },
    { season: "zaid", from: 3, to: 5 },
  ],
  notes: [
    "Inside the tropics the noon sun is to the north of the plot in May to July and to the south the rest of the year.",
  ],
  source: "NASA POWER climatology",
  today: "2026-11-07",
};
const climate: Climate = {
  point: sun.point,
  source: "NASA POWER climatology",
  months: flat.map((_, i) => ({ temp: 25, temp_max: 35, temp_min: 15, rain_mm: i === 7 ? 166 : 20, humidity: 60 })),
};
const crops = [
  { id: "wheat", name: "Wheat", light: "full", day_length: "long" },
  { id: "turmeric", name: "Turmeric", light: "part", day_length: "neutral" },
  { id: "cocoa", name: "Cocoa", light: "shade", day_length: "neutral" },
] as Crop[];

function weeks(base: number): { sow: string; score: number; season: string }[] {
  return Array.from({ length: 53 }, (_, i) => {
    const d = new Date(Date.UTC(2026, 10, 7 + 7 * i));
    return { sow: d.toISOString().slice(0, 10), score: Math.max(0, base - i), season: "rabi" };
  });
}

function option(crop: string, name: string, score: number): Option {
  return {
    crop,
    name,
    kind: "cereal",
    sow: "2026-11-07",
    harvest: "2027-03-01",
    season: "rabi",
    score,
    water: { need_mm: 450, rain_mm: 100, irrigation_mm: 400, gap_mm: 350, short_mm: 0 },
    light: {
      want: "full",
      sun_mj: 17.5,
      sun_hours: 8.3,
      dimmest_month: "December",
      slope_factor: 0.9,
      day_length_h: [11, 12.1],
    },
    reasons: [{ id: "sun.ok", message: "Sunny enough: about 18 MJ/m² a day." }],
    risks: [{ id: "sun.slope", message: "The plot's slope faces north: it gets about 10% less sun." }],
    calendar: weeks(score),
  };
}

function json(body: unknown) {
  return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
}

afterEach(() => vi.unstubAllGlobals());

describe("Seasons", () => {
  it("shows the year, the noon sun and a sowing calendar, and explains a week", async () => {
    const asks: Record<string, unknown>[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string, init?: RequestInit) => {
        if (url.endsWith("/sun")) return json(sun);
        if (url.endsWith("/climate")) return json(climate);
        if (url.endsWith("/api/crops")) return json({ crops });
        const ask = JSON.parse(String(init?.body ?? "{}")) as { crops?: string[]; around?: string; limit?: number };
        asks.push(ask);
        if (ask.limit) return json({ farm: "farm_1", today: "2026-11-07", options: [option("wheat", "Wheat", 90)] });
        const opts = (ask.crops ?? []).map((c, i) => option(c, crops.find((x) => x.id === c)!.name, 90 - 30 * i));
        return json({ farm: "farm_1", today: "2026-11-07", options: opts });
      }),
    );
    const user = userEvent.setup();
    render(<Seasons farm={farm} />);

    const year = await screen.findByRole("table", { name: "The year month by month" });
    expect(screen.getByText(/falls 15% towards the north/)).toBeInTheDocument();
    for (const want of ["Kharif", "Zaid", "Sunlight", "Bright sun", "Day length", "Noon sun", "Slope vs flat"]) {
      expect(within(year).getByText(want)).toBeInTheDocument();
    }
    // Rabi runs over the new year: October to December, then January and February.
    expect(within(year).getAllByText("Rabi")).toHaveLength(2);
    // June's noon sun is north, December's south; the slope loses a fifth in December.
    expect(within(year).getByText("84° N")).toBeInTheDocument();
    expect(within(year).getByText("50° S")).toBeInTheDocument();
    expect(within(year).getByText("-20%")).toBeInTheDocument();
    expect(screen.getByRole("img", { name: /noon sun's height/i })).toBeInTheDocument();
    expect(screen.getByText(/noon sun is to the north of the plot/)).toBeInTheDocument();

    // The calendar starts with the farm's best crops, a cell per week.
    const cal = await screen.findByRole("table", { name: "Sowing calendar" });
    await waitFor(() => expect(within(cal).getAllByRole("cell")).toHaveLength(53));
    expect(within(cal).getByText(/Long-day/)).toBeInTheDocument();

    // Add a crop: a second row, scored together.
    await user.selectOptions(screen.getByRole("combobox", { name: "Add a crop" }), "cocoa");
    await waitFor(() => expect(within(cal).getAllByRole("cell")).toHaveLength(106));
    expect(asks.at(-1)).toMatchObject({ crops: ["wheat", "cocoa"], calendar: true });
    expect(within(cal).getByText(/Shade/)).toBeInTheDocument();

    // A week, explained.
    await user.click(within(cal).getAllByRole("cell", { name: /^Wheat sown/ })[0]!);
    expect(asks.at(-1)).toMatchObject({ crops: ["wheat"], around: "2026-11-07", spread: 3 });
    expect(await screen.findByRole("list", { name: "Risks" })).toHaveTextContent(/slope faces north/);
    expect(document.body).toHaveTextContent(/about 17.5 MJ\/m² a day, 8.3 h of bright sun, least in December/);

    // Take a crop off.
    await user.click(screen.getByRole("button", { name: "Remove Cocoa" }));
    await waitFor(() => expect(within(cal).getAllByRole("cell")).toHaveLength(53));
  });
});
