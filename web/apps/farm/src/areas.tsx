import { Button } from "@agentkit/ui/components/button";
import { Hand, Pencil, Scissors, Square, Undo2 } from "lucide-react";
import { useMemo, useState } from "react";
import { addShape, areaM2, cutAreas, freehandRing, strokeCrosses, rectCorners, rectFromDrag } from "./draw";
import { FarmMap, type Stroke, type ZoneShape } from "./map";
import { colors } from "./plan";
import type { Point } from "./types";

/** The most areas a plot is divided into here (the plan takes up to 50 zones). */
export const maxAreas = 12;

type AreaTool = "cut" | "shape" | "rect";

const tools: { id: AreaTool; label: string; icon: typeof Pencil; stroke: Stroke; help: string }[] = [
  {
    id: "cut",
    label: "Cut with a line",
    icon: Scissors,
    stroke: "segment",
    help: "Drag a line right across the plot, from one edge to the other: it cuts the area it crosses in two.",
  },
  {
    id: "shape",
    label: "Draw a shape",
    icon: Pencil,
    stroke: "free",
    help: "Draw round a part of the plot with your finger: it becomes an area of its own.",
  },
  {
    id: "rect",
    label: "Rectangle",
    icon: Square,
    stroke: "box",
    help: "Drag from corner to corner over a part of the plot: it becomes an area of its own.",
  },
];

const acres = (m2: number) => (m2 / 4046.8564224).toFixed(2);

/**
 * Divides a plot into areas, each to get its own crop: cut it along a line,
 * or draw a shape inside it. The areas always cover the plot without
 * overlapping; the plan check on the server checks them again as zones.
 */
export function AreaEditor({
  plot,
  initial,
  onDone,
  onCancel,
}: {
  plot: Point[];
  initial?: Point[][];
  onDone: (areas: Point[][]) => void;
  onCancel: () => void;
}) {
  const [areas, setAreas] = useState<Point[][]>(initial && initial.length > 0 ? initial : [plot]);
  const [past, setPast] = useState<Point[][][]>([]);
  const [tool, setTool] = useState<AreaTool>("cut");
  const [panning, setPanning] = useState(false);
  const [note, setNote] = useState("");
  const set = (next: Point[][] | undefined, miss: string) => {
    if (!next) {
      setNote(miss);
      return;
    }
    if (next.length > maxAreas) {
      setNote(`That makes ${next.length} areas; at most ${maxAreas}.`);
      return;
    }
    setPast((p) => [...p, areas]);
    setAreas(next);
    setNote("");
  };
  const onStroke = (pts: Point[]) => {
    const [a, b] = pts;
    if (tool === "cut") {
      if (a && b) set(cutAreas(areas, a, b), "Draw the line right across the plot, from one edge to the other.");
      return;
    }
    const drawn = tool === "shape" ? freehandRing(pts) : undefined;
    if (drawn?.problem === strokeCrosses) {
      set(undefined, strokeCrosses);
      return;
    }
    const r = tool === "shape" ? drawn?.ring : a && b ? rectFromDrag(a, b) : undefined;
    const ring = r === undefined ? undefined : Array.isArray(r) ? r : rectCorners(r);
    set(ring && addShape(areas, ring), "Draw the shape over the plot, a little bigger than a few steps across.");
  };
  const zones: ZoneShape[] = useMemo(
    () => areas.map((ring, i) => ({ ring, label: String(i + 1), color: colors[i % colors.length] ?? "#16a34a" })),
    [areas],
  );
  const plotM2 = areaM2(plot);
  const help = tools.find((t) => t.id === tool)?.help;

  return (
    <section aria-label="Areas of the plot" className="flex flex-col gap-3 rounded-lg border p-4">
      <h2 className="font-semibold">Areas of the plot</h2>
      <p className="text-sm text-muted-foreground">
        Divide the plot if you grow different crops in different parts. One area is fine too.
      </p>
      <div className="flex flex-wrap items-center gap-2" role="toolbar" aria-label="Area tools">
        {tools.map((t) => (
          <Button
            key={t.id}
            size="sm"
            variant={tool === t.id && !panning ? "default" : "outline"}
            aria-pressed={tool === t.id && !panning}
            onClick={() => {
              setTool(t.id);
              setPanning(false);
            }}
          >
            <t.icon /> {t.label}
          </Button>
        ))}
        <Button
          size="sm"
          variant={panning ? "default" : "ghost"}
          aria-pressed={panning}
          onClick={() => setPanning((p) => !p)}
        >
          <Hand /> Move map
        </Button>
      </div>
      <p className="text-sm text-muted-foreground">
        {panning ? "Drag to move the map; tap a tool to draw again." : `${help} Two fingers move the map.`}
      </p>
      <FarmMap
        corners={plot}
        zones={zones}
        fit={1}
        stroke={panning ? undefined : tools.find((t) => t.id === tool)?.stroke}
        onStroke={onStroke}
        className="h-[55vh] min-h-72 w-full overflow-hidden rounded-lg border"
      />
      {note && (
        <p role="status" className="text-sm text-ak-warn">
          {note}
        </p>
      )}
      <ol aria-label="Areas" className="flex flex-wrap gap-2 text-sm">
        {areas.map((a, i) => (
          <li key={i} className="flex items-center gap-1.5 rounded-md border px-2 py-1">
            <span
              className="size-3 shrink-0 rounded-sm"
              aria-hidden="true"
              style={{ background: colors[i % colors.length] }}
            />
            Area {i + 1}: {acres(areaM2(a))} acres ({Math.round((areaM2(a) / plotM2) * 100)}%)
          </li>
        ))}
      </ol>
      <div className="flex flex-wrap gap-2">
        <Button onClick={() => onDone(areas)}>
          {areas.length === 1 ? "Keep one area" : `Use these ${areas.length} areas`}
        </Button>
        <Button
          variant="outline"
          disabled={past.length === 0}
          onClick={() => {
            const prev = past[past.length - 1];
            if (!prev) return;
            setPast(past.slice(0, -1));
            setAreas(prev);
          }}
        >
          <Undo2 /> Undo
        </Button>
        {areas.length > 1 && (
          <Button variant="outline" onClick={() => set([plot], "")}>
            One area again
          </Button>
        )}
        <Button variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </section>
  );
}
