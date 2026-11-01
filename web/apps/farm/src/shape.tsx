import { bounds, fitAspect, pathOf, projector } from "./landview";
import type { Farm } from "./types";

/** A small outline of plots to scale, for cards and lists. */
export function PlotShape({
  farms,
  className,
  color = "#4d7c0f",
}: {
  farms: Farm[];
  className?: string;
  color?: string;
}) {
  const all = farms.flatMap((f) => f.boundary.corners);
  const origin = all[0] ?? { lat: 0, lng: 0 };
  const xy = projector(origin);
  const b = bounds(all.map(xy));
  const view = fitAspect(bounds(all.map(xy), Math.max(2, 0.08 * Math.max(b.w, b.h))), 4 / 3);
  return (
    <svg viewBox={`${view.x} ${view.y} ${view.w} ${view.h}`} className={className} aria-hidden="true">
      {farms.map((f) => (
        <path
          key={f.id}
          d={pathOf(f.boundary.corners.map(xy), true)}
          fill={color}
          fillOpacity={0.22}
          stroke={color}
          strokeWidth={view.w / 60}
          strokeLinejoin="round"
        />
      ))}
    </svg>
  );
}
