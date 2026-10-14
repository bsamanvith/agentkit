import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@agentkit/ui/components/collapsible";
import { Spinner } from "@agentkit/ui/components/spinner";
import { cn } from "@agentkit/ui";
import { Calculator, ChevronRight, Clock, FileText, Globe, Wrench, type LucideIcon } from "lucide-react";
import { useState } from "react";

/** What kind of tool a step ran: picks its icon (docs/UI.md "Tool calls"). */
export type ToolKind = "search" | "page" | "calc" | "clock" | "tool";

/** A step's state. "stopped" is a call cut short by Stop: neutral, not a failure. */
export type ToolStepState = "running" | "done" | "failed" | "stopped";

const icons: Record<ToolKind, LucideIcon> = {
  search: Globe,
  page: FileText,
  calc: Calculator,
  clock: Clock,
  tool: Wrench,
};

/** A tool's display metadata (GET /v1/toolsets/{name}/tools, "display"). */
export type ToolDisplay = { title?: string; icon?: string; present?: string; past?: string };

export function toolKind(icon: string | undefined): ToolKind {
  return icon !== undefined && Object.hasOwn(icons, icon) ? (icon as ToolKind) : "tool";
}

/**
 * A call's line from its tool's display metadata: "Searching the web" while
 * it runs, "Searched the web" after; without metadata, "Running echo" and
 * "Ran echo" (docs/UI.md "Tool calls"). The detail is the first argument.
 */
export function stepText(
  name: string,
  input: unknown,
  running: boolean,
  display?: ToolDisplay,
): { kind: ToolKind; label: string; detail?: string } {
  const label = running ? (display?.present ?? `Running ${name}`) : (display?.past ?? `Ran ${name}`);
  let detail: string | undefined;
  if (input && typeof input === "object" && !Array.isArray(input)) {
    const first = Object.values(input as Record<string, unknown>)[0];
    if (typeof first === "string") detail = `“${first.length > 80 ? `${first.slice(0, 80)}…` : first}”`;
    else if (typeof first === "number" || typeof first === "boolean") detail = String(first);
  }
  return { kind: toolKind(display?.icon), label, ...(detail !== undefined ? { detail } : {}) };
}

export type ToolStepProps = {
  kind?: ToolKind;
  state: ToolStepState;
  /** The verb phrase: present tense while running ("Searching the web"), past after ("Searched the web"). */
  label: string;
  /** The key argument, in muted text: the query in quotes, the page hosts. */
  detail?: string;
  /** How long the call took, in milliseconds (done, failed). */
  durationMs?: number;
  /** The call's input and result, shown in its details when either is given. */
  input?: unknown;
  result?: string;
  className?: string;
};

/** The first results a details view shows, and the cut for a long text (docs/UI.md "Long content"). */
const maxResults = 20;
const maxChars = 4000;

function size(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

/** A value as one line of text: strings as they are, the rest as JSON. */
function show(v: unknown): string {
  if (typeof v === "string") return v;
  try {
    return JSON.stringify(v) ?? String(v);
  } catch {
    return String(v);
  }
}

type Item = { title: string; url?: string; snippet?: string };

/** Search-like results: a list whose entries all have a url or a title, at the top or under a key. */
function resultItems(parsed: unknown): Item[] | undefined {
  const lists = Array.isArray(parsed)
    ? [parsed]
    : parsed && typeof parsed === "object"
      ? Object.values(parsed as Record<string, unknown>).filter(Array.isArray)
      : [];
  for (const list of lists as unknown[][]) {
    const items = list.filter(
      (x): x is Record<string, unknown> => !!x && typeof x === "object" && ("url" in x || "title" in x),
    );
    if (items.length > 0 && items.length === list.length) {
      return items.map((x) => ({
        title: show(x.title ?? x.url ?? ""),
        ...(typeof x.url === "string" ? { url: x.url } : {}),
        ...(typeof x.content === "string"
          ? { snippet: x.content }
          : typeof x.snippet === "string"
            ? { snippet: x.snippet }
            : {}),
      }));
    }
  }
  return undefined;
}

function host(url: string | undefined): string {
  if (!url) return "";
  try {
    return new URL(url).hostname.replace(/^www\./, "");
  } catch {
    return "";
  }
}

const caption = "text-xs font-medium text-ak-muted";
const textButton = "self-start rounded-sm text-xs text-ak-text-2 underline underline-offset-2 hover:text-ak-text";

/** A call's details: the request as key/value, the result as a compact list or text, raw JSON on demand. All text. */
function Details({ input, result }: { input: unknown; result: string | undefined }) {
  const [raw, setRaw] = useState(false);
  const [all, setAll] = useState(false);
  let parsed: unknown;
  try {
    parsed = result !== undefined ? JSON.parse(result) : undefined;
  } catch {
    parsed = undefined;
  }
  const items = resultItems(parsed);
  const entries =
    input && typeof input === "object" && !Array.isArray(input) ? Object.entries(input as Record<string, unknown>) : [];
  let body = null;
  if (result !== undefined && items) {
    const shown = all ? items : items.slice(0, maxResults);
    body = (
      <ul className="flex flex-col gap-1.5">
        {shown.map((it, i) => (
          <li key={i} className="flex min-w-0 gap-2">
            <span
              aria-hidden="true"
              className="mt-0.5 grid size-4 flex-none place-items-center rounded-full bg-ak-select text-[10px] font-semibold uppercase"
            >
              {(host(it.url) || it.title).charAt(0)}
            </span>
            <span className="flex min-w-0 flex-col">
              <span className="truncate text-ak-text">
                {it.title} <span className="text-ak-muted">{host(it.url)}</span>
              </span>
              {it.snippet ? <span className="truncate text-ak-muted">{it.snippet}</span> : null}
            </span>
          </li>
        ))}
        {items.length > shown.length ? (
          <li>
            <button type="button" className={textButton} onClick={() => setAll(true)}>
              Show all {items.length}
            </button>
          </li>
        ) : null}
      </ul>
    );
  } else if (result !== undefined) {
    const long = result.length > maxChars && !all;
    body = (
      <>
        <pre tabIndex={0} aria-label="Result" className="overflow-x-auto font-mono text-xs whitespace-pre-wrap">
          {long ? `${result.slice(0, maxChars)}…` : result}
        </pre>
        {long ? (
          <button type="button" className={textButton} onClick={() => setAll(true)}>
            Show all ({size(new TextEncoder().encode(result).length)})
          </button>
        ) : null}
      </>
    );
  }
  return (
    <div className="mt-1 mb-2 ml-1.5 flex flex-col gap-2 border-l border-ak-line pl-3 text-(length:--ak-text-sm)">
      {entries.length > 0 ? (
        <div className="flex flex-col gap-1">
          <span className={caption}>Request</span>
          <dl className="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-3 gap-y-0.5">
            {entries.map(([k, v]) => (
              <div key={k} className="contents">
                <dt className="font-mono text-ak-muted">{k}</dt>
                <dd className="break-words text-ak-text-2">{show(v)}</dd>
              </div>
            ))}
          </dl>
        </div>
      ) : null}
      {body ? (
        <div className="flex flex-col gap-1">
          <span className={caption}>Result</span>
          {body}
        </div>
      ) : null}
      <button type="button" aria-expanded={raw} className={textButton} onClick={() => setRaw((r) => !r)}>
        {raw ? "Hide raw JSON" : "Show raw JSON"}
      </button>
      {raw ? (
        <pre
          tabIndex={0}
          aria-label="Raw JSON"
          className="max-h-80 overflow-auto rounded-sm bg-ak-surface-2 p-2 font-mono text-xs whitespace-pre-wrap"
        >
          {JSON.stringify({ input, output: parsed ?? result ?? null }, null, 2)}
        </pre>
      ) : null}
    </div>
  );
}

function Line({ kind = "tool", state, label, detail, durationMs }: ToolStepProps) {
  const Icon = icons[kind];
  return (
    <>
      {state === "running" ? (
        <Spinner className="size-3.5 flex-none" />
      ) : (
        <Icon aria-hidden="true" className="size-3.5 flex-none" />
      )}
      <span className="flex-none font-medium">
        {state === "stopped" ? `Stopped: ${label.charAt(0).toLowerCase()}${label.slice(1)}` : label}
        {state === "failed" ? " failed" : ""}
      </span>
      {detail ? <span className="min-w-0 truncate text-ak-muted">{detail}</span> : null}
      {durationMs !== undefined && state !== "running" ? (
        <span className="ml-auto flex-none text-xs text-ak-muted tabular-nums">{formatSeconds(durationMs)}</span>
      ) : null}
    </>
  );
}

/**
 * One tool call as a quiet line: icon, verb phrase, key argument, time.
 * With an input or a result it opens in place to its details. Everything
 * here is text: tool output is never rendered as HTML.
 */
export function ToolStep(props: ToolStepProps) {
  const { state, className, input, result } = props;
  const line = cn(
    "flex min-h-6 w-full min-w-0 items-center gap-2 text-left text-sm",
    state === "failed" ? "text-ak-bad" : "text-ak-text-2",
  );
  if (input === undefined && result === undefined) {
    return (
      <div className={cn(line, className)} data-state={state}>
        <Line {...props} />
      </div>
    );
  }
  return (
    <Collapsible className={className}>
      <CollapsibleTrigger className={cn(line, "group rounded-sm hover:text-ak-text")} data-step={state}>
        <Line {...props} />
        <ChevronRight
          aria-hidden="true"
          className="size-3.5 flex-none text-ak-muted transition-transform group-data-[state=open]:rotate-90"
        />
      </CollapsibleTrigger>
      <CollapsibleContent>
        <Details input={input} result={result} />
      </CollapsibleContent>
    </Collapsible>
  );
}

/** 1400 → "1.4 s". */
export function formatSeconds(ms: number): string {
  return `${(ms / 1000).toFixed(1)} s`;
}
