import { useId, useMemo } from "react";
import { plantColor } from "./distribution";
import { bounds, fitAspect, metres, pathOf, projector, scaleLength, type XY } from "./landview";
import type { Distribution, DistributionResult, Farm, Point } from "./types";

function delay(s: number) {
  return { animationDelay: `${s.toFixed(2)}s` };
}

/**
 * A group's plots drawn to scale, north up: each plot's outline and name,
 * and, with a laid-out distribution, every piece in its plant group's
 * colour with its rows hatched the way they run, border bands round the
 * edges and trees as crowns. The midday sun's side is marked, since that
 * is where shade comes from.
 */
export function GroupPicture({
  farms,
  result,
  distribution,
  highlight,
  className,
  aspect = 16 / 10,
}: {
  farms: Farm[];
  result?: DistributionResult;
  distribution?: Distribution;
  /** A plant group to pick out: the others fade. */
  highlight?: string;
  className?: string;
  aspect?: number;
}) {
  const uid = useId().replace(/:/g, "");
  const all = farms.flatMap((f) => f.boundary.corners);
  const origin = all[0] ?? { lat: 0, lng: 0 };
  const xy = useMemo(() => projector(origin), [origin.lat, origin.lng]); // eslint-disable-line react-hooks/exhaustive-deps
  const view = useMemo(() => {
    const b = bounds(all.map(xy));
    return fitAspect(bounds(all.map(xy), Math.max(12, 0.08 * Math.max(b.w, b.h))), aspect);
  }, [farms, xy, aspect]); // eslint-disable-line react-hooks/exhaustive-deps
  const fs = view.w / 50;
  const px = view.w / 900;
  const scale = scaleLength(view.w);
  const south = origin.lat >= 0;
  const d = distribution ?? result?.distribution;
  const bearings = [...new Set((result?.plots ?? []).map((p) => Math.round(p.row_bearing)))];
  const ring = (ps: Point[]) => pathOf(ps.map(xy), true);
  const label = (f: Farm): XY => {
    const ps = f.boundary.corners.map(xy);
    return { x: ps.reduce((s, p) => s + p.x, 0) / ps.length, y: ps.reduce((s, p) => s + p.y, 0) / ps.length };
  };

  return (
    <svg
      viewBox={`${view.x} ${view.y} ${view.w} ${view.h}`}
      className={className}
      role="img"
      aria-label={`The ${farms.length === 1 ? "plot" : `${farms.length} plots`}${result ? " with the plant groups laid out" : ""}, north up`}
      data-testid="group-picture"
    >
      <defs>
        {bearings.map((b) => (
          <pattern
            key={b}
            id={`${uid}-rows-${b}`}
            patternUnits="userSpaceOnUse"
            width={Math.max(1.2, view.w / 160)}
            height={Math.max(1.2, view.w / 160)}
            patternTransform={`rotate(${b})`}
          >
            <line
              x1="0"
              y1="0"
              x2="0"
              y2={Math.max(1.2, view.w / 160)}
              stroke="white"
              strokeOpacity="0.28"
              strokeWidth={px * 1.4}
            />
          </pattern>
        ))}
        <radialGradient id={`${uid}-crown`}>
          <stop offset="0%" stopColor="#bbf7d0" stopOpacity="0.95" />
          <stop offset="70%" stopColor="#166534" stopOpacity="0.95" />
          <stop offset="100%" stopColor="#14532d" stopOpacity="0.9" />
        </radialGradient>
      </defs>
      <rect x={view.x} y={view.y} width={view.w} height={view.h} className="fill-[#eef2e6] dark:fill-[#1b2117]" />
      {/* The plots' ground. */}
      {farms.map((f, i) => (
        <path
          key={f.id}
          d={ring(f.boundary.corners)}
          className="lv-fade fill-[#d9cfb4] dark:fill-[#3b3527]"
          style={delay(0.05 * i)}
        />
      ))}
      {/* The pieces. */}
      {result?.plots.map((p, pi) =>
        p.regions.map((r, ri) => {
          const color = plantColor(d, r.plant);
          const faded = highlight !== undefined && highlight !== r.plant;
          const shape = r.hole && r.hole.length >= 3 ? ring(r.ring) + ring(r.hole) : ring(r.ring);
          const t = 0.15 + 0.04 * (pi * 6 + ri);
          return (
            <g key={`${p.plot}-${ri}`} opacity={faded ? 0.25 : 1} data-plant={r.plant}>
              <path d={shape} fill={color} fillOpacity={0.82} fillRule="evenodd" className="lv-grow" style={delay(t)} />
              {!r.hole && !(r.trees && r.trees.length > 0) && (
                <path
                  d={shape}
                  fill={`url(#${uid}-rows-${Math.round(p.row_bearing)})`}
                  className="lv-grow"
                  style={delay(t)}
                />
              )}
              <path
                d={shape}
                fill="none"
                stroke="white"
                strokeOpacity={0.6}
                strokeWidth={px * 1.2}
                fillRule="evenodd"
              />
            </g>
          );
        }),
      )}
      {/* Trees as crowns. */}
      {result?.plots.map((p, pi) =>
        p.regions.map((r, ri) => {
          if (!r.trees || r.trees.length === 0) return null;
          const crown = Math.max(px * 3, Math.min(4, view.w / 120));
          const faded = highlight !== undefined && highlight !== r.plant;
          return (
            <g key={`t-${p.plot}-${ri}`} opacity={faded ? 0.2 : 1}>
              {r.trees.slice(0, 1500).map((tp, k) => {
                const q = xy(tp);
                return (
                  <circle
                    key={k}
                    cx={q.x}
                    cy={q.y}
                    r={crown}
                    fill={`url(#${uid}-crown)`}
                    className="lv-pop"
                    style={delay(0.5 + 0.03 * pi + Math.min(1.2, k * 0.004))}
                  />
                );
              })}
            </g>
          );
        }),
      )}
      {/* Outlines and names. */}
      {farms.map((f) => {
        const c = label(f);
        return (
          <g key={`o-${f.id}`}>
            <path
              d={ring(f.boundary.corners)}
              fill="none"
              strokeWidth={px * 2.5}
              className="lv-draw stroke-[#3f6212] dark:stroke-[#bef264]"
              pathLength={1}
            />
            <text x={c.x} y={c.y} fontSize={fs} textAnchor="middle" dominantBaseline="middle" className="lv-label">
              {f.name}
            </text>
          </g>
        );
      })}
      {/* North, the midday sun's side, and the scale. */}
      <g transform={`translate(${view.x + view.w - fs * 2.2} ${view.y + fs * 2.4})`} aria-hidden="true">
        <circle r={fs * 1.3} className="fill-white/85 dark:fill-black/50" />
        <path
          d={`M0 ${-fs} L${fs * 0.45} ${fs * 0.5} L0 ${fs * 0.2} L${-fs * 0.45} ${fs * 0.5}Z`}
          className="fill-[#1c1917] dark:fill-white"
        />
        <text y={fs * 2.6} fontSize={fs * 0.8} textAnchor="middle" className="lv-label">
          N
        </text>
      </g>
      <g
        transform={`translate(${view.x + view.w / 2} ${south ? view.y + view.h - fs * 1.1 : view.y + fs * 1.1})`}
        aria-hidden="true"
      >
        <circle r={fs * 0.55} fill="#f59e0b" />
        <text x={fs * 0.9} y={fs * 0.05} fontSize={fs * 0.75} dominantBaseline="middle" className="lv-label">
          midday sun
        </text>
      </g>
      <g transform={`translate(${view.x + fs} ${view.y + view.h - fs * 1.2})`} aria-label={`Scale: ${metres(scale)}`}>
        <rect width={scale} height={fs * 0.28} className="fill-[#1c1917] dark:fill-white" />
        <text y={-fs * 0.4} fontSize={fs * 0.75} className="lv-label">
          {metres(scale)}
        </text>
      </g>
    </svg>
  );
}
