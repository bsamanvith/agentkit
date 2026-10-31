import { Status, type StatusTone } from "@agentkit/ui";
import { AlertTriangle, CheckCircle2, Info, XCircle } from "lucide-react";
import { num, place } from "./coords";
import type { Check, Report, Status as ReportStatus } from "./types";

const tone: Record<ReportStatus, StatusTone> = { verified: "ok", needs_review: "warn", invalid: "bad" };
const label: Record<ReportStatus, string> = { verified: "Verified", needs_review: "Needs review", invalid: "Invalid" };

export function StatusBadge({ status }: { status: ReportStatus }) {
  return <Status tone={tone[status]}>{label[status]}</Status>;
}

function CheckIcon({ c }: { c: Check }) {
  switch (c.severity) {
    case "error":
      return <XCircle className="mt-0.5 size-4 shrink-0 text-ak-bad" aria-label="Error" />;
    case "warning":
      return <AlertTriangle className="mt-0.5 size-4 shrink-0 text-ak-warn" aria-label="Warning" />;
  }
  return c.id === "area.declared" || c.id === "location.found" ? (
    <CheckCircle2 className="mt-0.5 size-4 shrink-0 text-ak-ok" aria-label="Passed" />
  ) : (
    <Info className="mt-0.5 size-4 shrink-0 text-ak-muted" aria-label="Note" />
  );
}

/** A boundary's verification: its status, sizes, where it is, and every check. */
export function ReportView({ report }: { report: Report }) {
  const m = report.measures;
  // Errors first, then warnings, then the rest.
  const order = { error: 0, warning: 1, info: 2 };
  const checks = [...report.checks].sort((a, b) => order[a.severity] - order[b.severity]);
  return (
    <section aria-label="Verification" className="flex flex-col gap-3">
      <div className="flex items-center justify-between">
        <StatusBadge status={report.status} />
        {m && <span className="text-xs text-muted-foreground">{m.corners} corners</span>}
      </div>
      {m && (
        <div>
          <div className="text-2xl font-semibold" data-testid="area">
            {m.acres_text}
          </div>
          <div className="text-sm text-muted-foreground">
            {num(m.acres, 3)} acres · {num(m.hectares, 3)} ha · {num(m.area_m2, 0)} m² · {num(m.cents, 1)} cents
          </div>
          <div className="text-sm text-muted-foreground">Perimeter {num(m.perimeter_m, 0)} m</div>
        </div>
      )}
      {report.location && <div className="text-sm">{place(report.location)}</div>}
      {checks.length > 0 && (
        <ul className="flex flex-col gap-2">
          {checks.map((c, i) => (
            <li key={i} className="flex gap-2 text-sm" data-check={c.id} data-severity={c.severity}>
              <CheckIcon c={c} />
              <span>
                {c.message}
                {c.corners && c.corners.length > 0 && (
                  <span className="text-muted-foreground"> (corner {c.corners.map((n) => n + 1).join(", ")})</span>
                )}
              </span>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
