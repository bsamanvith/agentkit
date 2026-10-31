import { Button } from "@agentkit/ui/components/button";
import { Input } from "@agentkit/ui/components/input";
import { Textarea } from "@agentkit/ui/components/textarea";
import { cn } from "@agentkit/ui";
import { Crosshair, Footprints, Keyboard, MapPin, Trash2, Undo2 } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { ApiError, api } from "./api";
import { formatCorners, parseCorners } from "./coords";
import { FarmMap, type Outline } from "./map";
import { ReportView } from "./report";
import { units, type Corner, type Draft, type Farm, type Report, type Source, type Unit } from "./types";

type Mode = Source;

const modes: { id: Mode; label: string; icon: typeof MapPin; help: string }[] = [
  {
    id: "map",
    label: "Draw",
    icon: MapPin,
    help: "Tap each corner of the plot on the map, in order around it. Drag a corner to move it.",
  },
  {
    id: "gps",
    label: "Walk",
    icon: Footprints,
    help: "Stand at a corner, wait for a good fix, and tap “Add my position”. Walk to the next corner and repeat.",
  },
  {
    id: "typed",
    label: "Type",
    icon: Keyboard,
    help: "One corner per line: latitude, longitude (17.385, 78.4867). Degrees-minutes-seconds work too.",
  },
];

const hasGPS = typeof navigator !== "undefined" && "geolocation" in navigator;

interface Fix {
  lat: number;
  lng: number;
  accuracy: number;
}

/** Adds or edits a plot: its name, the size the farmer declares, and its boundary, verified as it is drawn. */
export function Editor({
  farm,
  others,
  onSaved,
  onCancel,
}: {
  farm?: Farm;
  others: Farm[];
  onSaved: (f: Farm) => void;
  onCancel: () => void;
}) {
  const [name, setName] = useState(farm?.name ?? "");
  const [declared, setDeclared] = useState(farm?.declared ? String(farm.declared.value) : "");
  const [unit, setUnit] = useState<Unit>(farm?.declared?.unit ?? "acre");
  const [corners, setCorners] = useState<Corner[]>(farm?.boundary.corners ?? []);
  const [mode, setMode] = useState<Mode>(farm?.boundary.source ?? "map");
  const [text, setText] = useState(formatCorners(farm?.boundary.corners ?? []));
  const [parseErrors, setParseErrors] = useState<string[]>([]);
  const [report, setReport] = useState<Report | undefined>(farm?.report);
  const [checking, setChecking] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [fix, setFix] = useState<Fix>();
  const [gpsError, setGpsError] = useState("");
  const [fit, setFit] = useState(farm ? 1 : 0);

  const declaredArea = useMemo(() => {
    const v = Number(declared);
    return declared.trim() !== "" && Number.isFinite(v) && v > 0 ? { value: v, unit } : undefined;
  }, [declared, unit]);

  // Verify as the boundary changes, a moment after the last change.
  useEffect(() => {
    if (corners.length < 3) return;
    const ctl = new AbortController();
    const t = setTimeout(() => {
      setChecking(true);
      api
        .verify({ boundary: { corners, source: mode }, declared: declaredArea }, farm?.id, ctl.signal)
        .then(setReport)
        .catch((e: unknown) => {
          if (!ctl.signal.aborted) setError(e instanceof Error ? e.message : String(e));
        })
        .finally(() => {
          if (!ctl.signal.aborted) setChecking(false);
        });
    }, 500);
    return () => {
      clearTimeout(t);
      ctl.abort();
    };
  }, [corners, declaredArea, mode, farm?.id]);

  // Follow the GPS while walking.
  useEffect(() => {
    if (mode !== "gps" || !hasGPS) return;
    const id = navigator.geolocation.watchPosition(
      (p) => {
        setGpsError("");
        setFix({ lat: p.coords.latitude, lng: p.coords.longitude, accuracy: p.coords.accuracy });
      },
      (e) =>
        setGpsError(
          e.code === e.PERMISSION_DENIED
            ? "Location permission was refused: allow it in the browser to walk the plot."
            : e.message,
        ),
      { enableHighAccuracy: true, maximumAge: 0, timeout: 30_000 },
    );
    return () => navigator.geolocation.clearWatch(id);
  }, [mode]);

  const change = (c: Corner[]) => {
    setCorners(c);
    setText(formatCorners(c));
    setError("");
  };

  const applyText = () => {
    const p = parseCorners(text);
    setParseErrors(p.errors.map((e) => `Line ${e.line} (${e.text}): ${e.why}`));
    if (p.errors.length === 0) {
      setCorners(p.corners);
      setFit((n) => n + 1);
    }
  };

  const flagged = useMemo(
    () => [...new Set(report?.checks.flatMap((c) => (c.severity === "info" ? [] : (c.corners ?? []))) ?? [])],
    [report],
  );
  const outlines: Outline[] = useMemo(
    () => others.filter((o) => o.id !== farm?.id).map((o) => ({ id: o.id, name: o.name, corners: o.boundary.corners })),
    [others, farm?.id],
  );

  const save = async () => {
    setSaving(true);
    setError("");
    const draft: Draft = { name, declared: declaredArea, boundary: { corners, source: mode } };
    try {
      onSaved(farm ? await api.update(farm.id, draft) : await api.create(draft));
    } catch (e) {
      if (e instanceof ApiError && e.report) setReport(e.report);
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  };

  // A report is about the corners it was made for; under 3 there is none.
  const shown = corners.length >= 3 ? report : undefined;
  const canSave = name.trim() !== "" && shown !== undefined && shown.status !== "invalid" && !saving;
  const help = modes.find((m) => m.id === mode)?.help;

  return (
    <div className="flex flex-col gap-4 lg:flex-row">
      <div className="flex flex-col gap-2 lg:flex-1">
        <div
          role="tablist"
          aria-label="How to give the boundary"
          className="grid grid-cols-3 gap-1 rounded-lg bg-muted p-1"
        >
          {modes.map((m) => (
            <button
              key={m.id}
              role="tab"
              aria-selected={mode === m.id}
              onClick={() => setMode(m.id)}
              className={cn(
                "flex items-center justify-center gap-1.5 rounded-md px-2 py-1.5 text-sm font-medium",
                mode === m.id ? "bg-background shadow-sm" : "text-muted-foreground",
              )}
            >
              <m.icon className="size-4" aria-hidden="true" />
              {m.label}
            </button>
          ))}
        </div>
        <p className="text-sm text-muted-foreground">{help}</p>
        <FarmMap
          corners={corners}
          onChange={change}
          editable={mode === "map"}
          flagged={flagged}
          others={outlines}
          fit={fit}
          you={mode === "gps" ? fix : undefined}
          className="h-[55vh] min-h-72 w-full overflow-hidden rounded-lg border lg:h-[70vh]"
        />
        <div className="flex flex-wrap gap-2">
          {mode === "gps" && (
            <Button
              disabled={!fix}
              onClick={() =>
                fix &&
                change([...corners, { lat: fix.lat, lng: fix.lng, accuracy_m: Math.round(fix.accuracy * 10) / 10 }])
              }
            >
              <Crosshair /> Add my position
            </Button>
          )}
          <Button variant="outline" disabled={corners.length === 0} onClick={() => change(corners.slice(0, -1))}>
            <Undo2 /> Undo corner
          </Button>
          <Button variant="outline" disabled={corners.length === 0} onClick={() => change([])}>
            <Trash2 /> Clear
          </Button>
          <Button variant="ghost" disabled={corners.length === 0} onClick={() => setFit((n) => n + 1)}>
            Zoom to plot
          </Button>
        </div>
        {mode === "gps" && (
          <p className="text-sm" role="status">
            {(hasGPS ? gpsError : "This device has no GPS.") ||
              (fix ? `GPS fix: within ${Math.round(fix.accuracy)} m` : "Waiting for GPS…")}
          </p>
        )}
        {mode === "typed" && (
          <div className="flex flex-col gap-2">
            <Textarea
              aria-label="Corners"
              rows={6}
              value={text}
              onChange={(e) => setText(e.target.value)}
              placeholder={"17.385000, 78.486700\n17.385000, 78.487300\n17.385570, 78.487300\n17.385570, 78.486700"}
              className="font-mono text-sm"
            />
            <Button variant="secondary" onClick={applyText} className="self-start">
              Show on map
            </Button>
            {parseErrors.map((e) => (
              <p key={e} className="text-sm text-ak-bad">
                {e}
              </p>
            ))}
          </div>
        )}
      </div>

      <div className="flex flex-col gap-4 lg:w-96">
        <label className="flex flex-col gap-1 text-sm font-medium">
          Name
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="East field" maxLength={200} />
        </label>
        <div className="flex flex-col gap-1 text-sm font-medium">
          <span id="declared-label">Size on your papers (optional)</span>
          <div className="flex gap-2">
            <Input
              aria-labelledby="declared-label"
              inputMode="decimal"
              value={declared}
              onChange={(e) => setDeclared(e.target.value)}
              placeholder="2.5"
              className="w-28"
            />
            <select
              aria-label="Unit"
              value={unit}
              onChange={(e) => setUnit(e.target.value as Unit)}
              className="h-9 rounded-md border bg-background px-2 text-sm"
            >
              {units.map((u) => (
                <option key={u.id} value={u.id}>
                  {u.label}
                </option>
              ))}
            </select>
          </div>
        </div>

        <div className="rounded-lg border p-4">
          {shown ? (
            <ReportView report={shown} />
          ) : (
            <p className="text-sm text-muted-foreground">
              {corners.length < 3
                ? `Add at least 3 corners (${corners.length} so far) to check the plot.`
                : "Checking…"}
            </p>
          )}
          {checking && shown && <p className="mt-2 text-xs text-muted-foreground">Checking…</p>}
        </div>

        {error && (
          <p className="text-sm text-ak-bad" role="alert">
            {error}
          </p>
        )}
        <div className="flex gap-2">
          <Button disabled={!canSave} onClick={save}>
            {saving ? "Saving…" : farm ? "Save changes" : "Save plot"}
          </Button>
          <Button variant="ghost" onClick={onCancel}>
            Cancel
          </Button>
        </div>
      </div>
    </div>
  );
}
