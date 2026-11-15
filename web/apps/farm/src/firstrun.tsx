import { Button } from "@agentkit/ui/components/button";
import { Plus, Sprout } from "lucide-react";
import { useEffect, useState } from "react";
import { api } from "./api";
import { Editor } from "./editor";
import { guidedSteps } from "./sketch";
import { formatDate, localName } from "./plan";
import type { Crop, Farm, Option } from "./types";

/** Where the first-run path ends: the plot, on the tab that shows its season. */
export type Landing = "plan" | "roadmap" | undefined;

const selectClass = "h-9 w-full rounded-md border bg-background px-2 text-sm";

function message(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

/**
 * The first run, for a farmer with no plots: find the village, draw the
 * plot, name it, say the crop (or take ours) and see the dates. Every step
 * after the drawing can be skipped, and nothing else is asked.
 */
export function FirstRun({
  planner,
  roadmap,
  onAdd,
  onDone,
}: {
  /** Crop planning is on: the crop step is offered. */
  planner: boolean;
  /** The season roadmap is on: the path ends on it. */
  roadmap: boolean;
  /** Opens the plain plot editor, for a farmer who skips the guide. */
  onAdd: () => void;
  /** The path is over: open this plot, on this tab. */
  onDone: (f: Farm, tab?: Landing) => void;
}) {
  const [skipped, setSkipped] = useState(false);
  const [step, setStep] = useState(0);
  const [farm, setFarm] = useState<Farm>();
  const steps: string[] = [...guidedSteps, ...(planner ? ["Your crop and its dates"] : [])];
  const shown = farm ? 3 : step;

  if (skipped)
    return (
      <div className="flex flex-col items-center gap-4 rounded-2xl border bg-ak-surface px-6 py-16 text-center shadow-sm">
        <span className="flex size-14 items-center justify-center rounded-2xl bg-[#3f6212]/10">
          <Sprout className="size-8 text-ak-ok" aria-hidden="true" />
        </span>
        <h1 className="text-2xl font-semibold">Add your first plot</h1>
        <p className="max-w-sm text-sm text-muted-foreground">
          Draw it on the satellite map, walk around it with your phone&apos;s GPS, or type the coordinates. We measure
          it and check that it looks right.
        </p>
        <Button onClick={onAdd}>
          <Plus /> Add plot
        </Button>
      </div>
    );

  return (
    <section aria-label="Your first plot" className="flex flex-col gap-4">
      <div className="flex flex-col gap-1">
        <p className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
          Step {shown + 1} of {steps.length}
        </p>
        <h1 className="text-2xl font-semibold">{steps[shown]}</h1>
        <ol aria-label="Steps" className="flex gap-1">
          {steps.map((s, i) => (
            <li
              key={s}
              aria-current={i === shown ? "step" : undefined}
              title={s}
              className="h-1 flex-1 rounded-full"
              style={{ background: i <= shown ? "#4d7c0f" : "#d6d3d1" }}
            />
          ))}
        </ol>
      </div>
      {farm ? (
        <CropStep
          farm={farm}
          // HANDOVER(T145): once the Cycles view lands, end the path there
          // (the plot's cycles) instead of the season plan or roadmap tab.
          onDone={(f, planned) => onDone(f, planned ? (roadmap ? "roadmap" : "plan") : undefined)}
        />
      ) : (
        <Editor
          guided
          others={[]}
          onStep={setStep}
          onCancel={() => setSkipped(true)}
          onSaved={(f) => (planner ? setFarm(f) : onDone(f))}
        />
      )}
      {!farm && (
        <Button variant="link" className="self-start px-0 text-muted-foreground" onClick={() => setSkipped(true)}>
          Skip the guide
        </Button>
      )}
    </section>
  );
}

/** The last step: the crop the farmer wants, or the ones we suggest, and their dates. */
function CropStep({ farm, onDone }: { farm: Farm; onDone: (f: Farm, planned: boolean) => void }) {
  const [crops, setCrops] = useState<Crop[]>([]);
  const [crop, setCrop] = useState("");
  const [options, setOptions] = useState<Option[]>();
  const [pick, setPick] = useState<Option>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    api.crops().then(setCrops, () => setCrops([]));
  }, []);

  const ask = (id?: string) => {
    setBusy(true);
    setError("");
    setPick(undefined);
    api
      .recommend(farm.id, id ? { crops: [id] } : { limit: 3 })
      .then(
        (r) => {
          setOptions(r.options);
          if (id) setPick(r.options[0]);
          if (r.options.length === 0) setError("No dates for that crop here. Try another, or skip for now.");
        },
        (e: unknown) => setError(message(e)),
      )
      .finally(() => setBusy(false));
  };
  const accept = (o: Option) => {
    setBusy(true);
    setError("");
    api
      .layout(farm.id, [{ crop: o.crop, share: 1, sow: o.sow }])
      .then((r) => api.savePlan(farm.id, r.plan))
      .then(
        (f) => onDone(f, true),
        (e: unknown) => {
          setError(message(e));
          setBusy(false);
        },
      );
  };

  return (
    <div className="flex flex-col gap-4">
      <p className="text-sm text-muted-foreground">
        {farm.name} is saved
        {farm.report.measures?.acres_text ? `: ${farm.report.measures.acres_text}` : ""}. What do you want to grow on
        it?
      </p>
      <div className="flex flex-col gap-2 rounded-lg border p-4">
        <label className="flex flex-col gap-1 text-sm font-medium">
          Crop
          <select
            aria-label="Crop"
            className={selectClass}
            value={crop}
            onChange={(e) => {
              setCrop(e.target.value);
              if (e.target.value) ask(e.target.value);
            }}
          >
            <option value="">Pick a crop</option>
            {crops.map((c) => (
              <option key={c.id} value={c.id}>
                {c.name}
                {localName(c.names) && ` · ${localName(c.names)}`}
              </option>
            ))}
          </select>
        </label>
        <Button
          variant="outline"
          className="self-start"
          disabled={busy}
          onClick={() => {
            setCrop("");
            ask();
          }}
        >
          <Sprout /> {busy ? "Working it out…" : "Suggest for me"}
        </Button>
      </div>
      {error && (
        <p role="alert" className="text-sm text-ak-bad">
          {error}
        </p>
      )}
      {options && !pick && options.length > 0 && (
        <ul aria-label="Suggested crops" className="flex flex-col gap-2">
          {options.map((o) => (
            <li key={o.crop}>
              <button
                className="flex w-full flex-col gap-1 rounded-lg border p-3 text-left text-sm hover:bg-accent"
                onClick={() => setPick(o)}
              >
                <span className="font-medium">
                  {o.name}
                  {localName(o.names) && (
                    <span className="font-normal text-muted-foreground"> · {localName(o.names)}</span>
                  )}
                </span>
                <span className="text-muted-foreground">
                  Sow {formatDate(o.sow)}, harvest about {formatDate(o.harvest)}
                </span>
              </button>
            </li>
          ))}
        </ul>
      )}
      {pick && (
        <section aria-label="Your dates" className="flex flex-col gap-3 rounded-lg border p-4">
          <h2 className="font-semibold">{pick.name}</h2>
          <dl className="grid grid-cols-2 gap-3">
            <div>
              <dt className="text-xs text-muted-foreground">Sow</dt>
              <dd className="text-lg font-semibold">{formatDate(pick.sow)}</dd>
            </div>
            <div>
              <dt className="text-xs text-muted-foreground">Harvest, about</dt>
              <dd className="text-lg font-semibold">{formatDate(pick.harvest)}</dd>
            </div>
          </dl>
          {pick.window && (
            <p className="text-sm text-muted-foreground">
              A good time to sow: {formatDate(pick.window.from)} to {formatDate(pick.window.to)}.
            </p>
          )}
          {!pick.window && pick.next && (
            <p className="text-sm text-muted-foreground">
              The next good time to sow: {formatDate(pick.next.from)} to {formatDate(pick.next.to)}.
            </p>
          )}
          <div className="flex flex-wrap gap-2">
            <Button disabled={busy} onClick={() => accept(pick)}>
              {busy ? "Saving…" : "Save and see the season"}
            </Button>
            {(options?.length ?? 0) > 1 && (
              <Button variant="outline" onClick={() => setPick(undefined)}>
                Other suggestions
              </Button>
            )}
          </div>
        </section>
      )}
      <Button variant="ghost" className="self-start" onClick={() => onDone(farm, false)}>
        {pick ? "Not now: go to my plot" : "Skip: go to my plot"}
      </Button>
    </div>
  );
}
