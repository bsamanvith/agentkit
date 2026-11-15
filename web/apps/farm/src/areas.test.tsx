import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { AreaEditor } from "./areas";
import { areaM2, plane } from "./draw";
import type { Stroke, ZoneShape } from "./map";
import type { Point } from "./types";

interface MapProps {
  stroke?: Stroke;
  onStroke?: (pts: Point[]) => void;
  zones?: ZoneShape[];
}

// Leaflet cannot run in jsdom: a stand-in keeps the props so a test can
// draw on the map.
const map = vi.hoisted(() => ({ props: undefined as unknown as MapProps }));
vi.mock("./map", () => ({
  FarmMap: (p: MapProps) => {
    map.props = p;
    return <div data-testid="map" />;
  },
}));

const pl = plane({ lat: 16.29, lng: 80.45 });
const at = (x: number, y: number) => pl.point({ x, y });
const plot = [at(0, 0), at(100, 0), at(100, 100), at(0, 100)];

describe("AreaEditor", () => {
  it("keeps the plot as one area until the farmer divides it", async () => {
    const onDone = vi.fn();
    render(<AreaEditor plot={plot} onDone={onDone} onCancel={() => {}} />);
    expect(map.props.zones).toHaveLength(1);
    await userEvent.setup().click(screen.getByRole("button", { name: "Keep one area" }));
    expect(onDone).toHaveBeenCalledWith([plot]);
  });

  it("cuts the plot in two along a line drawn across it, and the two areas become what it hands back", async () => {
    const onDone = vi.fn();
    render(<AreaEditor plot={plot} onDone={onDone} onCancel={() => {}} />);
    expect(map.props.stroke).toBe("segment");
    act(() => map.props.onStroke?.([at(40, -10), at(40, 110)]));
    expect(map.props.zones).toHaveLength(2);
    expect(screen.getByRole("list", { name: "Areas" })).toHaveTextContent(
      /Area 1: 1.48 acres \(60%\)Area 2: 0.99 acres \(40%\)/,
    );
    await userEvent.setup().click(screen.getByRole("button", { name: "Use these 2 areas" }));
    const areas = onDone.mock.calls[0]?.[0] as Point[][];
    expect(areas.map(areaM2).reduce((a, b) => a + b, 0)).toBeCloseTo(areaM2(plot), -1);
  });

  it("makes a rectangle drawn inside the plot an area, and undo takes it away", async () => {
    render(<AreaEditor plot={plot} onDone={() => {}} onCancel={() => {}} />);
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: /rectangle/i }));
    expect(map.props.stroke).toBe("box");
    act(() => map.props.onStroke?.([at(20, 20), at(50, 60)]));
    // The shape and the rest around it, cut in two so no area has a hole.
    expect(map.props.zones).toHaveLength(3);
    await user.click(screen.getByRole("button", { name: /undo/i }));
    expect(map.props.zones).toHaveLength(1);
  });

  it("says so when a line does not cross the plot, and changes nothing", () => {
    render(<AreaEditor plot={plot} onDone={() => {}} onCancel={() => {}} />);
    act(() => map.props.onStroke?.([at(150, 0), at(150, 100)]));
    expect(map.props.zones).toHaveLength(1);
    expect(screen.getByRole("status")).toHaveTextContent(/right across the plot/);
  });

  it("lets one finger move the map when asked", async () => {
    render(<AreaEditor plot={plot} onDone={() => {}} onCancel={() => {}} />);
    await userEvent.setup().click(screen.getByRole("button", { name: /move map/i }));
    expect(map.props.stroke).toBeUndefined();
  });
});
