import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import Cycles from "./cycles";
import { edited, sameCycle, span, type Cycle, type Line } from "./cycles-data";
import type { Farm } from "./types";

const farm: Farm = {
  id: "farm_1",
  name: "Kadapa plot",
  boundary: { corners: [] },
  report: { status: "verified", checks: [], checked_at: "2026-06-01T06:00:00Z" },
  created_at: "2026-06-01T06:00:00Z",
  updated_at: "2026-06-01T06:00:00Z",
};

function cycle(origin: Cycle["origin"], crop: string, name: string, sow: string, harvest: string): Cycle {
  return {
    origin,
    season: "kharif",
    method: "conventional",
    plan: { zones: [{ crop, ring: [], row_bearing: 0, sow }] },
    areas: [{ zone: 0, crop, crop_name: name, area_m2: 4047, area_ha: 0.405, sow, sow_kind: "planned", harvest }],
    sow,
    sow_kind: "planned",
    harvest,
    score: 80,
    reasons: [{ id: "rotation.pulse", message: "A legume after Maize puts nitrogen back in the soil." }],
  };
}

// A line shaped like the API's: nothing set by the farmer yet.
function line(): Line {
  const now = cycle("suggested", "black_gram", "Black gram", "2026-06-20", "2026-09-08");
  const next = cycle("suggested", "chickpea", "Chickpea", "2026-10-15", "2027-01-31");
  const after = cycle("suggested", "sorghum", "Sorghum", "2027-06-10", "2027-10-01");
  return {
    farm: "farm_1",
    today: "2026-06-01",
    future: true,
    cycles: [
      { position: 0, from: "2026-06-01", suggested: now, planned: now },
      { position: 1, from: "2026-09-23", suggested: next, planned: next },
      { position: 2, from: "2027-02-15", suggested: after, planned: after },
    ],
  };
}

function json(body: unknown, status = 200) {
  return Promise.resolve(
    new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }),
  );
}

type Call = [string, RequestInit];

function server(answer: (path: string, init: RequestInit) => Promise<Response>) {
  const fetch = vi.fn((path: string, init: RequestInit) => answer(path, init));
  vi.stubGlobal("fetch", fetch);
  return fetch;
}

afterEach(() => vi.unstubAllGlobals());

describe("Cycles", () => {
  it("shows the current cycle first and takes the suggestion in one tap", async () => {
    const accepted = line();
    const own = { ...cycle("farmer", "black_gram", "Black gram", "2026-06-20", "2026-09-08") };
    accepted.cycles[0] = { ...accepted.cycles[0], position: 0, from: "2026-06-01", own, planned: own };
    const fetch = server((path, init) => {
      if (init.method === "PUT") return json(accepted);
      if (path.endsWith("/roadmap"))
        return json({ farm: "farm_1", today: "2026-06-01", zones: [], tasks: [], counts: {} });
      return json(line());
    });
    const onFarm = vi.fn();
    render(<Cycles farm={farm} onFarm={onFarm} />);
    expect(await screen.findByRole("heading", { name: "This cycle" })).toBeInTheDocument();
    expect(screen.getAllByText("Black gram").length).toBeGreaterThan(0);
    expect(screen.getByText(/harvest around/)).toBeInTheDocument();
    // The later cycles are one tap away, not in the way.
    expect(screen.queryByRole("heading", { name: "Next cycle" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /Use this/ }));
    await waitFor(() => expect(onFarm).toHaveBeenCalled());
    const put = fetch.mock.calls.find((c) => (c as unknown as Call)[1].method === "PUT") as unknown as Call;
    expect(put[0]).toBe("/api/farms/farm_1/cycles/0");
    expect(JSON.parse(String(put[1].body))).toEqual({ accept: true });
    expect(await screen.findByText(/Yours/)).toBeInTheDocument();
  });

  it("records a sowing the farmer already did", async () => {
    const fetch = server(() => json(line()));
    render(<Cycles farm={farm} />);
    await userEvent.click(await screen.findByRole("button", { name: /I already sowed/ }));
    const input = screen.getByLabelText("Sown on");
    await userEvent.clear(input);
    await userEvent.type(input, "2026-05-30");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(fetch.mock.calls.some((c) => (c as unknown as Call)[0] === "/api/farms/farm_1/cycles/sown")).toBe(true),
    );
    const post = fetch.mock.calls.find((c) => (c as unknown as Call)[0].endsWith("/sown")) as unknown as Call;
    expect(JSON.parse(String(post[1].body))).toEqual({ day: "2026-05-30" });
  });

  it("shows later cycles side by side with the farmer's own, its warnings, and lets them go back", async () => {
    const l = line();
    const own = cycle("farmer", "groundnut", "Groundnut", "2026-09-01", "2026-12-26");
    l.cycles[1] = {
      ...l.cycles[1],
      position: 1,
      from: "2026-09-23",
      own,
      planned: own,
      checks: [
        {
          id: "cycle.overlap",
          severity: "warning",
          area: 0,
          message: "Black gram now ends 8 September 2026; your Groundnut sowing of 1 September 2026 is before that.",
        },
      ],
    };
    const fetch = server((_, init) => json(init.method === "DELETE" ? line() : l));
    render(<Cycles farm={farm} />);
    await userEvent.click(await screen.findByRole("button", { name: /Plan the next 2 cycles/ }));
    expect(screen.getByRole("heading", { name: "Next cycle" })).toBeInTheDocument();
    expect(screen.getAllByText("Groundnut").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Chickpea").length).toBeGreaterThan(0);
    expect(screen.getByText(/is before that/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Follow the suggestion" }));
    await waitFor(() => expect(fetch.mock.calls.some((c) => (c as unknown as Call)[1].method === "DELETE")).toBe(true));
    const del = fetch.mock.calls.find((c) => (c as unknown as Call)[1].method === "DELETE") as unknown as Call;
    expect(del[0]).toBe("/api/farms/farm_1/cycles/1");
  });

  it("chooses organic, turns later cycles off, and says what the server refused", async () => {
    const fetch = server((path, init) => {
      if (path.endsWith("/settings")) return json({ ...line(), future: false, cycles: line().cycles.slice(0, 1) });
      if (init.method === "PUT") return json({ error: 'method "x"' }, 400);
      return json(line());
    });
    render(<Cycles farm={farm} />);
    await userEvent.click(await screen.findByRole("radio", { name: "organic" }));
    expect(await screen.findByRole("alert")).toHaveTextContent('method "x"');
    const put = fetch.mock.calls.find((c) => (c as unknown as Call)[1].method === "PUT") as unknown as Call;
    expect(JSON.parse(String(put[1].body))).toEqual({ method: "organic" });
    await userEvent.click(screen.getByRole("checkbox", { name: /Plan the cycles after this one/ }));
    await waitFor(() => expect(screen.queryByRole("button", { name: /Plan the next/ })).not.toBeInTheDocument());
  });
});

describe("cycle helpers", () => {
  it("edits each area's crop and date, keeping its boundary", () => {
    const plan = {
      zones: [
        {
          crop: "maize",
          with: "pigeon_pea",
          pattern: [4, 1] as [number, number],
          ring: [{ lat: 1, lng: 2 }],
          row_bearing: 90,
          sow: "2026-06-20",
        },
        { crop: "cotton", ring: [{ lat: 3, lng: 4 }], row_bearing: 0, row_cm: 90, sow: "2026-06-25" },
      ],
    };
    const out = edited(plan, [
      { crop: "maize", sow: "2026-06-28" },
      { crop: "chilli", sow: "2026-07-01" },
    ]);
    expect(out.zones[0]).toEqual({ ...plan.zones[0], sow: "2026-06-28" });
    expect(out.zones[1]).toEqual({ crop: "chilli", ring: [{ lat: 3, lng: 4 }], row_bearing: 0, sow: "2026-07-01" });
  });

  it("places cycles on the timeline inside it", () => {
    expect(span("2026-01-01", "2026-01-11", "2026-01-01", "2026-01-06")).toEqual({ left: 0, width: 50 });
    const out = span("2026-01-01", "2026-01-11", "2025-12-01", "2027-01-01");
    expect(out.left).toBe(0);
    expect(out.width).toBe(100);
  });

  it("tells the same cycle from a different one", () => {
    const a = cycle("suggested", "maize", "Maize", "2026-06-20", "2026-09-20");
    expect(sameCycle(a, { ...a, origin: "farmer" })).toBe(true);
    expect(sameCycle(a, cycle("farmer", "maize", "Maize", "2026-06-21", "2026-09-21"))).toBe(false);
    expect(sameCycle(a, undefined)).toBe(false);
  });
});
