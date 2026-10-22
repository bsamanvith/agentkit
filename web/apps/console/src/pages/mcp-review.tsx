// Reviewing an MCP server's tools (docs/UI.md "Adding an MCP server", the
// mockup's toolReviewCard and mcpReviewView): one card per tool to approve
// or reject, and the review page, /toolsets/{name}/review, for a server
// that changed or added tools since they were approved.
//
// A server's names, titles, descriptions and schemas are hostile input.
// They are rendered as React text only: no markup, no links built from
// them, no dangerouslySetInnerHTML.

import { ApiError, type ReviewTool, type ServerToolset } from "@agentkit/agent-ui/api";
import { Status } from "@agentkit/ui";
import { Button } from "@agentkit/ui/components/button";
import { Ban, Check, CircleAlert, ShieldAlert } from "lucide-react";
import { useMemo, useState, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useApi, useResource } from "../api";
import { useLists } from "../lists";
import { Loaded, Page } from "./page";
import { useForbidden } from "./forbidden";
import { Callout, Forbidden, Section } from "./parts";
import {
  changeWords,
  decisions,
  diffTokens,
  initialChoices,
  kinds,
  reviewErrorText,
  risks,
  undecided,
  type Choice,
  type Choices,
  lines,
  needsDecision,
  schemaText,
  reviewFlags,
  serverClaims,
  visible,
  words,
  type DiffPart,
} from "./mcp-servers";
import { fields } from "./schema";
import { FieldList } from "./toolset";

/** A diff of text, approved → now: removed in red with −, added in green with +, never colour alone. */
function Diff({ label, parts }: { label: string; parts: DiffPart[] }) {
  return (
    <figure className="flex flex-col gap-1">
      <figcaption className="text-xs font-medium text-ak-muted">{label}</figcaption>
      <p
        aria-label={label}
        className="max-h-80 overflow-auto rounded-md border bg-ak-surface-2 p-2.5 font-mono text-xs whitespace-pre-wrap break-words text-ak-text"
      >
        {parts.map((p, i) =>
          p.op === "same" ? (
            <span key={i}>{p.text}</span>
          ) : p.op === "del" ? (
            <del key={i} className="text-ak-bad decoration-ak-bad">
              <span aria-hidden="true">−</span>
              <span className="sr-only">Removed: </span>
              {p.text}
            </del>
          ) : (
            <ins key={i} className="font-semibold text-ak-ok no-underline">
              <span aria-hidden="true">+</span>
              <span className="sr-only">Added: </span>
              {p.text}
            </ins>
          ),
        )}
      </p>
    </figure>
  );
}

const statusTag: Record<string, { word: string; tone: "warn" | "neutral" | "bad" }> = {
  new: { word: "New", tone: "neutral" },
  changed: { word: "Changed", tone: "warn" },
  rejected: { word: "Rejected", tone: "neutral" },
  removed: { word: "Removed", tone: "neutral" },
};

const selectClass =
  "h-8 rounded-md border bg-ak-surface px-2 text-(length:--ak-text-sm) text-ak-text outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50";

/**
 * One tool to approve or reject (the mockup's toolReviewCard): its name as
 * agents call it, the description exactly as the model sees it with the
 * server's invisible characters shown, flags, the diff for a changed tool,
 * the class agentkit will use, and its input schema on demand.
 */
export function ToolCard({
  toolset,
  tool: t,
  flags,
  choice,
  onChange,
}: {
  toolset: string;
  tool: ReviewTool;
  /** toolFlags of the tool, worked out once for the review. */
  flags: string[];
  choice: Choice;
  onChange: (c: Choice) => void;
}) {
  const [schema, setSchema] = useState(false);
  const tag = statusTag[t.status];
  const raw = t.current?.description ?? "";
  const shown = t.description ?? "";
  const claims = serverClaims(t.current);
  const changed = t.status === "changed" && t.approved !== undefined;
  // Worked out once per tool, not at each click: a server's text can be long.
  const { approved, current } = t;
  const diffs = useMemo(() => {
    if (!changed) return null;
    const before = schemaText(approved);
    const after = schemaText(current);
    return {
      description: diffTokens(words(visible(approved?.description ?? "")), words(visible(raw))),
      schema: before === after ? null : diffTokens(lines(before), lines(after)),
    };
  }, [changed, approved, current, raw]);
  const shownRaw = useMemo(() => (raw !== shown && !changed ? visible(raw) : ""), [raw, shown, changed]);
  const id = `tool-${t.name}`;
  const decide = (d: "approve" | "reject") => onChange({ ...choice, decision: d });
  return (
    <div
      role="group"
      aria-label={t.name}
      data-tool={t.name}
      className="flex flex-col gap-3 rounded-md border bg-ak-surface p-3"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="flex min-w-0 flex-wrap items-center gap-2">
          <code className="font-mono text-(length:--ak-text-sm) font-medium break-all text-ak-text">
            {toolset}__{t.name}
          </code>
          {tag ? (
            <Status tone={tag.tone} className="text-xs">
              {tag.word}
            </Status>
          ) : null}
        </span>
        <span role="radiogroup" aria-label={`Decision for ${t.name}`} className="flex gap-1.5">
          <Button
            type="button"
            size="sm"
            variant={choice.decision === "approve" ? "default" : "outline"}
            role="radio"
            aria-checked={choice.decision === "approve"}
            onClick={() => decide("approve")}
          >
            <Check aria-hidden="true" />
            Approve
          </Button>
          <Button
            type="button"
            size="sm"
            variant={choice.decision === "reject" ? "default" : "outline"}
            role="radio"
            aria-checked={choice.decision === "reject"}
            onClick={() => decide("reject")}
          >
            <Ban aria-hidden="true" />
            Reject
          </Button>
        </span>
      </div>
      {flags.length > 0 ? (
        <Callout title="Check this one">
          <ul className="list-disc pl-4">
            {flags.map((f) => (
              <li key={f}>{f}</li>
            ))}
          </ul>
        </Callout>
      ) : null}
      {t.current?.title ? (
        <p className="text-(length:--ak-text-sm) text-ak-text-2">
          <span className="text-ak-muted">Title: </span>
          {visible(t.current.title)}
        </p>
      ) : null}
      {diffs ? <Diff label="Description, approved → now" parts={diffs.description} /> : null}
      {diffs?.schema ? <Diff label="Schema, approved → now" parts={diffs.schema} /> : null}
      <div className="flex flex-col gap-1">
        <span className="text-xs font-medium text-ak-muted">Description, exactly as the model sees it</span>
        <p
          data-testid="model-description"
          className="rounded-md border bg-ak-surface-2 p-2.5 text-(length:--ak-text-sm) whitespace-pre-wrap break-words text-ak-text"
        >
          {shown || <span className="text-ak-muted">(none)</span>}
        </p>
        {shownRaw ? (
          <p className="text-xs break-words whitespace-pre-wrap text-ak-muted">
            <span>The server sent: </span>
            <span className="font-mono text-ak-text-2">{shownRaw}</span>
          </p>
        ) : null}
      </div>
      <div className="flex flex-wrap items-center gap-2 text-(length:--ak-text-sm)">
        <span className="text-ak-muted">agentkit treats it as</span>
        <select
          aria-label={`Kind of ${t.name}`}
          value={choice.kind}
          onChange={(e) => onChange({ ...choice, kind: e.target.value })}
          className={selectClass}
        >
          {kinds.map((k) => (
            <option key={k} value={k}>
              {k}
            </option>
          ))}
        </select>
        <select
          aria-label={`Risk of ${t.name}`}
          value={choice.risk}
          onChange={(e) => onChange({ ...choice, risk: e.target.value })}
          className={selectClass}
        >
          {risks.map((r) => (
            <option key={r} value={r}>
              {r} risk
            </option>
          ))}
        </select>
        <span className="text-xs text-ak-muted">
          {claims.length
            ? `The server says ${claims.join(", ")}: a claim, not a check.`
            : "The server makes no claim about it."}
        </span>
      </div>
      <div>
        <Button
          type="button"
          variant="link"
          size="sm"
          className="h-auto px-0"
          aria-expanded={schema}
          aria-controls={`${id}-schema`}
          onClick={() => setSchema(!schema)}
        >
          {schema ? "Hide input schema" : "Show input schema"}
        </Button>
        {schema ? (
          <div id={`${id}-schema`} className="mt-1 flex flex-col gap-2">
            <FieldList list={fields(t.current?.input_schema ?? {})} label="Input" />
            <pre
              aria-label={`${t.name} raw definition`}
              className="max-h-96 overflow-auto rounded-md border bg-ak-surface-2 p-2.5 font-mono text-xs text-ak-text"
            >
              {schemaText(t.current)}
            </pre>
          </div>
        ) : null}
      </div>
    </div>
  );
}

/** The cards of a review: the tools to decide on, then a bulk approve for those without flags. */
export function ReviewCards({
  ts,
  choices,
  setChoices,
  only,
}: {
  ts: ServerToolset;
  choices: Choices;
  setChoices: (c: Choices) => void;
  only: ReviewTool[];
}) {
  const flags = useMemo(() => reviewFlags(ts.tools), [ts.tools]);
  const flagsOf = (t: ReviewTool) => flags.get(t.name) ?? [];
  const quick = only.filter((t) => !choices[t.name]?.decision && flagsOf(t).length === 0);
  return (
    <>
      {quick.length > 0 ? (
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="self-start"
          onClick={() => {
            const next = { ...choices };
            for (const t of quick)
              next[t.name] = { ...(next[t.name] ?? { kind: "write", risk: "medium" }), decision: "approve" };
            setChoices(next);
          }}
        >
          <Check aria-hidden="true" />
          Approve the {quick.length} without flags
        </Button>
      ) : null}
      {only.map((t) => (
        <ToolCard
          key={t.name}
          toolset={ts.name}
          tool={t}
          flags={flagsOf(t)}
          choice={choices[t.name] ?? { kind: "write", risk: "medium" }}
          onChange={(c) => setChoices({ ...choices, [t.name]: c })}
        />
      ))}
    </>
  );
}

export function ErrorLine({ children }: { children: ReactNode }) {
  return (
    <p role="alert" className="flex items-start gap-1.5 text-(length:--ak-text-sm) text-ak-bad">
      <CircleAlert aria-hidden="true" className="mt-0.5 size-4 flex-none" />
      <span>{children}</span>
    </p>
  );
}

function ReviewBody({
  ts,
  reload,
  error,
  setError,
}: {
  ts: ServerToolset;
  reload: () => void;
  error: string;
  setError: (e: string) => void;
}) {
  const { client } = useApi();
  const { reloadToolsets } = useLists();
  const navigate = useNavigate();
  const [choices, setChoices] = useState<Choices>(() => initialChoices(ts.tools));
  const [busy, setBusy] = useState(false);
  const asked = ts.tools.filter(needsDecision);
  const rejected = ts.tools.filter((t) => t.status === "rejected");
  const removed = ts.tools.filter((t) => t.status === "removed");
  const approved = ts.tools.filter((t) => t.status === "approved");
  const left = undecided(ts.tools, choices).length;
  const save = () => {
    const ds = decisions(ts.tools, choices);
    if (ds.length === 0) return;
    setBusy(true);
    setError("");
    client.reviewToolset(ts.name, ds).then(
      () => {
        reloadToolsets();
        void navigate(`/toolsets/${encodeURIComponent(ts.name)}`, { state: { reviewed: true } });
      },
      (e: unknown) => {
        setBusy(false);
        setError(reviewErrorText(e));
        if (e instanceof ApiError && e.status === 409) reload();
      },
    );
  };
  const what = changeWords(ts.tools);
  return (
    <>
      {asked.length > 0 ? (
        <Callout icon={ShieldAlert} title={`${ts.name} ${what} since they were approved.`}>
          {asked.length === 1 ? "That tool is" : "Those tools are"} off until you decide. Its other tools keep working.
        </Callout>
      ) : (
        <p className="text-(length:--ak-text-sm) text-ak-muted">Nothing new or changed to review.</p>
      )}
      {error ? <ErrorLine>{error}</ErrorLine> : null}
      <ReviewCards ts={ts} choices={choices} setChoices={setChoices} only={asked} />
      {rejected.length > 0 ? (
        <details className="rounded-md border px-3 py-2">
          <summary className="cursor-pointer text-(length:--ak-text-sm) text-ak-text-2">
            {rejected.length} rejected: approve one to turn it on
          </summary>
          <div className="mt-3 flex flex-col gap-3">
            <ReviewCards ts={ts} choices={choices} setChoices={setChoices} only={rejected} />
          </div>
        </details>
      ) : null}
      {removed.length > 0 ? (
        <Section title="Removed by the server">
          <ul className="flex flex-col gap-1.5 text-(length:--ak-text-sm)">
            {removed.map((t) => (
              <li key={t.name} className="flex flex-wrap items-center gap-2">
                <code className="font-mono">{t.name}</code>
                <span className="text-ak-muted">It no longer reaches the model.</span>
                <Button
                  type="button"
                  variant="outline"
                  size="xs"
                  aria-pressed={choices[t.name]?.decision === "reject"}
                  onClick={() =>
                    setChoices({
                      ...choices,
                      [t.name]: {
                        ...(choices[t.name] ?? { kind: "write", risk: "medium" }),
                        decision: choices[t.name]?.decision === "reject" ? undefined : "reject",
                      },
                    })
                  }
                >
                  {choices[t.name]?.decision === "reject" ? "Will be forgotten" : "Forget it"}
                </Button>
              </li>
            ))}
          </ul>
        </Section>
      ) : null}
      {approved.length > 0 ? (
        <details className="rounded-md border px-3 py-2">
          <summary className="cursor-pointer text-(length:--ak-text-sm) text-ak-text-2">
            {approved.length} approved and unchanged
          </summary>
          <ul className="mt-2 flex flex-col gap-1 text-(length:--ak-text-sm)">
            {approved.map((t) => (
              <li key={t.name} className="flex flex-col">
                <code className="font-mono">{t.name}</code>
                <span className="break-words text-ak-muted">{t.description}</span>
              </li>
            ))}
          </ul>
        </details>
      ) : null}
      <div className="flex flex-wrap items-center gap-2">
        <Button type="button" disabled={busy || left > 0 || decisions(ts.tools, choices).length === 0} onClick={save}>
          {busy ? "Saving…" : "Save review"}
        </Button>
        <Button asChild variant="outline">
          <Link to={`/toolsets/${encodeURIComponent(ts.name)}`}>Cancel</Link>
        </Button>
        {left > 0 ? <span className="text-xs text-ak-muted">{left} still to decide</span> : null}
      </div>
    </>
  );
}

/** /toolsets/{name}/review: a server's new and changed tools, to approve or keep off. */
export function ReviewPage() {
  const { name = "" } = useParams();
  const [n, setN] = useState(0);
  const [error, setError] = useState("");
  const res = useResource((c, signal) => c.getToolsetReview(name, { signal }), `review\n${name}\n${n}`);
  if (useForbidden(res)) return <Forbidden />;
  return (
    <Page
      title={`Review ${name}`}
      back={{ to: `/toolsets/${encodeURIComponent(name)}`, label: name }}
      aside={
        res.state === "ready" && res.data.status === "review_needed" ? (
          <Status tone="warn" className="text-(length:--ak-text-sm)">
            Review needed
          </Status>
        ) : undefined
      }
    >
      <Loaded res={res} what="the review">
        {(ts) => (
          // Keyed: a fresh read (after a conflict) starts the choices afresh.
          <div key={`${ts.version}\n${n}`} className="contents">
            <ReviewBody ts={ts} reload={() => setN((x) => x + 1)} error={error} setError={setError} />
          </div>
        )}
      </Loaded>
    </Page>
  );
}
