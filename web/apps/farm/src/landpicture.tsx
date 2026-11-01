import { Button } from "@agentkit/ui/components/button";
import { Box, Map as MapIcon, Pause, Play, RotateCcw, Scan } from "lucide-react";
import { type CSSProperties, useEffect, useMemo, useRef, useState } from "react";
import {
  bounds,
  clipSegment,
  cropColor,
  elementLooks,
  fitAspect,
  growth,
  looks,
  metres,
  nearbyLabel,
  pathOf,
  planSpan,
  projector,
  ringArea,
  roadWidth,
  scaleLength,
  season,
  treesAlong,
  type XY,
} from "./landview";
import { colors, cropLabel, formatDate } from "./plan";
import type { Crop, Design, Feature, FeatureKind, Nearby, Point, Survey, Zone } from "./types";

const aspect = 4 / 3;

type Scope = "plot" | "area";

function delay(s: number): CSSProperties {
  return { animationDelay: `${s.toFixed(2)}s` };
}

function centroidOf(xys: XY[]): XY {
  const n = Math.max(1, xys.length);
  return { x: xys.reduce((s, p) => s + p.x, 0) / n, y: xys.reduce((s, p) => s + p.y, 0) / n };
}

/**
 * The plot drawn as the app's own animated picture, north up and to
 * scale: the boundary draws itself in, the surroundings from the map fade
 * in around it (water flows, buildings rise when tilted), the crop zones
 * grow through the season, and a laid-out farm (pond, shed, path, gate,
 * border trees) appears piece by piece, with lines to the nearest road,
 * water and houses.
 */
export function LandView({
  corners,
  survey,
  design,
  zones = [],
  crops = [],
  className,
}: {
  corners: Point[];
  survey?: Survey;
  design?: Design;
  zones?: Zone[];
  crops?: Crop[];
  className?: string;
}) {
  const [scope, setScope] = useState<Scope>("plot");
  const [tilt, setTilt] = useState(false);
  const [run, setRun] = useState(0);
  const [picked, setPicked] = useState("");
  const span = useMemo(() => planSpan(zones, crops), [zones, crops]);
  const [t, setT] = useState<number>();
  const [playing, setPlaying] = useState(false);
  const [today] = useState(() => Date.now());
  const now = t ?? (span ? Math.min(Math.max(today, span.from), span.to) : today);

  // The season player: from the first sowing to the last harvest in ten seconds.
  const frame = useRef(0);
  useEffect(() => {
    if (!playing || !span) return;
    let last = performance.now();
    const step = (at: number) => {
      const dt = at - last;
      last = at;
      setT((cur) => {
        const next = (cur ?? span.from) + ((span.to - span.from) * dt) / 10_000;
        if (next >= span.to) {
          setPlaying(false);
          return span.to;
        }
        return next;
      });
      frame.current = requestAnimationFrame(step);
    };
    frame.current = requestAnimationFrame(step);
    return () => cancelAnimationFrame(frame.current);
  }, [playing, span]);

  const origin = survey?.shape.centroid ?? corners[0] ?? { lat: 0, lng: 0 };
  const xy = useMemo(() => projector(origin), [origin.lat, origin.lng]); // eslint-disable-line react-hooks/exhaustive-deps
  const plot = corners.map(xy);
  const elements = design?.elements ?? [];

  const view = useMemo(() => {
    const own = [...plot, ...elements.flatMap((e) => (e.ring ?? []).map(xy))];
    const b = bounds(own);
    if (scope === "plot") return fitAspect(bounds(own, Math.max(15, 0.3 * Math.max(b.w, b.h))), aspect);
    const near = (survey?.nearest ?? []).map((n) => xy(n.at));
    const r = Math.max(b.w, b.h);
    return fitAspect(bounds([...own, ...near], Math.max(25, r)), aspect);
  }, [scope, plot.length, survey, design, xy]); // eslint-disable-line react-hooks/exhaustive-deps

  const fs = view.w / 48; // label size in metres
  const px = view.w / 800; // about a screen pixel, in metres
  const features = useMemo(
    () => [...(survey?.features ?? [])].sort((a, b) => looks[a.kind].z - looks[b.kind].z),
    [survey],
  );
  const kinds = [...new Set(features.map((f) => f.kind))];
  const callouts = (survey?.nearest ?? []).filter((n) => !n.inside).slice(0, 7);
  const scale = scaleLength(view.w);

  const terrain = survey?.terrain;
  const high = terrain?.points[terrain.highest];
  const low = terrain?.points[terrain.lowest];
  const fall =
    terrain && !terrain.flat && high && low
      ? {
          from: xy(high),
          to: xy(low),
          text: `Falls ${terrain.fall_m.toFixed(1)} m${terrain.downhill ? ` towards the ${terrain.downhill}` : ""}`,
        }
      : undefined;

  return (
    <div className={className}>
      <div className="flex flex-wrap items-center gap-2">
        <div className="flex rounded-md border p-0.5" role="group" aria-label="How much to show">
          <Button
            variant={scope === "plot" ? "secondary" : "ghost"}
            size="sm"
            aria-pressed={scope === "plot"}
            onClick={() => setScope("plot")}
          >
            <Scan /> Plot
          </Button>
          <Button
            variant={scope === "area" ? "secondary" : "ghost"}
            size="sm"
            aria-pressed={scope === "area"}
            onClick={() => setScope("area")}
          >
            <MapIcon /> Around it
          </Button>
        </div>
        <Button variant="outline" size="sm" aria-pressed={tilt} onClick={() => setTilt((v) => !v)}>
          <Box /> {tilt ? "Flat" : "Tilt"}
        </Button>
        <Button variant="ghost" size="sm" onClick={() => setRun((r) => r + 1)} aria-label="Play the drawing again">
          <RotateCcw /> Replay
        </Button>
        {span && (
          <div className="flex min-w-60 flex-1 items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                if (!playing && (t === undefined || t >= span.to)) setT(span.from);
                setPlaying((p) => !p);
              }}
              aria-label={playing ? "Pause the season" : "Play the season"}
            >
              {playing ? <Pause /> : <Play />} Season
            </Button>
            <input
              type="range"
              aria-label="Day of the season"
              className="flex-1 accent-green-700"
              min={span.from}
              max={span.to}
              step={86_400_000}
              value={now}
              onChange={(e) => {
                setPlaying(false);
                setT(Number(e.target.value));
              }}
            />
            <span className="w-24 text-right text-xs text-muted-foreground tabular-nums">
              {formatDate(new Date(now).toISOString().slice(0, 10))}
            </span>
          </div>
        )}
      </div>

      <div className="lv-stage relative mt-2 aspect-[4/3] w-full overflow-hidden rounded-lg border bg-[#eef3e2]">
        <svg
          key={run}
          role="img"
          aria-label="Picture of the plot and what is around it"
          className="lv size-full"
          data-tilt={tilt}
          viewBox={`${view.x} ${view.y} ${view.w} ${view.h}`}
          preserveAspectRatio="xMidYMid meet"
          onClick={() => setPicked("")}
        >
          <defs>
            <clipPath id="lv-plot">
              <path d={pathOf(plot, true)} />
            </clipPath>
            {zones.map((z, i) => {
              const g = growth(now, season(z, crops));
              const spacing = Math.max((z.row_cm ?? 45) / 100, view.w / 140);
              return (
                <pattern
                  key={i}
                  id={`lv-rows-${i}`}
                  patternUnits="userSpaceOnUse"
                  width={spacing}
                  height={spacing}
                  patternTransform={`rotate(${z.row_bearing - 90})`}
                >
                  <rect width={spacing} height={spacing} fill={g > 0 ? "#9b7653" : "#b08d68"} />
                  {g > 0 && (
                    <line
                      x1={0}
                      x2={spacing}
                      y1={spacing / 2}
                      y2={spacing / 2}
                      stroke={cropColor(g)}
                      strokeWidth={spacing * (0.2 + 0.6 * g)}
                      strokeLinecap="round"
                    />
                  )}
                </pattern>
              );
            })}
          </defs>

          {/* The land around: fields, villages, water, roads, buildings. */}
          <g className="lv-fade" style={delay(0.4)}>
            {features.map((f) => (
              <FeatureShape
                key={f.id}
                f={f}
                xy={xy}
                px={px}
                tilt={tilt}
                picked={picked === f.id}
                onPick={() => setPicked(f.id)}
              />
            ))}
          </g>

          {/* The plot: bare earth, then the boundary drawing itself in. */}
          <path d={pathOf(plot, true)} className="lv-fade" fill="#c9a47c" style={delay(0.1)} />
          <g clipPath="url(#lv-plot)">
            {zones.map((z, i) => (
              <path
                key={i}
                className="lv-grow"
                style={delay(1 + i * 0.25)}
                d={pathOf(z.ring.map(xy), true)}
                fill={`url(#lv-rows-${i})`}
                stroke={colors[i % colors.length]}
                strokeWidth={2 * px}
              >
                <title>{cropLabel(crops, z.crop, z.with)}</title>
              </path>
            ))}
          </g>

          {elements.map((e, i) => {
            const look = elementLooks[e.kind];
            const at = 1.6 + i * 0.35;
            const title = <title>{[look.label, e.why].filter(Boolean).join(": ")}</title>;
            if (e.kind === "gate" && e.at) {
              const p = xy(e.at);
              return (
                <g key={i} className="lv-pop" style={delay(at)}>
                  {title}
                  <rect
                    x={p.x - 2}
                    y={p.y - 2}
                    width={4}
                    height={4}
                    fill={look.fill}
                    stroke={look.stroke}
                    strokeWidth={1.5 * px}
                  />
                  <text x={p.x} y={p.y - 3} fontSize={fs * 0.8} textAnchor="middle" className="lv-label">
                    Gate
                  </text>
                </g>
              );
            }
            const ring = (e.ring ?? []).map(xy);
            if (ring.length < 3) return null;
            if (e.kind === "border") {
              const hole = (e.hole ?? []).map(xy);
              const trees = treesAlong(ring, hole, e.tree_spacing_m ?? 4, Math.min(e.trees ?? 400, 400));
              const r = Math.max(0.8, (e.tree_spacing_m ?? 4) * 0.38);
              return (
                <g key={i}>
                  <path
                    className="lv-fade"
                    style={delay(at)}
                    d={pathOf(ring, true) + pathOf(hole, true)}
                    fillRule="evenodd"
                    fill={look.fill}
                    fillOpacity={0.55}
                  >
                    {title}
                  </path>
                  {trees.map((p, k) => (
                    <circle
                      key={k}
                      className="lv-pop"
                      style={delay(at + (1.4 * k) / Math.max(1, trees.length))}
                      cx={p.x}
                      cy={p.y}
                      r={r}
                      fill="#4d7c0f"
                      stroke="#365314"
                      strokeWidth={0.6 * px}
                    />
                  ))}
                </g>
              );
            }
            const c = centroidOf(ring);
            if (e.kind === "pond") {
              const side = Math.sqrt(Math.abs(ringArea(ring)));
              return (
                <g key={i}>
                  <path
                    className="lv-grow"
                    style={delay(at)}
                    d={pathOf(ring, true)}
                    fill={look.fill}
                    stroke={look.stroke}
                    strokeWidth={1.5 * px}
                  >
                    {title}
                  </path>
                  <g clipPath="url(#lv-plot)">
                    {[0, 1.3].map((d) => (
                      <circle
                        key={d}
                        className="lv-ripple"
                        style={delay(at + 0.8 + d)}
                        cx={c.x}
                        cy={c.y}
                        r={side * 0.35}
                        fill="none"
                        stroke="#eff6ff"
                        strokeWidth={1.2 * px}
                      />
                    ))}
                  </g>
                  <text x={c.x} y={c.y + fs * 0.3} fontSize={fs * 0.75} textAnchor="middle" className="lv-label">
                    Pond
                  </text>
                </g>
              );
            }
            if (e.kind === "shed") {
              const lift = tilt ? 3 : 0;
              return (
                <g key={i} className="lv-pop" style={delay(at)}>
                  {title}
                  {tilt && <path d={pathOf(ring, true)} fill="#78716c" />}
                  <path
                    className="lv-rise"
                    style={{ transform: `translateY(${-lift}px)` }}
                    d={pathOf(ring, true)}
                    fill={look.fill}
                    stroke={look.stroke}
                    strokeWidth={1.2 * px}
                  />
                </g>
              );
            }
            return (
              <path
                key={i}
                className="lv-fade"
                style={delay(at)}
                d={pathOf(ring, true)}
                fill={look.fill}
                stroke={look.stroke}
                strokeWidth={0.8 * px}
              >
                {title}
              </path>
            );
          })}

          <path
            d={pathOf(plot, true)}
            className="lv-draw"
            pathLength={1}
            fill="none"
            stroke="#3f6212"
            strokeWidth={3 * px}
            strokeLinejoin="round"
          />
          {plot.map((p, i) => (
            <circle
              key={i}
              className="lv-pop"
              style={delay(0.2 + (0.9 * i) / plot.length)}
              cx={p.x}
              cy={p.y}
              r={4 * px}
              fill="#4d7c0f"
              stroke="white"
              strokeWidth={1.2 * px}
            />
          ))}

          {fall && (
            <g className="lv-fade" style={delay(2.2)}>
              <defs>
                <marker
                  id="lv-arrow"
                  viewBox="0 0 10 10"
                  refX="8"
                  refY="5"
                  markerWidth="5"
                  markerHeight="5"
                  orient="auto"
                >
                  <path d="M0 0L10 5L0 10z" fill="#1e3a8a" />
                </marker>
              </defs>
              <line
                x1={fall.from.x}
                y1={fall.from.y}
                x2={fall.to.x}
                y2={fall.to.y}
                stroke="#1e3a8a"
                strokeWidth={2 * px}
                strokeDasharray={`${6 * px} ${4 * px}`}
                markerEnd="url(#lv-arrow)"
              >
                <title>{fall.text}</title>
              </line>
            </g>
          )}

          {callouts.map((n, i) => (
            <Callout key={n.id + i} n={n} xy={xy} view={view} fs={fs} px={px} at={2 + i * 0.2} flip={i % 2 === 1} />
          ))}
        </svg>

        <Compass />
        {!tilt && (
          <div className="pointer-events-none absolute inset-x-2 bottom-2 flex flex-col text-[10px] text-stone-700">
            <div
              className="h-1.5 border-x-2 border-b-2 border-stone-700"
              style={{ width: `${(scale / view.w) * 100}%` }}
            />
            <span>{metres(scale)}</span>
          </div>
        )}
        {survey && (
          <span className="pointer-events-none absolute right-2 bottom-1 text-[10px] text-stone-600">
            Map data © OpenStreetMap contributors
          </span>
        )}
      </div>

      <ul className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground" aria-label="Legend">
        <li className="flex items-center gap-1">
          <Swatch color="#c9a47c" border="#3f6212" /> Your plot
        </li>
        {zones.map((z, i) => (
          <li key={`z${i}`} className="flex items-center gap-1">
            <Swatch color={cropColor(growth(now, season(z, crops)))} border={colors[i % colors.length] ?? "#16a34a"} />
            {cropLabel(crops, z.crop, z.with)}
          </li>
        ))}
        {elements.map((e, i) => (
          <li key={`e${i}`} className="flex items-center gap-1">
            <Swatch color={elementLooks[e.kind].fill} border={elementLooks[e.kind].stroke} />{" "}
            {elementLooks[e.kind].label}
          </li>
        ))}
        {kinds.map((k) => (
          <li key={k} className="flex items-center gap-1">
            <Swatch color={looks[k].fill ?? looks[k].stroke} border={looks[k].mark ?? looks[k].stroke} />{" "}
            {looks[k].label}
          </li>
        ))}
      </ul>
      {picked && <Picked f={features.find((f) => f.id === picked)} survey={survey} />}
    </div>
  );
}

function Swatch({ color, border }: { color: string; border: string }) {
  return (
    <span
      className="inline-block size-3 rounded-sm border"
      style={{ background: color, borderColor: border }}
      aria-hidden="true"
    />
  );
}

function Compass() {
  return (
    <svg className="pointer-events-none absolute top-2 right-2 size-9" viewBox="-20 -20 40 40" aria-label="North is up">
      <circle r="18" fill="white" fillOpacity="0.8" stroke="#78716c" strokeWidth="1" />
      <path d="M0 -14L5 2L0 -1L-5 2Z" fill="#b91c1c" />
      <path d="M0 14L5 2L0 -1L-5 2Z" fill="#a8a29e" />
      <text x="9" y="-8" fontSize="8" fontWeight="700" fill="#1c1917">
        N
      </text>
    </svg>
  );
}

function FeatureShape({
  f,
  xy,
  px,
  tilt,
  picked,
  onPick,
}: {
  f: Feature;
  xy: (p: Point) => XY;
  px: number;
  tilt: boolean;
  picked: boolean;
  onPick: () => void;
}) {
  const look = looks[f.kind];
  const pts = f.points.map(xy);
  const name = [look.label, f.name || f.ref].filter(Boolean).join(": ");
  const pick = (e: React.MouseEvent) => {
    e.stopPropagation();
    onPick();
  };
  const outline = picked ? "#f59e0b" : look.stroke;
  const first = pts[0];
  if (!first) return null;
  if (pts.length === 1 || f.kind === "well") {
    const p = first;
    return (
      <circle
        cx={p.x}
        cy={p.y}
        r={Math.max(2, 4 * px)}
        fill={look.fill}
        stroke={outline}
        strokeWidth={px}
        onClick={pick}
      >
        <title>{name}</title>
      </circle>
    );
  }
  const closed = Boolean(f.closed && look.fill);
  const d = pathOf(pts, closed);
  if (closed) {
    const lift = tilt && look.rises ? (f.kind === "facility" ? 6 : 4) : 0;
    return (
      <g onClick={pick} className="cursor-pointer">
        <title>{name}</title>
        {lift > 0 && <path d={d} fill="#a8a29e" />}
        <path
          d={d}
          className={look.rises ? "lv-rise" : undefined}
          style={lift ? { transform: `translateY(${-lift}px)` } : undefined}
          fill={look.fill}
          stroke={outline}
          strokeWidth={(picked ? 3 : 1) * look.width * Math.max(1, px)}
          strokeDasharray={look.dash}
        />
        {f.kind === "water" && (
          <path
            d={d}
            className="lv-flow"
            fill="none"
            stroke="#e0f2fe"
            strokeWidth={1.2 * px}
            strokeDasharray={`${8 * px} ${10 * px}`}
          />
        )}
      </g>
    );
  }
  const width = f.kind === "road" ? roadWidth(f.class) : look.width;
  return (
    <g onClick={pick} className="cursor-pointer">
      <title>{name}</title>
      {f.kind === "road" && (
        <path
          d={d}
          fill="none"
          stroke="#a8a29e"
          strokeWidth={width + 2 * px}
          strokeLinecap="round"
          strokeLinejoin="round"
        />
      )}
      <path
        d={d}
        fill="none"
        stroke={picked ? "#f59e0b" : look.stroke}
        strokeWidth={Math.max(width, 1.5 * px)}
        strokeDasharray={look.dash}
        strokeLinecap="round"
        strokeLinejoin="round"
      />
      {look.flows && (
        <path
          d={d}
          className="lv-flow"
          fill="none"
          stroke="#e0f2fe"
          strokeWidth={Math.max(0.4, width / 4)}
          strokeDasharray={`${Math.max(2, width * 1.5)} ${Math.max(4, width * 3)}`}
          strokeLinecap="round"
        />
      )}
    </g>
  );
}

function Callout({
  n,
  xy,
  view,
  fs,
  px,
  at,
  flip,
}: {
  n: Nearby;
  xy: (p: Point) => XY;
  view: { x: number; y: number; w: number; h: number };
  fs: number;
  px: number;
  at: number;
  flip: boolean;
}) {
  const inset = fs * 1.5;
  const inner = { x: view.x + inset, y: view.y + inset, w: view.w - 2 * inset, h: view.h - 2 * inset };
  const from = xy(n.from);
  const to = xy(n.at);
  const seg = clipSegment(from, to, inner);
  if (!seg || n.distance_m < 1) return null;
  const [a, b] = seg;
  const reached = Math.hypot(b.x - to.x, b.y - to.y) < 0.5;
  const label = nearbyLabel(n);
  // Keep the label inside the picture: anchored on the side towards the
  // middle, above or below the line's end.
  const fx = (b.x - view.x) / view.w;
  const fy = (b.y - view.y) / view.h;
  const anchor = fx > 0.62 ? "end" : fx < 0.38 ? "start" : "middle";
  const dx = anchor === "end" ? -fs * 0.4 : anchor === "start" ? fs * 0.4 : 0;
  const below = fy < 0.2 || (flip && fy < 0.8);
  return (
    <g className="lv-fade" style={delay(at)}>
      <line
        className="lv-draw"
        style={delay(at)}
        pathLength={1}
        x1={a.x}
        y1={a.y}
        x2={b.x}
        y2={b.y}
        stroke="#1c1917"
        strokeOpacity={0.7}
        strokeWidth={1.2 * px}
      />
      {reached && <circle cx={b.x} cy={b.y} r={3 * px} fill="#1c1917" />}
      <text
        x={b.x + dx}
        y={b.y + (below ? fs * 1.1 : -fs * 0.5)}
        fontSize={fs * 0.85}
        textAnchor={anchor}
        className="lv-label"
      >
        {label}
      </text>
    </g>
  );
}

function Picked({ f, survey }: { f?: Feature; survey?: Survey }) {
  if (!f) return null;
  const n = survey?.nearby.find((x) => x.id === f.id);
  const kind: FeatureKind = f.kind;
  return (
    <p className="mt-2 rounded-md border bg-muted/40 px-3 py-2 text-sm" role="status">
      <span className="font-medium">{f.name || f.ref || looks[kind].label}</span>
      {f.class && <span className="text-muted-foreground"> ({f.class.replaceAll("_", " ")})</span>}
      {n ? ` · ${nearbyLabel(n)}` : ""}
    </p>
  );
}
