import { Button } from "@agentkit/ui/components/button";
import { Sprout, X } from "lucide-react";
import { useState } from "react";
import { call } from "./api";
import { formatDate, localName } from "./plan";
import type { Note, Option, Point } from "./types";

/** What POST /api/recommend answers for a place with no plot (farmagent.PlaceAnswer). */
export interface PlaceAnswer {
  place: string;
  point: Point;
  regions: string[];
  assumed?: Note[];
  today: string;
  options: Option[];
  notes?: Note[];
  /** Field crops that suit sowing within six weeks; empty or missing when none does. */
  soon?: Option[];
  /** In a line: what can go in now, or that nothing can and what comes next. */
  headline?: string;
}

/** The assumption line, in plain words: what the answer took for granted. */
function assumedLine(assumed: Note[] = []): string {
  const parts: string[] = [];
  if (assumed.some((n) => n.id === "water.rain_only")) parts.push("rain only, no irrigation");
  if (assumed.some((n) => n.id === "soil.unknown")) parts.push("the soil not known");
  return parts.length ? `Assuming ${parts.join(", and ")}.` : "";
}

function sowWhen(o: Option): string {
  const w = o.window ?? o.next;
  if (w && w.from !== w.to) return `Sow ${formatDate(w.from)} to ${formatDate(w.to)}`;
  return `Sow about ${formatDate(o.sow)}`;
}

/**
 * Before the plot is drawn: what can be sown at the village now, the top
 * three by the same rules a saved plot gets. Optional, and never in the
 * way of drawing: it asks only when tapped and fails quietly.
 */
export default function SowHere({ at }: { at: Point }) {
  const [open, setOpen] = useState(false);
  const [answer, setAnswer] = useState<PlaceAnswer>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const ask = () => {
    setOpen(true);
    if (answer) return;
    setBusy(true);
    setError("");
    call<PlaceAnswer>("POST", "/api/recommend", { lat: at.lat, lng: at.lng, limit: 3 })
      .then(setAnswer, () => setError("Could not work it out just now. Draw your plot and we will try again there."))
      .finally(() => setBusy(false));
  };

  if (!open)
    return (
      <Button variant="outline" size="sm" className="self-start" onClick={ask}>
        <Sprout /> What can I sow here now?
      </Button>
    );

  return (
    <section aria-label="What you can sow here" className="flex flex-col gap-2 rounded-lg border p-3 text-sm">
      <div className="flex items-center justify-between gap-2">
        <h2 className="font-semibold">What you can sow here now</h2>
        <Button variant="ghost" size="sm" aria-label="Hide" onClick={() => setOpen(false)}>
          <X />
        </Button>
      </div>
      {busy && <p className="text-muted-foreground">Working it out…</p>}
      {error && <p className="text-muted-foreground">{error}</p>}
      {answer && answer.options.length === 0 && (
        <p className="text-muted-foreground">Nothing scores well here now. Draw your plot to see later seasons.</p>
      )}
      {answer?.headline && <p className="font-medium">{answer.headline}</p>}
      {answer && answer.options.length > 0 && (
        <ol aria-label="Crops to sow here" className="flex flex-col gap-1">
          {(answer.soon?.length ? answer.soon : answer.options).slice(0, 3).map((o) => (
            <li key={o.crop} className="flex flex-col rounded-md border px-2 py-1.5">
              <span className="font-medium">
                {o.name}
                {localName(o.names) && (
                  <span className="font-normal text-muted-foreground"> · {localName(o.names)}</span>
                )}
              </span>
              <span className="text-muted-foreground">
                {sowWhen(o)}, harvest about {formatDate(o.harvest)}
              </span>
            </li>
          ))}
        </ol>
      )}
      {answer && (
        <p className="text-xs text-muted-foreground">
          {assumedLine(answer.assumed)} Draw your plot next for dates on your own land.
        </p>
      )}
    </section>
  );
}
