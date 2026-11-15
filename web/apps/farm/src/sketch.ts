import { useRef, useState } from "react";
import { dragRect, freehandRing, rectCorners, rectFromDrag, rectHandles, type Rect, type RectHandle } from "./draw";
import type { Handle, Stroke } from "./map";
import type { Corner, Point } from "./types";

/** The steps of the guided first plot (the editor's guided mode): where, the boundary, its name. */
export const guidedSteps = ["Find your village", "Draw your plot", "Name it"] as const;

/** How the farmer draws a plot on the map: freehand, corner by corner, or a rectangle. */
export type Tool = "pencil" | "lines" | "rect";

const handleInfo: Record<RectHandle, { label: string; glyph: string }> = {
  turn: { label: "Turn the rectangle", glyph: "↻" },
  length: { label: "Stretch its length", glyph: "↕" },
  width: { label: "Stretch its width", glyph: "↔" },
};

/** What the map needs to draw with a tool: what a drag draws, and the handles to show. */
export interface SketchMap {
  stroke?: Stroke;
  onStroke: (pts: Point[]) => void;
  handles: Handle[];
  onHandle: (id: string, at: Point) => void;
  /** Corners drag. */
  editable: boolean;
  /** Taps add corners. */
  taps: boolean;
  onChange: (c: Corner[]) => void;
}

/**
 * The drawing state of a plot's boundary: the corners (kept by the
 * caller, set through apply), the rectangle being shaped, and the history
 * for undo. Every change goes through set, so undo steps back over taps,
 * strokes, rectangles, GPS corners and typed ones alike.
 */
export function useSketch(corners: Corner[], apply: (c: Corner[]) => void, tool: Tool, panning = false) {
  const [past, setPast] = useState<Corner[][]>([]);
  const [rect, setRect] = useState<Rect>();
  // Why the last stroke drew nothing, for the farmer; cleared by the next change.
  const [problem, setProblem] = useState("");
  // The handle being dragged: a drag is one step of the history.
  const dragging = useRef<string>(undefined);

  const set = (c: Corner[], r?: Rect) => {
    setPast((p) => [...p.slice(-49), corners]);
    setProblem("");
    setRect(r);
    dragging.current = undefined;
    apply(c);
  };
  const undo = () => {
    const prev = past[past.length - 1];
    if (!prev) return;
    setPast(past.slice(0, -1));
    setRect(undefined);
    dragging.current = undefined;
    apply(prev);
  };
  const onStroke = (pts: Point[]) => {
    if (tool === "pencil") {
      const { ring, problem } = freehandRing(pts);
      if (ring) set(ring);
      else setProblem(problem ?? "");
    } else if (tool === "rect") {
      const [a, b] = pts;
      const r = a && b ? rectFromDrag(a, b) : undefined;
      if (r) set(rectCorners(r), r);
    }
  };
  const onHandle = (id: string, at: Point) => {
    if (!rect) return;
    const r = dragRect(rect, id as RectHandle, at);
    if (dragging.current !== id) setPast((p) => [...p.slice(-49), corners]);
    dragging.current = id;
    setRect(r);
    apply(rectCorners(r));
  };
  const shown = tool === "rect" ? rect : undefined;
  const map: SketchMap = {
    stroke: panning ? undefined : tool === "pencil" ? "free" : tool === "rect" ? "box" : undefined,
    onStroke,
    handles: shown
      ? Object.entries(rectHandles(shown)).map(([id, at]) => ({ id, at, ...handleInfo[id as RectHandle] }))
      : [],
    onHandle,
    editable: true,
    taps: tool === "lines",
    // A tap (lines) or a corner dragged: the rectangle, if any, is now just corners.
    onChange: (c) => set(c),
  };
  return { set, undo, canUndo: past.length > 0, rect: shown, map, problem };
}
