// Two versions of an agent's definition compared as text (docs/UI.md
// "Versions"): a line per value, + and − with hidden "Added:" and "Removed:"
// words (not colour alone), and runs of unchanged lines folded.

import { cn } from "@agentkit/ui/lib/utils";
import { useState } from "react";
import { definitionText, diffLines, type DiffLine } from "./agent-def";

/** Unchanged lines kept around a change; longer runs fold. */
const context = 2;

type Row = { kind: "line"; line: DiffLine } | { kind: "fold"; lines: DiffLine[] };

function rows(lines: DiffLine[]): Row[] {
  const out: Row[] = [];
  let i = 0;
  while (i < lines.length) {
    if (lines[i]?.op !== "same") {
      out.push({ kind: "line", line: lines[i] as DiffLine });
      i++;
      continue;
    }
    let j = i;
    while (j < lines.length && lines[j]?.op === "same") j++;
    const run = lines.slice(i, j);
    const head = i === 0 ? 0 : context;
    const tail = j === lines.length ? 0 : context;
    if (run.length > head + tail + 1) {
      for (const l of run.slice(0, head)) out.push({ kind: "line", line: l });
      out.push({ kind: "fold", lines: run.slice(head, run.length - tail) });
      for (const l of run.slice(run.length - tail)) out.push({ kind: "line", line: l });
    } else {
      for (const l of run) out.push({ kind: "line", line: l });
    }
    i = j;
  }
  return out;
}

function Line({ line }: { line: DiffLine }) {
  const sign = line.op === "add" ? "+" : line.op === "del" ? "−" : " ";
  return (
    <div
      className={cn(
        "flex gap-2 px-3 whitespace-pre-wrap",
        line.op === "add" && "bg-ak-ok/10 text-ak-text",
        line.op === "del" && "bg-ak-bad/10 text-ak-text",
        line.op === "same" && "text-ak-text-2",
      )}
    >
      <span aria-hidden="true" className="w-3 flex-none select-none text-ak-muted">
        {sign}
      </span>
      <span className="min-w-0 break-words">
        {line.op === "add" ? <span className="sr-only">Added: </span> : null}
        {line.op === "del" ? <span className="sr-only">Removed: </span> : null}
        {line.text || " "}
      </span>
    </div>
  );
}

function Fold({ lines }: { lines: DiffLine[] }) {
  const [open, setOpen] = useState(false);
  if (open) return lines.map((l, i) => <Line key={i} line={l} />);
  return (
    <button
      type="button"
      onClick={() => setOpen(true)}
      className="block w-full px-3 py-0.5 text-left text-xs text-ak-muted hover:bg-ak-hover"
    >
      {lines.length} unchanged lines
    </button>
  );
}

/** The diff of from to to, or "No differences." */
export function DefinitionDiff({
  from,
  to,
  label,
}: {
  from: Record<string, unknown>;
  to: Record<string, unknown>;
  label: string;
}) {
  const lines = diffLines(definitionText(from), definitionText(to));
  if (lines.every((l) => l.op === "same")) {
    return <p className="text-(length:--ak-text-sm) text-ak-muted">No differences.</p>;
  }
  return (
    <div
      role="group"
      aria-label={label}
      className="max-h-[60vh] overflow-auto rounded-md border bg-ak-surface-2 py-1.5 font-mono text-xs leading-5"
    >
      {rows(lines).map((r, i) =>
        r.kind === "fold" ? <Fold key={i} lines={r.lines} /> : <Line key={i} line={r.line} />,
      )}
    </div>
  );
}
