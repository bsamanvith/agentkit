import L from "leaflet";
import "leaflet/dist/leaflet.css";
import { useEffect, useRef } from "react";
import type { Corner, Point } from "./types";

const satellite = () =>
  L.tileLayer("https://server.arcgisonline.com/ArcGIS/rest/services/World_Imagery/MapServer/tile/{z}/{y}/{x}", {
    maxZoom: 19,
    attribution: "Imagery © Esri, Maxar, Earthstar Geographics",
  });
const streets = () =>
  L.tileLayer("https://tile.openstreetmap.org/{z}/{x}/{y}.png", {
    maxZoom: 19,
    attribution: "© OpenStreetMap contributors",
  });

// India, until the farmer has a plot or a GPS fix.
const start: L.LatLngExpression = [20.6, 78.9];

export interface Outline {
  id: string;
  name: string;
  corners: Point[];
}

/** A zone of a crop plan, drawn filled in its colour with a label. */
export interface ZoneShape {
  ring: Point[];
  label: string;
  color: string;
}

/**
 * The map: satellite or streets, the plot being edited as a polygon with
 * numbered corners, and the farmer's other plots in outline. When editable,
 * a tap adds a corner and a corner can be dragged.
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
  className?: string;
}) {
  const el = useRef<HTMLDivElement>(null);
  const map = useRef<L.Map | null>(null);
  const layer = useRef<L.LayerGroup | null>(null);
  const otherLayer = useRef<L.LayerGroup | null>(null);
  const youLayer = useRef<L.LayerGroup | null>(null);
  const zoneLayer = useRef<L.LayerGroup | null>(null);
  // The handlers read the latest props without rebinding.
  const latest = useRef({ corners, onChange, editable });
  useEffect(() => {
    latest.current = { corners, onChange, editable };
  });

  useEffect(() => {
    if (!el.current) return;
    const m = L.map(el.current, { center: start, zoom: 5, zoomControl: true });
    const sat = satellite().addTo(m);
    L.control.layers({ Satellite: sat, Streets: streets() }, undefined, { position: "topright" }).addTo(m);
    otherLayer.current = L.layerGroup().addTo(m);
    zoneLayer.current = L.layerGroup().addTo(m);
    layer.current = L.layerGroup().addTo(m);
    youLayer.current = L.layerGroup().addTo(m);
    m.on("click", (e: L.LeafletMouseEvent) => {
      const { corners, onChange, editable } = latest.current;
      if (!editable || !onChange) return;
      onChange([...corners, { lat: round(e.latlng.lat), lng: round(e.latlng.lng) }]);
    });
    map.current = m;
    return () => {
      m.remove();
      map.current = null;
    };
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
      L.polygon(
        z.ring.map((c) => L.latLng(c.lat, c.lng)),
        { color: z.color, weight: 2, fillColor: z.color, fillOpacity: 0.45 },
      )
        .bindTooltip(z.label, { permanent: true, direction: "center", className: "zone-label" })
        .addTo(g);
    }
  }, [zones]);

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
    if (you && map.current && corners.length === 0) map.current.setView([you.lat, you.lng], 18);
    // Centre on the first fix only.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [you === undefined]);

  return <div ref={el} className={className} role="application" aria-label="Map of the plot" />;
}

/** Rounds a coordinate to 7 decimals (about 1 cm). */
function round(x: number): number {
  return Math.round(x * 1e7) / 1e7;
}
