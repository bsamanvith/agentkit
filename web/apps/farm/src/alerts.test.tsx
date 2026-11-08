import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AlertsPage, Care } from "./alerts";
import type { Automation, Farm, Notice, Week } from "./types";

const farm: Farm = {
  id: "farm_1",
  name: "East field",
  boundary: { corners: [] },
  report: { status: "verified", checks: [], checked_at: "2026-11-01T06:00:00Z" },
  plan: { zones: [{ crop: "maize", ring: [], row_bearing: 0, sow: "2026-10-15" }] },
  created_at: "2026-11-01T06:00:00Z",
  updated_at: "2026-11-01T06:00:00Z",
} as Farm;

const days = ["09", "10", "11", "12", "13", "14", "15"].map((d) => ({
  date: `2026-11-${d}`,
  tmax_c: 30,
  tmin_c: 20,
  rain_mm: d === "12" ? 85 : 0,
  rain_chance: 0,
  wind_kmh: 10,
  gust_kmh: 20,
  humidity_pct: 60,
  et0_mm: 4,
}));

function week(advice: Week["advice"]): Week {
  return {
    farm: "farm_1",
    today: "2026-11-09",
    days,
    totals: { rain_mm: 85, et0_mm: 28, max_c: 30, min_c: 20, gust_kmh: 20, rain_days: 1 },
    crops: [
      {
        zone: 0,
        crop: "maize",
        name: "Maize",
        sow: "2026-10-15",
        stage: { name: "vegetative", das: 25, in_field: true },
      },
    ],
    advice,
    tasks: [
      {
        date: "2026-11-10",
        das: 26,
        stage: "vegetative",
        kind: "fertilise",
        crop: "maize",
        zone: 0,
        title: "First top-dressing of nitrogen",
      },
    ],
    source: "Open-Meteo",
  };
}

const heavy = {
  rule: "heavy_rain",
  key: "heavy_rain:farm_1:2026-11-12",
  severity: "warning" as const,
  day: "2026-11-12",
  title: "Heavy rain Thursday 12 Nov",
  action: "Open the field drains and check the bunds before Thursday 12 Nov.",
  why: "85 mm of rain is forecast on Thursday 12 Nov.",
};

const notice: Notice = {
  id: "note_1",
  farm: "farm_1",
  automation: "weekly-weather",
  run: "run_1",
  key: heavy.key,
  severity: "warning",
  title: "Heavy rain Thursday 12 Nov · East field",
  body: heavy.action + "\n\nWhy: " + heavy.why,
  day: heavy.day,
  created_at: "2026-11-09T00:31:00Z",
};

function json(v: unknown) {
  return new Response(JSON.stringify(v), { status: 200, headers: { "Content-Type": "application/json" } });
}

afterEach(() => vi.unstubAllGlobals());

describe("Care", () => {
  it("says a quiet week needs nothing, and shows the work due", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string) => {
        if (url.endsWith("/week")) return json(week([]));
        if (url.endsWith("/calendar")) return json({ zones: [], notes: [] });
        return json({ notices: [], unread: 0 });
      }),
    );
    render(<Care farm={farm} weather automations />);
    expect(await screen.findByText(/Nothing to do because of the weather this week/)).toBeInTheDocument();
    expect(screen.getByText("First top-dressing of nitrogen")).toBeInTheDocument();
    expect(screen.getByText(/Maize: vegetative, day 25/)).toBeInTheDocument();
    expect(await screen.findByText(/No alerts yet/)).toBeInTheDocument();
  });

  it("shows the advice and the plot's alerts, and marks them read", async () => {
    const reads: unknown[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string, init?: RequestInit) => {
        if (url.endsWith("/week")) return json(week([heavy]));
        if (url.endsWith("/calendar")) return json({ zones: [], notes: [] });
        if (url.endsWith("/api/notices/read")) {
          reads.push(JSON.parse(String(init?.body)));
          return json({ marked: 1 });
        }
        return json({ notices: reads.length ? [{ ...notice, read_at: "2026-11-09T02:00:00Z" }] : [notice], unread: 1 });
      }),
    );
    const user = userEvent.setup();
    render(<Care farm={farm} weather automations />);
    expect(await screen.findByText("Heavy rain Thursday 12 Nov")).toBeInTheDocument();
    expect(screen.getAllByText(/Open the field drains/)).toHaveLength(2); // the week's advice and the alert
    expect(screen.getByText("85 mm")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Mark 1 read" }));
    await waitFor(() => expect(reads).toEqual([{ farm: "farm_1" }]));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Mark read" })).not.toBeInTheDocument());
  });
});

describe("AlertsPage", () => {
  const weekly: Automation = {
    id: "weekly-weather",
    name: "Weekly weather check",
    description: "Every Monday.",
    trigger: { kind: "schedule", schedule: "30 0 * * 1" },
    scope: {},
    action: { kind: "rule", rule: "weather_advice" },
    enabled: true,
    builtin: true,
    created_at: "2026-11-08T00:00:00Z",
    updated_at: "2026-11-08T00:00:00Z",
  };
  const watch: Automation = {
    ...weekly,
    id: "rain-watch",
    name: "Heavy rain watch",
    trigger: { kind: "weather", schedule: "0 */6 * * *", weather: { metric: "rain_mm", op: ">=", value: 50, days: 3 } },
  };

  it("lists the automations in words, runs one, turns one off and adds one", async () => {
    const calls: { url: string; method: string; body?: unknown }[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string, init?: RequestInit) => {
        const method = init?.method ?? "GET";
        calls.push({ url, method, body: init?.body ? JSON.parse(String(init.body)) : undefined });
        if (url === "/api/automations" && method === "GET")
          return json({
            automations: [weekly, watch],
            rules: { weather_advice: "Weather advice", crop_tasks: "Work" },
          });
        if (url.endsWith("/run"))
          return json({
            runs: [
              {
                id: "r1",
                automation: "weekly-weather",
                farm: "farm_1",
                status: "no_action",
                note: "0 pieces of advice",
                reason: "run by hand",
                notices: 0,
                attempts: 1,
                due: "",
                started: "2026-11-09T01:00:00Z",
                finished: "",
              },
            ],
          });
        if (url.startsWith("/api/notices")) return json({ notices: [notice], unread: 1 });
        return json({ ...weekly, enabled: false });
      }),
    );
    const user = userEvent.setup();
    render(<AlertsPage farms={[farm]} onRead={() => {}} />);
    const card = (await screen.findByRole("heading", { name: "Weekly weather check" })).closest("article")!;
    expect(within(card).getByText(/Mondays at 06:00 IST/)).toBeInTheDocument();
    expect(screen.getByText(/every 6 hours, if a day's rain \(mm\) >= 50 in the next 3 days/)).toBeInTheDocument();
    await user.click(within(card).getByRole("button", { name: /Run now/ }));
    expect(await within(card).findByText("nothing to do")).toBeInTheDocument();
    await user.click(within(card).getByRole("button", { name: /Turn off/ }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === "PUT" && (c.body as Automation).enabled === false)).toBe(true),
    );
    expect(within(card).queryByRole("button", { name: /Delete/ })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /New/ }));
    await user.click(screen.getByRole("button", { name: /Add automation/ }));
    await waitFor(() => {
      const made = calls.find((c) => c.method === "POST" && c.url === "/api/automations");
      expect(made?.body).toMatchObject({
        name: "Heat watch",
        trigger: { kind: "weather", weather: { metric: "tmax_c", op: ">=", value: 40, days: 5 } },
      });
    });
  });
});
