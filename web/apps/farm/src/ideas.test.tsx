import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import Ideas, { type Idea, type IdeasReport } from "./ideas";
import type { Farm } from "./types";

const farm: Farm = {
  id: "farm_1",
  name: "Red field",
  boundary: { corners: [] },
  report: { status: "verified", checks: [], checked_at: "2026-11-01T06:00:00Z" },
  created_at: "2026-11-01T06:00:00Z",
  updated_at: "2026-11-01T06:00:00Z",
};

const fit = { score: 100, sow: "2026-11-08", harvest: "2027-03-01", good_weeks: 30 };
const groundnut: Idea = {
  crop: "groundnut",
  name: "Groundnut",
  kind: "oilseed",
  verdict: "realistic",
  band: "common",
  why: "A crop of this district, with its own local sowing window, and the plot suits it.",
  open: fit,
  price: {
    commodity: "Groundnut",
    likely: 6300,
    area: "YSR Kadapa district",
    confidence: "fair",
    msp: 7263,
    msp_season: "Kharif 2026-27",
    markets: [{ date: "2026-10-28", market: "Proddatur", modal: 6400 }],
  },
  sell: [{ id: "mandi", name: "The regulated market yard (mandi)", how: "Open auction at the nearest yard." }],
};
const capsicum: Idea = {
  crop: "capsicum",
  name: "Capsicum",
  kind: "vegetable",
  verdict: "stretch",
  band: "method",
  why: "In the open field it scores 45 of 100.",
  open: { ...fit, score: 45 },
  with: {
    ...fit,
    score: 82,
    method: "shade_net",
    name: "Shade net house",
    what: "A frame covered with a net.",
    skill: "medium",
    cost: { sqm: 2500, total: 1775000, subsidy: 887500, farmer: 887500 },
  },
  sell: [],
};
const saffron: Idea = {
  crop: "crocus_sativus",
  name: "Crocus sativus",
  common: ["saffron"],
  kind: "medicinal",
  experimental: true,
  verdict: "unrealistic",
  band: "unrealistic",
  why: "Too hot around May (33 °C); it stands at most 23 °C.",
  open: { ...fit, score: 1 },
  sell: [{ id: "fpo", name: "A farmer producer organisation", how: "Pool the crop." }],
};
const report: IdeasReport = {
  farm: "farm_1",
  today: "2026-11-01",
  ideas: [groundnut, capsicum, saffron],
  notes: [{ id: "ideas.money", message: "Yield, running cost and profit an acre are not estimated." }],
};

function serve(answer: (url: string) => IdeasReport) {
  const urls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) => {
      urls.push(url);
      return new Response(JSON.stringify(answer(url)), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }),
  );
  return urls;
}

afterEach(() => vi.unstubAllGlobals());

describe("Ideas", () => {
  it("shows each band with its verdicts, the method's cost, prices and where to sell", async () => {
    serve(() => report);
    render(<Ideas farm={farm} />);
    const usual = await screen.findByRole("region", { name: "Usual crops here" });
    expect(within(usual).getByText("Realistic")).toBeInTheDocument();
    expect(within(usual).getByText(/About ₹6,300 a quintal/)).toBeInTheDocument();
    expect(within(usual).getByText(/Support price ₹7,263/)).toBeInTheDocument();
    expect(within(usual).getByText(/Proddatur ₹6,400/)).toBeInTheDocument();
    expect(within(usual).getByText(/Where to sell \(1 ways\)/)).toBeInTheDocument();

    const method = screen.getByRole("region", { name: "Possible with a growing method" });
    expect(within(method).getByText("A stretch")).toBeInTheDocument();
    expect(within(method).getByText(/Scheme cost norm for 2,500 m²: ₹17,75,000/)).toBeInTheDocument();
    expect(within(method).getByText(/about ₹8,87,500 from you/)).toBeInTheDocument();

    // An unrealistic idea says so and why, and offers nowhere to sell.
    const no = screen.getByRole("region", { name: "Not realistic here" });
    expect(within(no).getByText("Unrealistic")).toBeInTheDocument();
    expect(within(no).getByText(/Too hot around May/)).toBeInTheDocument();
    expect(within(no).queryByText(/Where to sell/)).not.toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "New things that could work" })).not.toBeInTheDocument();
    expect(screen.getByText(/profit an acre are not estimated/)).toBeInTheDocument();
  });

  it("checks one idea by name, and goes back to all of them", async () => {
    const urls = serve((url) => (url.includes("search=") ? { ...report, ideas: [saffron] } : report));
    render(<Ideas farm={farm} />);
    await screen.findByRole("region", { name: "Usual crops here" });
    await userEvent.type(screen.getByLabelText(/Check a crop by name/), "saffron");
    await userEvent.click(screen.getByRole("button", { name: "Check" }));
    await waitFor(() => expect(screen.queryByRole("region", { name: "Usual crops here" })).not.toBeInTheDocument());
    expect(urls.at(-1)).toBe("/api/farms/farm_1/ideas?search=saffron");
    expect(await screen.findByText("Unrealistic")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Show all ideas" }));
    expect(await screen.findByRole("region", { name: "Usual crops here" })).toBeInTheDocument();
  });
});
