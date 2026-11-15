import L from "leaflet";
import "leaflet/dist/leaflet.css";
import "./draw.css";
import { useEffect, useRef } from "react";
import type { Corner, Point } from "./types";

const satellite = () =>
  L.tileLayer("https://server.arcgisonline.com/ArcGIS/rest/services/World_Imagery/MapServer/tile/{z}/{y}/{x}", {
    // Rural India has imagery to zoom 18; closer, stretch it rather than
    // show "Map data not yet available".
    maxNativeZoom: 18,
    maxZoom: 20,
    attribution: "Imagery © Esri, Maxar, Earthstar Geographics",
  });
const streets = () =>
  L.tileLayer("https://tile.openstreetmap.org/{z}/{x}/{y}.png", {
    maxZoom: 19,
    attribution: "© OpenStreetMap contributors",
  });

// India, until the farmer has a plot or a GPS fix.
const start: L.LatLngExpression = [20.6, 78.9];

/** The zoom from which a tap adds a corner; below it a tap zooms in. */
export const cornerZoom = 15;

export interface Outline {
  id: string;
  name: string;
  corners: Point[];
}

/** A zone of a crop plan, drawn filled in its colour with a label (none when empty). */
export interface ZoneShape {
  ring: Point[];
  /** A hole in it: a border band is the ring less its hole. */
  hole?: Point[];
  label: string;
  color: string;
}

/**
 * A stroke drawn on the map with one finger or the mouse: free (a pencil
 * line), segment (a straight line from where it starts to where it ends) or
 * box (a rectangle between the two).
 */
export type Stroke = "free" | "segment" | "box";

/** A handle on the map the farmer drags, such as a rectangle's turn handle. */
export interface Handle {
  id: string;
  at: Point;
  /** What it does, for its tooltip and screen readers. */
  label: string;
  /** The character shown on it. */
  glyph: string;
}

/**
 * The map: satellite or streets, the plot being edited as a polygon with
 * numbered corners, and the farmer's other plots in outline. When editable,
 * a tap adds a corner and a corner can be dragged. With a stroke, a
 * one-finger (or mouse) drag draws instead of moving the map, from the
 * zoom where taps add corners; two fingers, the wheel and the buttons
 * still move and zoom it, and so does a drag when zoomed out.
 */
export function FarmMap({
  corners,
  onChange,
  editable = false,
  flagged = [],
  others = [],
  zones = [],
  fit = 0,
  you,
  goto,
  taps = true,
  stroke,
  onStroke,
  handles = [],
  onHandle,
  className,
}: {
  corners: Corner[];
  onChange?: (c: Corner[]) => void;
  editable?: boolean;
  /** Corners a check names, by index: drawn red. */
  flagged?: number[];
  others?: Outline[];
  zones?: ZoneShape[];
  /** Change it to fit the map to the corners again. */
  fit?: number;
  /** The farmer's position, if known. */
  you?: Point & { accuracy?: number };
  /** A place to fly to, such as the village the farmer searched for: a new object each time. */
  goto?: Point;
  /** Whether a tap adds a corner (when editable). */
  taps?: boolean;
  /** What a drag draws, if anything; taps then add no corners. */
  stroke?: Stroke;
  /** Called with the points of a finished stroke (two, its ends, for a segment or a box). */
  onStroke?: (pts: Point[]) => void;
  handles?: Handle[];
  /** Called as a handle is dragged, with where it is. */
  onHandle?: (id: string, at: Point) => void;
  className?: string;
}) {
  const el = useRef<HTMLDivElement>(null);
  const map = useRef<L.Map | null>(null);
  const layer = useRef<L.LayerGroup | null>(null);
  const otherLayer = useRef<L.LayerGroup | null>(null);
  const youLayer = useRef<L.LayerGroup | null>(null);
  const zoneLayer = useRef<L.LayerGroup | null>(null);
  const handleLayer = useRef<L.LayerGroup | null>(null);
  const handleMarks = useRef(new Map<string, L.Marker>());
  // The handles being dragged.
  const held = useRef(new Set<string>());
  // The handlers read the latest props without rebinding.
  const latest = useRef({ corners, onChange, editable, taps, stroke, onStroke, onHandle });
  useEffect(() => {
    latest.current = { corners, onChange, editable, taps, stroke, onStroke, onHandle };
  });

  useEffect(() => {
    if (!el.current) return;
    // No double-tap zoom where a tap adds a corner: each tap of it would add one.
    const m = L.map(el.current, { center: start, zoom: 5, zoomControl: true, doubleClickZoom: !onChange });
    const sat = satellite().addTo(m);
    L.control.layers({ Satellite: sat, Streets: streets() }, undefined, { position: "topright" }).addTo(m);
    otherLayer.current = L.layerGroup().addTo(m);
    zoneLayer.current = L.layerGroup().addTo(m);
    layer.current = L.layerGroup().addTo(m);
    youLayer.current = L.layerGroup().addTo(m);
    handleLayer.current = L.layerGroup().addTo(m);
    const strokeLayer = L.layerGroup().addTo(m);
    m.on("click", (e: L.LeafletMouseEvent) => {
      const { corners, onChange, editable, taps, stroke } = latest.current;
      if (!editable || !onChange) return;
      // Zoomed out, a tap lands far from where the farmer meant: it zooms
      // in there instead of adding a corner (or drawing).
      if (m.getZoom() < cornerZoom) {
        m.setView(e.latlng, Math.min(m.getZoom() + 4, cornerZoom));
        return;
      }
      if (taps && !stroke) onChange([...corners, { lat: round(e.latlng.lat), lng: round(e.latlng.lng) }]);
    });
    // Strokes: the listeners run in the capture phase, before Leaflet's,
    // and keep a one-pointer drag from panning the map while it draws.
    const box = m.getContainer();
    let drawing: { id: number; pts: L.LatLng[]; line: L.Polyline } | undefined;
    const end = (keep: boolean) => {
      if (!drawing) return;
      const { pts } = drawing;
      drawing = undefined;
      strokeLayer.clearLayers();
      const kind = latest.current.stroke;
      const first = pts[0];
      const last = pts[pts.length - 1];
      if (!keep || !kind || !first || !last) return;
      const out = kind === "free" ? pts : [first, last];
      latest.current.onStroke?.(out.map((p) => ({ lat: round(p.lat), lng: round(p.lng) })));
    };
    const shape = (pts: L.LatLng[]): L.LatLng[] => {
      const first = pts[0];
      const last = pts[pts.length - 1];
      if (!first || !last || latest.current.stroke === "free") return pts;
      if (latest.current.stroke === "segment") return [first, last];
      return [first, L.latLng(first.lat, last.lng), last, L.latLng(last.lat, first.lng), first];
    };
    const down = (e: PointerEvent) => {
      if (drawing && !e.isPrimary) {
        // A second finger: the farmer is moving or zooming the map.
        end(false);
        return;
      }
      const t = e.target as Element;
      if (
        !latest.current.stroke ||
        !e.isPrimary ||
        e.button > 0 ||
        m.getZoom() < cornerZoom ||
        t.closest(".leaflet-marker-icon, .leaflet-control")
      )
        return;
      // Not a pan, and no text selected as the mouse drags.
      e.stopPropagation();
      e.preventDefault();
      const p = m.mouseEventToLatLng(e);
      drawing = {
        id: e.pointerId,
        pts: [p],
        line: L.polyline([p], { color: "#facc15", weight: 3, dashArray: "6 6" }).addTo(strokeLayer),
      };
      box.setPointerCapture?.(e.pointerId);
    };
    const move = (e: PointerEvent) => {
      if (!drawing || e.pointerId !== drawing.id) return;
      e.stopPropagation();
      drawing.pts.push(m.mouseEventToLatLng(e));
      drawing.line.setLatLngs(shape(drawing.pts));
    };
    const up = (e: PointerEvent) => {
      if (drawing && e.pointerId === drawing.id) end(e.type === "pointerup");
    };
    // Leaflet starts a pan on mousedown or touchstart: not while drawing.
    const hold = (e: Event) => {
      if (drawing && !((e as TouchEvent).touches?.length > 1)) {
        e.stopPropagation();
        if (e.type === "mousedown") e.preventDefault();
      }
    };
    box.addEventListener("pointerdown", down, true);
    box.addEventListener("pointermove", move, true);
    box.addEventListener("pointerup", up, true);
    box.addEventListener("pointercancel", up, true);
    box.addEventListener("mousedown", hold, true);
    box.addEventListener("touchstart", hold, true);
    map.current = m;
    return () => {
      m.remove();
      map.current = null;
    };
    // Once: whether the map adds corners does not change for its life.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // The plot being edited.
  useEffect(() => {
    const g = layer.current;
    if (!g) return;
    g.clearLayers();
    const ll = corners.map((c) => L.latLng(c.lat, c.lng));
    if (ll.length >= 3)
      L.polygon(ll, { color: "#facc15", weight: 3, fillOpacity: zones.length > 0 ? 0 : 0.15 }).addTo(g);
    else if (ll.length === 2) L.polyline(ll, { color: "#facc15", weight: 3 }).addTo(g);
    corners.forEach((c, i) => {
      const icon = L.divIcon({
        className: "",
        html: `<div class="corner-marker" data-flagged="${flagged.includes(i)}">${i + 1}</div>`,
        iconSize: [22, 22],
        iconAnchor: [11, 11],
      });
      const mk = L.marker([c.lat, c.lng], {
        icon,
        draggable: editable,
        keyboard: false,
        title: `Corner ${i + 1}`,
      }).addTo(g);
      mk.on("dragend", () => {
        const p = mk.getLatLng();
        const { corners, onChange } = latest.current;
        onChange?.(corners.map((x, j) => (j === i ? { lat: round(p.lat), lng: round(p.lng) } : x)));
      });
      if (c.accuracy_m)
        L.circle([c.lat, c.lng], { radius: c.accuracy_m, weight: 1, color: "#facc15", fillOpacity: 0.05 }).addTo(g);
    });
  }, [corners, flagged, editable, zones.length]);

  useEffect(() => {
    const g = otherLayer.current;
    if (!g) return;
    g.clearLayers();
    for (const o of others) {
      if (o.corners.length < 3) continue;
      L.polygon(
        o.corners.map((c) => L.latLng(c.lat, c.lng)),
        { color: "#ffffff", weight: 2, dashArray: "4 4", fillOpacity: 0.05 },
      )
        .bindTooltip(o.name)
        .addTo(g);
    }
  }, [others]);

  useEffect(() => {
    const g = zoneLayer.current;
    if (!g) return;
    g.clearLayers();
    for (const z of zones) {
      if (z.ring.length < 3) continue;
      const outer = z.ring.map((c) => L.latLng(c.lat, c.lng));
      const rings = z.hole && z.hole.length >= 3 ? [outer, z.hole.map((c) => L.latLng(c.lat, c.lng))] : outer;
      const shape = L.polygon(rings, { color: z.color, weight: 2, fillColor: z.color, fillOpacity: 0.45 });
      if (z.label) shape.bindTooltip(z.label, { permanent: true, direction: "center", className: "zone-label" });
      shape.addTo(g);
    }
  }, [zones]);

  // Handles move in place, so the one being dragged keeps its drag.
  useEffect(() => {
    const g = handleLayer.current;
    if (!g) return;
    const marks = handleMarks.current;
    for (const [id, mk] of marks) {
      if (!handles.some((h) => h.id === id)) {
        g.removeLayer(mk);
        marks.delete(id);
      }
    }
    for (const h of handles) {
      const mk = marks.get(h.id);
      if (mk) {
        if (!held.current.has(h.id)) mk.setLatLng([h.at.lat, h.at.lng]);
        continue;
      }
      const made = L.marker([h.at.lat, h.at.lng], {
        icon: L.divIcon({
          className: "",
          html: `<div class="draw-handle">${h.glyph}</div>`,
          iconSize: [30, 30],
          iconAnchor: [15, 15],
        }),
        draggable: true,
        keyboard: false,
        title: h.label,
        alt: h.label,
      }).addTo(g);
      made.on("dragstart", () => held.current.add(h.id));
      made.on("dragend", () => held.current.delete(h.id));
      made.on("drag", () => {
        const p = made.getLatLng();
        latest.current.onHandle?.(h.id, { lat: round(p.lat), lng: round(p.lng) });
      });
      marks.set(h.id, made);
    }
  }, [handles]);

  useEffect(() => {
    const g = youLayer.current;
    if (!g) return;
    g.clearLayers();
    if (!you) return;
    if (you.accuracy)
      L.circle([you.lat, you.lng], { radius: you.accuracy, weight: 1, color: "#38bdf8", fillOpacity: 0.1 }).addTo(g);
    L.circleMarker([you.lat, you.lng], {
      radius: 7,
      color: "#fff",
      weight: 2,
      fillColor: "#0284c7",
      fillOpacity: 1,
    }).addTo(g);
  }, [you]);

  // Fit to the plot (or the other plots) when asked.
  useEffect(() => {
    const m = map.current;
    if (!m) return;
    const pts = corners.length > 0 ? corners : others.flatMap((o) => o.corners);
    const [one] = pts;
    if (pts.length === 1 && one) m.setView([one.lat, one.lng], 18);
    else if (pts.length > 1)
      m.fitBounds(L.latLngBounds(pts.map((c) => [c.lat, c.lng])), { padding: [30, 30], maxZoom: 19 });
    // Only on request: not while the farmer is tapping corners.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [fit]);

  useEffect(() => {
    if (goto && map.current) map.current.setView([goto.lat, goto.lng], 16);
  }, [goto]);

  useEffect(() => {
    if (you && map.current && corners.length === 0) map.current.setView([you.lat, you.lng], 18);
    // Centre on the first fix only.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [you === undefined]);

  return (
    <div
      ref={el}
      className={className}
      role="application"
      aria-label="Map of the plot"
      data-stroke={stroke ?? undefined}
    />
  );
}

/** Rounds a coordinate to 7 decimals (about 1 cm). */
function round(x: number): number {
  return Math.round(x * 1e7) / 1e7;
}
