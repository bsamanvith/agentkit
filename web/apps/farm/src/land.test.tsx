import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Land } from "./land";
import { LandView } from "./landpicture";
import { ResearchSettings } from "./research";
import fixture from "./testdata/kothapeta.json";
import type { Design, DesignReport, Farm, Survey } from "./types";

const survey = fixture.survey as unknown as Survey;
const design = fixture.design as Design;
const report = fixture.report as DesignReport;

const farm: Farm = {
  id: "farm_1",
  name: "Kothapeta paddy",
  boundary: { corners: fixture.corners },
  report: { status: "verified", checks: [], checked_at: "2026-11-01T06:00:00Z" },
  survey,
  created_at: "2026-11-01T06:00:00Z",
  updated_at: "2026-11-01T06:00:00Z",
};

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

afterEach(() => vi.unstubAllGlobals());

describe("LandView", () => {
  it("draws the plot, its surroundings, the layout and lines to the nearest features", () => {
    const { container } = render(<LandView corners={fixture.corners} survey={survey} design={design} />);
    expect(screen.getByRole("img", { name: /picture of the plot/i })).toBeInTheDocument();
    const svg = container.querySelector("svg.lv")!;
    // The boundary draws itself in; each corner pops in.
    expect(svg.querySelectorAll("path.lv-draw")).toHaveLength(1);
    expect(svg.querySelectorAll("circle.lv-pop").length).toBeGreaterThanOrEqual(4);
    // Water flows; the pond ripples; border trees stand round the plot.
    expect(svg.querySelectorAll(".lv-flow").length).toBeGreaterThan(0);
    expect(svg.querySelectorAll(".lv-ripple")).toHaveLength(2);
    expect(svg.querySelectorAll('circle[fill="#4d7c0f"]').length).toBeGreaterThan(50);
    // Callouts name the nearest drain and road.
    expect(within(svg as unknown as HTMLElement).getByText(/Drain 98 m E/)).toBeInTheDocument();
    const legend = screen.getByRole("list", { name: "Legend" });
    for (const want of ["Your plot", "Farm pond", "Shed", "Border trees", "Drain", "Road", "Canal"]) {
      expect(within(legend).getByText(new RegExp(want))).toBeInTheDocument();
    }
    expect(screen.getByText(/OpenStreetMap contributors/)).toBeInTheDocument();
  });

  it("zooms out, tilts and plays the season", async () => {
    const user = userEvent.setup();
    const zones = [{ crop: "paddy", ring: fixture.corners, row_bearing: 0, row_cm: 20, sow: "2027-06-20" }];
    const crops = [{ id: "paddy", name: "Paddy", days: [110, 130] }] as never;
    const { container } = render(<LandView corners={fixture.corners} survey={survey} zones={zones} crops={crops} />);
    const before = container.querySelector("svg.lv")!.getAttribute("viewBox")!;
    await user.click(screen.getByRole("button", { name: /around it/i }));
    const after = container.querySelector("svg.lv")!.getAttribute("viewBox")!;
    expect(Number(after.split(" ")[2])).toBeGreaterThan(Number(before.split(" ")[2]));
    await user.click(screen.getByRole("button", { name: /tilt/i }));
    expect(container.querySelector("svg.lv")!.getAttribute("data-tilt")).toBe("true");
    const slider = screen.getByRole("slider", { name: /day of the season/i });
    // Before sowing the rows are bare; at harvest they are gold.
    const pattern = () => container.querySelector("#lv-rows-0 line");
    await user.click(screen.getByRole("button", { name: /play the season/i }));
    await user.click(screen.getByRole("button", { name: /pause the season/i }));
    expect(slider).toBeInTheDocument();
    const max = Number(slider.getAttribute("max"));
    // Drive the slider to the end with a change event.
    const { fireEvent } = await import("@testing-library/react");
    fireEvent.change(slider, { target: { value: String(max) } });
    expect(pattern()?.getAttribute("stroke")).toBe("rgb(214 174 54)");
    fireEvent.change(slider, { target: { value: slider.getAttribute("min") } });
    expect(pattern()).toBeNull();
  });
});

describe("Land", () => {
  it("shows the shape and surroundings, suggests a layout and saves it", async () => {
    const user = userEvent.setup();
    const calls: { method: string; url: string; body?: string }[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string, init: RequestInit) => {
        calls.push({ method: init.method ?? "GET", url, body: init.body as string | undefined });
        if (url.endsWith("/design/suggest")) return json({ design, report });
        if (url.endsWith("/design")) return json({ ...farm, design, design_report: report });
        return json({}, 404);
      }),
    );
    const onFarm = vi.fn();
    render(<Land farm={farm} onFarm={onFarm} />);
    const card = screen.getByRole("region", { name: "The land and around it" });
    expect(within(card).getByTestId("shape")).toHaveTextContent("Square");
    expect(within(card).getByText("Drain 98 m E")).toBeInTheDocument();
    expect(within(card).getByText(/falls .* to the SE/)).toBeInTheDocument();

    const shaping = screen.getByRole("region", { name: "Shape the farm" });
    await user.click(within(shaping).getByLabelText("Border trees"));
    await user.click(within(shaping).getByRole("button", { name: /suggest a layout/i }));
    await waitFor(() => expect(within(shaping).getByText("Looks sound")).toBeInTheDocument());
    expect(JSON.parse(calls[0]!.body!)).toEqual({ gate: true, path: true, shed: true, pond: true, border: false });
    expect(within(shaping).getByText(/holds 675 m³/)).toBeInTheDocument();
    expect(within(shaping).getByText("Suggested, not saved")).toBeInTheDocument();

    await user.click(within(shaping).getByRole("button", { name: /save layout/i }));
    await waitFor(() => expect(onFarm).toHaveBeenCalled());
    expect(calls[1]).toMatchObject({ method: "PUT", url: "/api/farms/farm_1/design" });
  });

  it("looks at the map when the plot has no survey yet", async () => {
    const fetch = vi.fn(async () => json(survey));
    vi.stubGlobal("fetch", fetch);
    render(<Land farm={{ ...farm, survey: undefined }} onFarm={() => {}} />);
    expect(screen.getByText(/looking at the map/i)).toBeInTheDocument();
    await screen.findByRole("region", { name: "The land and around it" });
    expect(fetch).toHaveBeenCalledWith("/api/farms/farm_1/survey", expect.objectContaining({ method: "GET" }));
  });
});

describe("ResearchSettings", () => {
  it("connects a provider with the farmer's key and never shows it back", async () => {
    const user = userEvent.setup();
    const providers = [
      { id: "exa", name: "Exa", sign_up: "https://dashboard.exa.ai", connected: false },
      { id: "parallel", name: "Parallel", sign_up: "https://platform.parallel.ai", connected: false },
    ];
    const fetch = vi.fn(async (url: string, init: RequestInit) => {
      if (init.method === "PUT") return json({ providers: [{ ...providers[0], connected: true }, providers[1]] });
      expect(url).toBe("/api/research");
      return json({ providers });
    });
    vi.stubGlobal("fetch", fetch);
    render(<ResearchSettings onClose={() => {}} />);
    const exa = await screen.findByRole("form", { name: "Exa" });
    await user.type(within(exa).getByLabelText("Exa API key"), "exa-key-123456");
    await user.click(within(exa).getByRole("button", { name: "Connect" }));
    await waitFor(() => expect(within(exa).getByText("Connected")).toBeInTheDocument());
    expect(fetch).toHaveBeenLastCalledWith(
      "/api/research/exa",
      expect.objectContaining({ method: "PUT", body: JSON.stringify({ key: "exa-key-123456" }) }),
    );
    expect(within(exa).getByLabelText("Exa API key")).toHaveValue("");
  });
});
