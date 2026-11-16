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

const starter = "These are starter values from the sources listed, for an agronomist to review.";
const label = "Always follow the product label.";

/** The inputs and timing addons as the farm binary puts them on a cycle. */
function addons(method: "organic" | "conventional", sell: boolean): Record<string, unknown> {
  return {
    inputs: {
      fields: [{ area: 0, crop: "black_gram", crop_name: "Black gram", hectares: 0.4, acres: 1, sow: "2026-06-20" }],
      acres: 1,
      method,
      water: "rainfed",
      notes: [starter, label],
      buy_before:
        method === "organic"
          ? [{ product: "fym", name: "Farmyard manure", make: true, need: 5, qty: 5, unit: "t", first: "2026-05-30" }]
          : [
              {
                product: "dap",
                name: "DAP",
                synthetic: true,
                need: 87,
                qty: 100,
                unit: "kg",
                packs: 2,
                pack: "50 kg bag",
                first: "2026-06-20",
              },
            ],
      buy_later: [{ product: "gypsum", name: "Gypsum", need: 320, qty: 320, unit: "kg", first: "2026-08-04" }],
      if_seen: [
        {
          field: 0,
          crop: "black_gram",
          problem: "pod_borer",
          name: "Pod borer",
          kind: "pest",
          signs: "Holes in the pods.",
          threshold: "1 larva a plant",
          options: [{ route: "organic", name: "Neem oil", rate: "3 ml a litre", water_per_acre: "200 litres" }],
        },
      ],
      schedule: [
        {
          field: 0,
          crop: "black_gram",
          key: "treat-seed",
          date: "2026-06-19",
          das: -1,
          stage: "",
          kind: "seed-treatment",
          title: "Treat the seed",
          doses: [{ product: "pf", name: "Pseudomonas", qty: 2500, unit: "g", per_acre: 1011 }],
          cautions: ["Wear gloves."],
        },
        {
          field: 0,
          crop: "black_gram",
          key: "spray-x",
          date: "2026-07-20",
          das: 30,
          stage: "vegetative",
          kind: "spray",
          title: "Spray",
          doses: [{ product: "neem", name: "Neem oil", qty: 1.2, unit: "l", per_acre: 1.2 }],
          water_per_acre: "200 litres",
          phi_days: 3,
          cautions: ["Do not spray in wind."],
        },
      ],
    },
    timing: {
      crop: "black_gram",
      crop_name: "Black gram",
      commodity: "Black Gram (Urd Beans)(Whole)",
      area: "Kadapa district, Andhra Pradesh",
      index: sell ? {} : undefined,
      selling: sell
        ? {
            sell_now: false,
            month: 1,
            months_ahead: 3,
            from: 10,
            price: 7000,
            gain_pct: 6.2,
            reasons: ["Prices rise after the glut."],
          }
        : undefined,
      reasons: sell ? [] : ["Too few years of prices."],
    },
  };
}

describe("Cycles", () => {
  it("keeps what to buy, feeding and sprays, and when to sell one tap away, with the notes wherever doses show", async () => {
    const l = line();
    l.cycles[0] = { ...l.cycles[0]!, addons: addons("conventional", true) };
    const organic = line();
    organic.cycles[0] = { ...organic.cycles[0]!, addons: addons("organic", false) };
    organic.cycles[0]!.planned = { ...organic.cycles[0]!.planned!, method: "organic" };
    const fetch = server((_path, init) => json(init.method === "PUT" ? organic : l));
    render(<Cycles farm={farm} />);
    const buy = await screen.findByRole("button", { name: /What to buy/ });
    // Collapsed: nothing of it shows until tapped.
    expect(buy).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("DAP")).not.toBeInTheDocument();
    await userEvent.click(buy);
    expect(screen.getByText("DAP")).toBeInTheDocument();
    expect(screen.getByText("2 × 50 kg bag")).toBeInTheDocument();
    // One number: the need as the schedule says it, the buy rounded up and said so.
    expect(screen.getByText(/87 kg needed/)).toBeInTheDocument();
    expect(screen.getByText("Gypsum")).toBeInTheDocument();
    expect(screen.getByText(/Only if you see/)).toBeInTheDocument();
    expect(screen.getByText(starter)).toBeInTheDocument();
    expect(screen.getByText(label)).toBeInTheDocument();
    // Feeding and sprays: large gram amounts in kilograms, the water and the wait.
    await userEvent.click(screen.getByRole("button", { name: /Feeding and sprays/ }));
    expect(screen.getByText(/2\.5 kg/)).toBeInTheDocument();
    expect(screen.getByText(/In 200 litres of water an acre/)).toBeInTheDocument();
    expect(screen.getByText(/Wait 3 days after it before harvest/)).toBeInTheDocument();
    expect(screen.getAllByText(starter).length).toBe(1); // one section open at a time, each with its notes
    await userEvent.click(screen.getByRole("button", { name: /When to sell/ }));
    expect(screen.getByText(/Hold until January: about \+6% after storage loss and cost/)).toBeInTheDocument();
    // Organic recomputes the line; this one has no timing advice: a line, not a section.
    await userEvent.click(screen.getByRole("radio", { name: "Organic" }));
    await waitFor(() => expect(screen.queryByRole("button", { name: /When to sell/ })).not.toBeInTheDocument());
    const put = fetch.mock.calls.find((c) => (c as unknown as Call)[1].method === "PUT") as unknown as Call;
    expect(JSON.parse(String(put[1].body))).toEqual({ method: "organic" });
    expect(screen.getByText(/not enough black gram price history here yet/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /What to buy/ }));
    expect(screen.getByText("Farmyard manure")).toBeInTheDocument();
    expect(screen.queryByText("DAP")).not.toBeInTheDocument();
  });

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

  it("records a crop the farmer already sowed that is not the planned one, in one step", async () => {
    const fetch = server((path) =>
      path === "/api/crops" ? json({ crops: [{ id: "groundnut", name: "Groundnut", kind: "oilseed" }] }) : json(line()),
    );
    render(<Cycles farm={farm} />);
    await userEvent.click(await screen.findByRole("button", { name: /I already sowed/ }));
    await screen.findByRole("option", { name: "Groundnut" });
    await userEvent.selectOptions(screen.getByLabelText("What I sowed"), "groundnut");
    const input = screen.getByLabelText("Sown on");
    await userEvent.clear(input);
    await userEvent.type(input, "2026-05-28");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.mock.calls.some((c) => (c as unknown as Call)[0].endsWith("/sown"))).toBe(true));
    const post = fetch.mock.calls.find((c) => (c as unknown as Call)[0].endsWith("/sown")) as unknown as Call;
    expect(JSON.parse(String(post[1].body))).toEqual({ day: "2026-05-28", crop: "groundnut" });
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
    await userEvent.click(await screen.findByRole("button", { name: /What comes after: the next 2 crops/ }));
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
    await userEvent.click(await screen.findByRole("radio", { name: "Organic" }));
    expect(await screen.findByRole("alert")).toHaveTextContent('method "x"');
    const put = fetch.mock.calls.find((c) => (c as unknown as Call)[1].method === "PUT") as unknown as Call;
    expect(JSON.parse(String(put[1].body))).toEqual({ method: "organic" });
    // Planning the later crops is under "What comes after", out of the way.
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /What comes after/ }));
    await userEvent.click(screen.getByRole("checkbox", { name: /Plan the crops after this one/ }));
    await waitFor(() => expect(screen.queryByRole("button", { name: /What comes after/ })).not.toBeInTheDocument());
    // Off, the box stays in sight to turn them on again.
    expect(screen.getByRole("checkbox", { name: /Plan the crops after this one/ })).not.toBeChecked();
  });
});

describe("Cycles, moving on", () => {
  // Today is a week before the black gram comes off: the server says the
  // line may move on from two weeks before it.
  const near = (future: boolean): Line => {
    const l = { ...line(), today: "2026-09-01", advance_from: "2026-08-25", future };
    return future ? l : { ...l, cycles: l.cycles.slice(0, 1) };
  };
  const advanced = (fetch: ReturnType<typeof server>) =>
    fetch.mock.calls.some((c) => (c as unknown as Call)[0] === "/api/farms/farm_1/cycles/advance");

  it("asks in the page, saying what will happen, before it moves on", async () => {
    const fetch = server(() => json(near(true)));
    render(<Cycles farm={farm} />);
    await userEvent.click(await screen.findByRole("button", { name: /Harvested: start the next cycle/ }));
    expect(advanced(fetch)).toBe(false);
    const ask = screen.getByRole("group", { name: /Start the next cycle/ });
    expect(ask).toHaveTextContent(/Chickpea becomes this cycle/);
    expect(ask).toHaveTextContent(/Black gram is kept as the crop before/);
    expect(ask).toHaveTextContent(/cannot be undone/);
    await userEvent.click(screen.getByRole("button", { name: "Not yet" }));
    expect(screen.queryByRole("group", { name: /Start the next cycle/ })).not.toBeInTheDocument();
    expect(advanced(fetch)).toBe(false);
    await userEvent.click(screen.getByRole("button", { name: /Harvested: start the next cycle/ }));
    await userEvent.click(screen.getByRole("button", { name: "Yes, start the next cycle" }));
    await waitFor(() => expect(advanced(fetch)).toBe(true));
  });

  it("is there with the later cycles turned off, and not before the day the server says", async () => {
    server(() => json(near(false)));
    const { unmount } = render(<Cycles farm={farm} />);
    await userEvent.click(await screen.findByRole("button", { name: /Harvested: start the next cycle/ }));
    expect(screen.getByRole("group", { name: /Start the next cycle/ })).toHaveTextContent(
      /The crop we suggest next becomes this cycle/,
    );
    unmount();
    server(() => json({ ...near(true), today: "2026-08-24" }));
    render(<Cycles farm={farm} />);
    await screen.findByText("This cycle");
    expect(screen.queryByRole("button", { name: /Harvested/ })).not.toBeInTheDocument();
  });
});

describe("Cycles, what the farmer sees first", () => {
  it("says plainly when nothing suits sowing soon, what to do meanwhile, and what comes next", async () => {
    const l = line();
    l.assumed = true;
    l.cycles[0] = {
      ...l.cycles[0]!,
      rest: { id: "cycle.rest", message: "Nothing suits sowing on rainfed land before June." },
      addons: addons("conventional", false),
    };
    server(() => json(l));
    render(<Cycles farm={farm} />);
    const rest = await screen.findByText("Nothing suits sowing on rainfed land before June.");
    const card = rest.closest("section")!;
    // First thing in the card: before the crop and its dates.
    expect(card.textContent!.indexOf("Nothing suits")).toBeLessThan(card.textContent!.indexOf("What comes next"));
    expect(screen.getByText(/The land rests/)).toBeInTheDocument();
    expect(screen.getByText(/Treat the seed · from/)).toBeInTheDocument(); // the work before sowing
    expect(screen.getByText(/Sow Black gram from/)).toBeInTheDocument();
    expect(screen.getByText(/Assuming rain only, no irrigation/)).toBeInTheDocument();
  });

  it("sets the water and soil in one tap from the assumption, and suggests again", async () => {
    const l = line();
    l.assumed = true;
    let saved = false;
    const fetch = server((path, init) => {
      if (path === "/api/soils") return json({ soils: [{ id: "red", name: "Red soil", signs: "" }] });
      if (path.endsWith("/profile")) {
        saved = true;
        return json({ ...farm, profile: JSON.parse(String(init.body)) });
      }
      return json(saved ? line() : l);
    });
    const onFarm = vi.fn();
    render(<Cycles farm={{ ...farm, profile: { previous_crop: "maize" } }} onFarm={onFarm} />);
    await userEvent.click(await screen.findByRole("button", { name: /Change: water and soil/ }));
    await userEvent.click(screen.getByRole("button", { name: "Borewell" }));
    await screen.findByRole("option", { name: "Red soil" });
    await userEvent.selectOptions(screen.getByLabelText("Soil"), "red");
    await userEvent.click(screen.getByRole("button", { name: /Save and suggest again/ }));
    await waitFor(() => expect(onFarm).toHaveBeenCalled());
    const put = fetch.mock.calls.find((c) => (c as unknown as Call)[0].endsWith("/profile")) as unknown as Call;
    // What was there is kept; the water and soil are the farmer's.
    expect(JSON.parse(String(put[1].body))).toEqual({
      previous_crop: "maize",
      soil: "red",
      water: [{ kind: "borewell", reliable: true }],
    });
    // The line is asked again: with the water known, the assumption goes.
    await waitFor(() => expect(screen.queryByText(/Assuming rain only/)).not.toBeInTheDocument());
    expect(fetch.mock.calls.filter((c) => (c as unknown as Call)[0] === "/api/farms/farm_1/cycles").length).toBe(2);
  });

  it("once sown, drops what we would have suggested and the work whose time has passed", async () => {
    const l = line();
    const own = cycle("farmer", "finger_millet", "Finger millet", "2026-05-28", "2026-09-20");
    own.sow_kind = "actual";
    own.areas[0]!.sow_kind = "actual";
    l.cycles[0] = { ...l.cycles[0]!, own, planned: own, addons: addons("conventional", false) };
    l.cycles[0].suggested = cycle("suggested", "chilli", "Chilli", "2027-06-01", "2027-11-01");
    server((path) => {
      if (path.endsWith("/roadmap"))
        return json({
          farm: "farm_1",
          today: "2026-06-01",
          zones: [],
          tasks: [
            { id: "p", title: "Plough and spread manure", date: "2026-05-07", state: "open", passed: true },
            { id: "g", title: "Fill the gaps", date: "2026-06-05", state: "open" },
          ],
          counts: {},
        });
      return json(l);
    });
    render(<Cycles farm={{ ...farm, plan: { zones: own.plan.zones } }} />);
    expect(await screen.findByText("Fill the gaps")).toBeInTheDocument();
    expect(screen.queryByText(/Plough/)).not.toBeInTheDocument();
    expect(screen.queryByText(/We would have suggested/)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /I already sowed/ })).not.toBeInTheDocument();
    expect(screen.getByText(/^Sown /)).toBeInTheDocument();
    // What was bought before sowing is behind the farmer: what is still to buy leads.
    const buy = await screen.findByRole("button", { name: /What to buy/ });
    expect(buy).toHaveTextContent("1 still to buy");
    expect(buy).not.toHaveTextContent("before sowing");
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
  it("keeps crop insurance one tap away: last date and days left, the 72-hour rule, the source and read date, last year's dates as such", async () => {
    const l = line();
    const source = {
      id: "pmfby-og-2020",
      name: "PMFBY Revamped Operational Guidelines",
      url: "https://pmfby.gov.in/x.pdf",
      where: "para 16.2",
      read: "2026-11-15",
      layer: "base",
    };
    l.cycles[0] = {
      ...l.cycles[0]!,
      addons: {
        schemes: {
          season: "kharif",
          year: 2026,
          confirm: "Confirm at the bank, the Rythu Seva Kendra or the insurer before you rely on this.",
          items: [
            {
              scheme: "pmfby",
              name: "Pradhan Mantri Fasal Bima Yojana (PMFBY)",
              kind: "insurance",
              cover: "notified",
              status: "open",
              what: "enrolment",
              by: "2026-07-15",
              days_left: 44,
              loss_notice_hours: 72,
              says: "",
              source,
            },
            {
              scheme: "ap-2026",
              name: "Crop insurance in Andhra Pradesh, kharif 2026",
              kind: "insurance",
              cover: "named",
              crops: ["black_gram"],
              for_season: "kharif 2026",
              status: "stale",
              says: "",
              source: { ...source, id: "dc", name: "Deccan Chronicle", layer: "Andhra Pradesh" },
            },
          ],
        },
      },
    };
    server(() => json(l));
    render(<Cycles farm={farm} />);
    const section = await screen.findByRole("button", { name: /Insurance/ });
    expect(section).toHaveAttribute("aria-expanded", "false");
    expect(section).toHaveTextContent(/Enrol by 15 Jul( \d{4})?, 44 days left · report damage within 72 hours/);
    expect(screen.queryByText(/Rythu Seva Kendra/)).not.toBeInTheDocument();
    await userEvent.click(section);
    expect(screen.getByText(/Last date to enrol: 15 Jul( \d{4})? · 44 days left/)).toBeInTheDocument();
    expect(screen.getByText("If the crop is damaged: report within 72 hours.")).toBeInTheDocument();
    expect(screen.getByText(/Covers the crops the state notifies each season/)).toBeInTheDocument();
    expect(screen.getByText("Covers your crop")).toBeInTheDocument();
    expect(screen.getByText("Last year's dates (kharif 2026), not yet announced for this season")).toBeInTheDocument();
    expect(screen.getAllByText(/read 15 Nov/)).toHaveLength(2);
    expect(screen.getByRole("link", { name: "PMFBY Revamped Operational Guidelines" })).toHaveAttribute(
      "href",
      "https://pmfby.gov.in/x.pdf",
    );
    expect(screen.getByText(/Confirm before you rely on this/)).toBeInTheDocument();
  });
});
