import { Button } from "@agentkit/ui/components/button";
import { Input } from "@agentkit/ui/components/input";
import { Textarea } from "@agentkit/ui/components/textarea";
import { cn } from "@agentkit/ui";
import {
  Crosshair,
  Footprints,
  Hand,
  Keyboard,
  MapPin,
  Pencil,
  Search,
  Spline,
  Square,
  Trash2,
  Undo2,
} from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { ApiError, api } from "./api";
import { formatCorners, parseCorners } from "./coords";
import { FarmMap, type Outline } from "./map";
import { ReportView, StatusBadge } from "./report";
import { useSketch, type Tool } from "./sketch";
import {
  units,
  type Corner,
  type Draft,
  type Farm,
  type PlaceMatch,
  type Point,
  type Report,
  type Source,
  type Unit,
} from "./types";

type Mode = Source;

const modes: { id: Mode; label: string; icon: typeof MapPin; help: string }[] = [
  {
    id: "map",
    label: "Draw",
    icon: MapPin,
    help: "Find your village and zoom in until you see your field.",
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

const tools: { id: Tool; label: string; icon: typeof MapPin; help: string }[] = [
  {
    id: "pencil",
    label: "Pencil",
    icon: Pencil,
    help: "Draw round the field with your finger. We turn the line into corners; drag a corner to fix it.",
  },
  {
    id: "lines",
    label: "Lines",
    icon: Spline,
    help: "Tap each corner in order around the field. Drag a corner to move it.",
  },
  {
    id: "rect",
    label: "Rectangle",
    icon: Square,
    help: "Drag from one corner of the field to the opposite one. Turn it with ↻, stretch it with ↕ and ↔, or drag a corner.",
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
  guided = false,
  onStep,
}: {
  farm?: Farm;
  others: Farm[];
  onSaved: (f: Farm) => void;
  onCancel: () => void;
  /** One step at a time, for a first plot on a phone: the village, the boundary, the name. */
  guided?: boolean;
  /** Told the guided step shown, from 0. */
  onStep?: (n: number) => void;
}) {
  const [name, setName] = useState(farm?.name ?? (guided ? "My field" : ""));
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
  const [tool, setTool] = useState<Tool>(farm ? "lines" : "pencil");
  const [panning, setPanning] = useState(false);
  const [step, setStepState] = useState(0);
  const setStep = (n: number) => {
    setStepState(n);
    onStep?.(n);
  };

  const declaredArea = useMemo(() => {
    const v = Number(declared);
    return declared.trim() !== "" && Number.isFinite(v) && v > 0 ? { value: v, unit } : undefined;
  }, [declared, unit]);
  // A size typed but not a plain number ("2 acre 10 gunta", "2,5") is not checked: say so.
  const badSize = declared.trim() !== "" && declaredArea === undefined;
  const [goto, setGoto] = useState<Point>();

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

  const sketch = useSketch(
    corners,
    (c) => {
      setCorners(c);
      setText(formatCorners(c));
      setError("");
    },
    tool,
    panning,
  );
  const change = sketch.set;

  const applyText = () => {
    const p = parseCorners(text);
    setParseErrors(p.errors.map((e) => `Line ${e.line} (${e.text}): ${e.why}`));
    if (p.errors.length === 0) {
      change(p.corners);
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

  const drawing = mode === "map" && (!guided || step === 1);
  const at = (n: number) => !guided || step === n;
  const status = shown ? (
    <ReportView report={shown} />
  ) : (
    <p className="text-sm text-muted-foreground">
      {corners.length < 3 ? `Add at least 3 corners (${corners.length} so far) to check the plot.` : "Checking…"}
    </p>
  );

  return (
    <div className={cn("flex flex-col gap-4", !guided && "lg:flex-row")}>
      <div className="flex flex-col gap-2 lg:flex-1">
        {at(1) && (
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
        )}
        {at(1) && <p className="text-sm text-muted-foreground">{help}</p>}
        {mode === "map" && at(0) && <PlaceSearch onPick={(p) => setGoto({ ...p })} />}
        {drawing && (
          <div className="flex flex-wrap items-center gap-2" role="toolbar" aria-label="Drawing tools">
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
            {tool !== "lines" && (
              <Button
                size="sm"
                variant={panning ? "default" : "ghost"}
                aria-pressed={panning}
                onClick={() => setPanning((p) => !p)}
              >
                <Hand /> Move map
              </Button>
            )}
          </div>
        )}
        {drawing && (
          <p className="text-sm text-muted-foreground">
            {panning ? "Drag to move the map; tap the tool again to draw." : tools.find((t) => t.id === tool)?.help}
            {tool !== "lines" && !panning && " Two fingers move the map."}
          </p>
        )}
        <FarmMap
          corners={corners}
          onChange={sketch.map.onChange}
          editable={mode === "map"}
          taps={drawing && sketch.map.taps}
          stroke={drawing ? sketch.map.stroke : undefined}
          onStroke={sketch.map.onStroke}
          handles={drawing ? sketch.map.handles : []}
          onHandle={sketch.map.onHandle}
          flagged={flagged}
          others={outlines}
          fit={fit}
          you={mode === "gps" ? fix : undefined}
          goto={goto}
          className="h-[55vh] min-h-72 w-full overflow-hidden rounded-lg border lg:h-[70vh]"
        />
        {at(1) && (
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
            <Button variant="outline" disabled={!sketch.canUndo} onClick={sketch.undo}>
              <Undo2 /> Undo
            </Button>
            <Button variant="outline" disabled={corners.length === 0} onClick={() => change([])}>
              <Trash2 /> Clear
            </Button>
            <Button variant="ghost" disabled={corners.length === 0} onClick={() => setFit((n) => n + 1)}>
              Zoom to plot
            </Button>
          </div>
        )}
        {mode === "gps" && at(1) && (
          <p className="text-sm" role="status">
            {(hasGPS ? gpsError : "This device has no GPS.") ||
              (fix ? `GPS fix: within ${Math.round(fix.accuracy)} m` : "Waiting for GPS…")}
          </p>
        )}
        {mode === "typed" && at(1) && (
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
        {guided && step === 1 && (
          <p className="flex items-center gap-2 text-sm" role="status">
            {shown ? (
              <>
                <StatusBadge status={shown.status} /> {shown.measures?.acres_text}
              </>
            ) : (
              status
            )}
          </p>
        )}
      </div>

      {at(2) && (
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
            {badSize && (
              <p className="text-xs font-normal text-ak-warn" role="status">
                Type just the number, like 2.5, and pick the unit. This size is not checked.
              </p>
            )}
          </div>

          <div className="rounded-lg border p-4">
            {status}
            {checking && shown && <p className="mt-2 text-xs text-muted-foreground">Checking…</p>}
          </div>

          {error && (
            <p className="text-sm text-ak-bad" role="alert">
              {error}
            </p>
          )}
          {name.trim() === "" && shown !== undefined && shown.status !== "invalid" && (
            <p className="text-sm text-muted-foreground">Give the plot a name to save it.</p>
          )}
          <div className="flex gap-2">
            <Button disabled={!canSave} onClick={save}>
              {saving ? "Saving…" : farm ? "Save changes" : "Save plot"}
            </Button>
            <Button variant="ghost" onClick={guided ? () => setStep(1) : onCancel}>
              {guided ? "Back" : "Cancel"}
            </Button>
          </div>
        </div>
      )}
      {guided && step < 2 && (
        <div className="flex gap-2">
          <Button
            disabled={step === 1 && (shown === undefined || shown.status === "invalid")}
            onClick={() => setStep(step + 1)}
          >
            {step === 0 ? "Next: draw the plot" : "Next: name it"}
          </Button>
          {step > 0 && (
            <Button variant="ghost" onClick={() => setStep(step - 1)}>
              Back
            </Button>
          )}
        </div>
      )}
    </div>
  );
}

/** Finds a village or town by name and moves the map there, so the farmer need not pan across India. */
function PlaceSearch({ onPick }: { onPick: (p: Point) => void }) {
  const [q, setQ] = useState("");
  const [found, setFound] = useState<PlaceMatch[]>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const find = (e: React.FormEvent) => {
    e.preventDefault();
    if (q.trim() === "") return;
    setBusy(true);
    setError("");
    api
      .places(q.trim())
      .then(
        (ps) => {
          setFound(ps);
          const [only] = ps;
          if (ps.length === 1 && only) onPick(only.point);
        },
        (err: unknown) => {
          setFound(undefined);
          setError(err instanceof Error ? err.message : String(err));
        },
      )
      .finally(() => setBusy(false));
  };
  return (
    <div className="flex flex-col gap-1">
      <form role="search" onSubmit={find} className="flex gap-2">
        <Input
          aria-label="Village or town"
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder="Your village or town"
          className="flex-1"
        />
        <Button type="submit" variant="secondary" disabled={busy || q.trim() === ""}>
          <Search /> {busy ? "Finding…" : "Find"}
        </Button>
      </form>
      {error && (
        <p role="alert" className="text-sm text-ak-bad">
          {error}
        </p>
      )}
      {found?.length === 0 && (
        <p className="text-sm text-muted-foreground">No place by that name. Try the mandal or district.</p>
      )}
      {found && found.length > 1 && (
        <ul aria-label="Places found" className="flex flex-col gap-1">
          {found.map((p) => (
            <li key={`${p.point.lat},${p.point.lng}`}>
              <button
                type="button"
                className="w-full rounded-md border px-2 py-1.5 text-left text-sm hover:bg-accent"
                onClick={() => {
                  onPick(p.point);
                  setFound(undefined);
                }}
              >
                {p.address || p.name}
              </button>
            </li>
          ))}
        </ul>
      )}
      <p className="text-xs text-muted-foreground">A tap zooms in until the fields are close enough to draw.</p>
    </div>
  );
}
