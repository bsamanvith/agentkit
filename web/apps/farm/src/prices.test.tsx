import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Prices } from "./prices";
import { cropChoices, rupees, type Estimate, type TodayAnswer } from "./prices-data";
import type { Farm } from "./types";

const farm: Farm = {
  id: "farm_1",
  name: "East field",
  boundary: { corners: [] },
  report: {
    status: "verified",
    checks: [],
    checked_at: "2026-11-01T06:00:00Z",
    location: { district: "Rangareddy", state: "Telangana" },
  },
  plan: { zones: [{ crop: "pigeon_pea", with: "sorghum", ring: [], row_bearing: 0, sow: "2026-06-15" }] },
  created_at: "2026-11-01T06:00:00Z",
  updated_at: "2026-11-01T06:00:00Z",
};

// Synthetic prices, shaped like the API's answers.
const today: TodayAnswer = {
  answer: {
    query: { commodity: "Arhar (Tur/Red Gram)(Whole)", state: "Telangana", district: "Rangareddy" },
    markets: [
      {
        date: "2026-11-07",
        state: "Telangana",
        district: "Rangareddi",
        market: "Shadnagar",
        commodity: "Arhar (Tur/Red Gram)(Whole)",
        variety: "Red",
        min: 7600,
        max: 8300,
        modal: 8100,
        source: "Agmarknet via data.gov.in",
      },
      {
        date: "2026-11-07",
        state: "Telangana",
        district: "Rangareddi",
        market: "Chevella",
        commodity: "Arhar (Tur/Red Gram)(Whole)",
        variety: "Red",
        min: 7400,
        max: 8000,
        modal: 7850,
        source: "Agmarknet via data.gov.in",
      },
    ],
    as_of: "2026-11-07",
    days_old: 1,
    source: "Agmarknet via data.gov.in",
  },
  msp: [
    {
      commodity: "Arhar (Tur/Red Gram)(Whole)",
      season: "Kharif 2026-27",
      price: 8450,
      source: "Cabinet decision, 13 May 2026 (PIB)",
    },
  ],
};

function estimate(sell: number, yieldQ?: number): Estimate {
  return {
    commodity: "Arhar (Tur/Red Gram)(Whole)",
    sell_month: sell,
    area: "Rangareddy district, Telangana",
    low: 7200,
    likely: 8000 + sell,
    high: 8800,
    now: 7975,
    trend_pct: 2.5,
    confidence: "fair",
    basis: { quotes: 1200, markets: 4, months: 14, from: "2025-09-01", to: "2026-11-07", sources: "price files" },
    revenue: yieldQ
      ? {
          yield_q_per_acre: yieldQ,
          per_acre: { low: 7200 * yieldQ, likely: 8000 * yieldQ, high: 8800 * yieldQ },
          acres: 2,
          total: { low: 2 * 7200 * yieldQ, likely: 2 * 8000 * yieldQ, high: 2 * 8800 * yieldQ },
        }
      : undefined,
    notes: ["The likely mandi price is below the MSP: ask about procurement centres."],
  };
}

afterEach(() => vi.unstubAllGlobals());

describe("Prices", () => {
  it("puts the plan's crops first", () => {
    const ids = cropChoices(farm).map((c) => c.id);
    expect(ids.slice(0, 2)).toEqual(["pigeon_pea", "sorghum"]);
    expect(ids.filter((i) => i === "sorghum")).toHaveLength(1);
    expect(ids).toContain("tomato");
  });

  it("writes rupees in the Indian grouping", () => {
    expect(rupees(123456.4)).toBe("₹1,23,456");
  });

  it("shows today's markets, the estimate and the MSP, and asks again when the month or yield changes", async () => {
    const calls: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string, init?: RequestInit) => {
        calls.push(init?.body ? `${url} ${String(init.body)}` : url);
        const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
        if (url.startsWith("/api/prices/history")) {
          return json({
            months: [{ month: "2026-10", median_modal: 7900, low: 7500, high: 8200, quotes: 60 }],
            quotes: 60,
          });
        }
        if (url.startsWith("/api/prices/estimate")) {
          const b = JSON.parse(String(init?.body)) as { sell_month: number; yield_q_per_acre?: number };
          return json(estimate(b.sell_month, b.yield_q_per_acre));
        }
        return json(today);
      }),
    );
    render(<Prices farm={farm} />);
    const table = await screen.findByRole("table");
    expect(within(table).getByText("Shadnagar")).toBeInTheDocument();
    expect(within(table).getByText("₹8,100")).toBeInTheDocument();
    expect(calls[0]).toBe("/api/prices?farm=farm_1&commodity=pigeon_pea");
    expect(await screen.findByText("₹8,450")).toBeInTheDocument();
    expect(await screen.findByText(/below the MSP/)).toBeInTheDocument();
    expect(screen.getByRole("img", { name: "Median price by month" })).toBeInTheDocument();

    await userEvent.selectOptions(screen.getByLabelText("Selling month"), "3");
    await waitFor(() => expect(screen.getByText("8,003")).toBeInTheDocument());
    await userEvent.type(screen.getByLabelText("Expected yield in quintals an acre"), "5");
    expect(await screen.findByText(/for the plot's 2 acres/)).toBeInTheDocument();
    expect(calls.some((c) => c.includes('"yield_q_per_acre":5'))).toBe(true);
  });

  it("says when there are no prices", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string) =>
        url.startsWith("/api/prices/history")
          ? new Response(JSON.stringify({ months: [], quotes: 0 }), { status: 200 })
          : new Response(JSON.stringify({ error: "no mandi prices found for Tur in Telangana" }), { status: 404 }),
      ),
    );
    render(<Prices farm={farm} />);
    expect(await screen.findAllByText(/no mandi prices found/)).toHaveLength(2);
    expect(await screen.findByText("No saved history yet.")).toBeInTheDocument();
  });
});
