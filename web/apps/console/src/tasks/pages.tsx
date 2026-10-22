// The Tasks pages (docs/UI.md "Tasks", T074): the section's own page
// (/tasks, /tasks/threads: the list on a phone, a pointer to it on a
// wider screen), a run (/tasks/{id}: what it is, Cancel, and its
// transcript, replayed and followed live from its event stream), and a
// thread between agents (/tasks/threads/{id}: who, hops, tokens, and the
// messages in order, each linked to the runs that wrote and handled it),
// and the triggers (/tasks/triggers, admin: when each fires next, how
// the last firing went, Pause and Resume).

import { ApiError, type Firing, type Run, type Thread, type Trigger } from "@agentkit/agent-ui/api";
import { Status } from "@agentkit/ui";
import { Button } from "@agentkit/ui/components/button";
import { Copy, Info, ListChecks, MessagesSquare } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { Link, useLocation, useParams } from "react-router";
import { useApi } from "../api";
import { Notice, Thread as Transcript } from "../chat/thread";
import { useLayout } from "../layout";
import { Empty, Loaded, Page } from "../pages/page";
import { useTasks, useTaskStore } from "./use-tasks";
import { follow, startView, type RunView } from "./follow";
import { TasksList } from "./list";

export { TasksList };
import {
  agentName,
  confirmCancel,
  participants,
  runElapsed,
  segmentOf,
  startedText,
  threadStatus,
  tokensText,
  useNow,
  usePolled,
  writtenBy,
} from "./shared";
import { runStatus } from "./store";

const noDisplays = new Map();

/** /tasks and /tasks/threads. */
export function TasksPage() {
  const { phone } = useLayout();
  const threads = segmentOf(useLocation().pathname) === "threads";
  if (phone) {
    return (
      <Page title={threads ? "Threads" : "Tasks"} heading="body" body="bare">
        <TasksList heading={false} />
      </Page>
    );
  }
  return (
    <Page title={threads ? "Threads" : "Tasks"} heading="body" body="bare">
      <Empty icon={threads ? MessagesSquare : ListChecks} title={threads ? "Threads between agents" : "Tasks"}>
        <p className="text-ak-muted">
          {threads
            ? "When an agent messages another, their thread shows in the list. Pick one to read it."
            : "Every run that isn't a chat is a task: started with POST /v1/tasks, by a trigger, or by another agent. Pick one to watch it."}
        </p>
      </Empty>
    </Page>
  );
}

function KeyValues({ rows }: { rows: [string, ReactNode][] }) {
  return (
    <dl className="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-6 gap-y-1.5 text-(length:--ak-text-sm)">
      {rows.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="text-ak-muted">{k}</dt>
          <dd className="min-w-0 break-words text-ak-text">{v}</dd>
        </div>
      ))}
    </dl>
  );
}

function Tag({ children }: { children: ReactNode }) {
  return (
    <span className="inline-flex h-6 items-center rounded-sm border px-1.5 font-mono text-xs text-ak-text">
      {children}
    </span>
  );
}

const back = { to: "/tasks", label: "tasks" };

/** Follows the run's events while the page is open. */
function useRunView(id: string): RunView {
  const { client, unauthorized } = useApi();
  const [got, setGot] = useState<{ id: string; view: RunView } | null>(null);
  useEffect(() => {
    const ctl = new AbortController();
    void follow(client, id, (view) => setGot({ id, view }), { signal: ctl.signal, unauthorized });
    return () => ctl.abort();
  }, [client, id, unauthorized]);
  return got?.id === id ? got.view : startView();
}

/** /tasks/{id}. */
export function RunPage() {
  const { id = "" } = useParams();
  const store = useTaskStore();
  const { runs } = useTasks();
  const view = useRunView(id);
  // The header's numbers: GET /v1/runs/{id}, again while the run goes on.
  const listed = runs?.find((r) => r.id === id);
  const [settled, setSettled] = useState(false);
  const res = usePolled((c, signal) => c.getRun(id, { signal }), `run ${id}`, settled ? 0 : 3000);
  const [cancelError, setCancelError] = useState("");
  const [canceling, setCanceling] = useState(false);

  useEffect(() => {
    store?.view(id);
    return () => store?.view(undefined);
  }, [store, id]);

  const run: Run | undefined = res.state === "ready" ? res.data : listed;
  const status = run ? runStatus(run.status) : undefined;
  // Once the stream and the run have both ended, the numbers are final: stop asking.
  const over = view.ended && status !== undefined && !status.active;
  if (over !== settled) setSettled(over);
  const now = useNow(status?.active === true);

  if (!run) {
    return (
      <Page title="Task" mono back={back}>
        {res.state === "failed" && res.error instanceof ApiError && res.error.status === 404 ? (
          <Empty icon={Info} title="No such task">
            <p className="text-ak-muted">It was deleted, or it belongs to another tenant.</p>
          </Empty>
        ) : (
          <Loaded res={res} what="the task">
            {() => null}
          </Loaded>
        )}
      </Page>
    );
  }

  const cancel = () => {
    if (!store || !confirmCancel(run.agent)) return;
    setCanceling(true);
    setCancelError("");
    store.cancel(run.id).then(
      () => setCanceling(false),
      (e: unknown) => {
        setCanceling(false);
        setCancelError(e instanceof Error ? e.message : String(e));
      },
    );
  };
  const title = view.prompt?.text.split("\n")[0]?.trim() || `Task of ${run.agent}`;
  const result = run.status === "done" ? (run.answer ?? "") : "";
  const turns = view.prompt ? [view.prompt, view.answer] : [view.answer];
  return (
    <Page
      title={title}
      back={back}
      wrap
      body="bare"
      aside={
        <>
          <Status tone={status?.tone ?? "neutral"} className="text-(length:--ak-text-sm)">
            {status?.word}
          </Status>
          {status?.active ? (
            <Button variant="outline" size="sm" disabled={canceling} onClick={cancel}>
              Cancel…
            </Button>
          ) : null}
        </>
      }
    >
      <Transcript
        turns={turns}
        label={`Transcript of ${title}`}
        agent={run.agent}
        displays={noDisplays}
        actions={{}}
        className="px-4 py-5 md:px-6"
        after={
          view.problem && !view.ended ? (
            <Notice tone={view.gone ? "bad" : "info"} role="status">
              {view.gone ? "Couldn't follow this task." : "Reconnecting to the task…"}
            </Notice>
          ) : null
        }
      >
        <section aria-label="About this task" className="flex flex-col gap-3 border-b pb-4">
          <KeyValues
            rows={[
              [
                "Agent",
                <span key="a" className="flex flex-wrap items-center gap-1.5">
                  <Tag>{run.agent}</Tag>
                  {run.agent_version ? <span className="text-ak-muted">v{run.agent_version}</span> : null}
                </span>,
              ],
              ["How it runs", run.background === false ? "Chat" : "Autonomous: never waits for a person"],
              ["Started", startedText(run.created_at, new Date(now))],
              [status?.active ? "Time so far" : "Time", runElapsed(run, now)],
              [status?.active ? "Tokens so far" : "Tokens", tokensText(run) || "None yet"],
              [
                "Run",
                <span key="r" className="font-mono">
                  {run.id}
                </span>,
              ],
            ]}
          />
          {cancelError ? (
            <Notice tone="bad" role="alert">
              Couldn&apos;t cancel the task: {cancelError}
            </Notice>
          ) : null}
          {result ? (
            <section aria-labelledby="task-result" className="flex flex-col gap-2">
              <div className="flex items-center gap-2">
                <h2 id="task-result" className="flex-1 text-(length:--ak-text-md) font-semibold text-ak-text">
                  Result
                </h2>
                <Button variant="ghost" size="sm" onClick={() => void navigator.clipboard?.writeText(result)}>
                  <Copy aria-hidden="true" className="size-3.5" />
                  Copy
                </Button>
              </div>
              <p className="break-words whitespace-pre-wrap text-ak-text">{result}</p>
            </section>
          ) : null}
        </section>
      </Transcript>
    </Page>
  );
}

function RunLink({ id, children }: { id: string; children: ReactNode }) {
  return (
    <Link
      to={`/tasks/${encodeURIComponent(id)}`}
      className="font-mono text-ak-text-2 underline-offset-4 hover:underline"
    >
      {children}
    </Link>
  );
}

/** "asked worker", "replied", "notice": what a message is, after its sender. */
function verbOf(kind: string, to: string): string {
  if (kind === "request") return `asked ${to}`;
  if (kind === "reply") return `replied to ${to}`;
  if (kind === "notice") return `notice to ${to}`;
  return `${kind} to ${to}`;
}

function Messages({ t }: { t: Thread }) {
  const ms = t.messages ?? [];
  if (ms.length === 0) return <p className="text-ak-muted">No messages yet.</p>;
  return (
    <ol aria-label="Messages" className="flex flex-col gap-3">
      {ms.map((m) => {
        const wrote = writtenBy(m, ms);
        return (
          <li
            key={m.id}
            data-hop={m.hop}
            className="flex flex-col gap-1.5 rounded-md border bg-ak-surface px-3.5 py-3 text-(length:--ak-text-sm)"
          >
            <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-ak-muted">
              <Tag>{agentName(m.from)}</Tag>
              <span>{verbOf(m.kind, agentName(m.to))}</span>
              {m.state !== "done" ? <span>· {m.state}</span> : null}
            </div>
            <p className="break-words whitespace-pre-wrap text-ak-text">{m.text}</p>
            <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-ak-muted">
              {wrote ? (
                <span>
                  Written in <RunLink id={wrote}>{wrote}</RunLink>
                </span>
              ) : null}
              {m.run_id ? (
                <span>
                  Handled in <RunLink id={m.run_id}>{m.run_id}</RunLink>
                </span>
              ) : null}
            </div>
          </li>
        );
      })}
    </ol>
  );
}

/** /tasks/threads/{id}. */
export function ThreadPage() {
  const { id = "" } = useParams();
  const res = usePolled((c, signal) => c.getThread(id, { signal }), `thread ${id}`, 3000);
  const backToThreads = { to: "/tasks/threads", label: "threads" };
  if (res.state !== "ready") {
    return (
      <Page title="Thread" mono back={backToThreads}>
        {res.state === "failed" && res.error instanceof ApiError && res.error.status === 404 ? (
          <Empty icon={Info} title="No such thread">
            <p className="text-ak-muted">It isn&apos;t in this tenant.</p>
          </Empty>
        ) : (
          <Loaded res={res} what="the thread">
            {() => null}
          </Loaded>
        )}
      </Page>
    );
  }
  const t = res.data;
  const who = participants(t.messages ?? []);
  const s = threadStatus(t);
  return (
    <Page
      title={who.length > 0 ? who.join(" ⇄ ") : t.id}
      mono
      back={backToThreads}
      aside={
        <Status tone={s.tone} className="text-(length:--ak-text-sm)">
          {s.word}
        </Status>
      }
    >
      <KeyValues
        rows={[
          [
            "Between",
            <span key="b" className="flex flex-wrap gap-1.5">
              {who.map((a) => (
                <Tag key={a}>{a}</Tag>
              ))}
            </span>,
          ],
          ["Hops", String(t.hops)],
          ["Tokens", t.tokens.toLocaleString("en-US")],
          [
            "Thread",
            <span key="t" className="font-mono">
              {t.id}
            </span>,
          ],
        ]}
      />
      {t.status === "stopped" && t.error ? (
        <Notice tone="bad">
          Stopped: {t.error.message || t.error.code} ({t.error.code})
        </Notice>
      ) : null}
      <section aria-labelledby="thread-messages" className="flex flex-col gap-3">
        <h2 id="thread-messages" className="text-(length:--ak-text-md) font-semibold text-ak-text">
          Messages
        </h2>
        <Messages t={t} />
      </section>
      <p className="text-xs text-ak-muted">
        A message from another agent reaches its recipient as data from that agent, never as instructions.
      </p>
    </Page>
  );
}

function firingText(f: Firing | undefined): { tone: "ok" | "warn" | "bad" | "neutral"; word: string } {
  if (!f) return { tone: "neutral", word: "Never fired" };
  if (f.state === "started") return { tone: "ok", word: "Started a run" };
  if (f.state === "pending") return { tone: "neutral", word: "Starting" };
  if (f.state === "skipped") return { tone: "warn", word: `Skipped${f.reason ? `: ${f.reason}` : ""}` };
  return { tone: "bad", word: `Failed${f.reason ? `: ${f.reason}` : ""}` };
}

function TriggerRow({ t, onChange }: { t: Trigger; onChange: (t: Trigger) => void }) {
  const { client, unauthorized } = useApi();
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState("");
  const last = firingText(t.last_firing);
  const next = t.paused
    ? "Paused"
    : t.next_fire_at
      ? startedText(t.next_fire_at, new Date())
      : t.schedule
        ? "Not scheduled"
        : "On request";
  const toggle = () => {
    setBusy(true);
    setFailed("");
    client.setTriggerPaused(t.id, !t.paused).then(
      (got) => {
        setBusy(false);
        onChange(got);
      },
      (e: unknown) => {
        setBusy(false);
        if (e instanceof ApiError && e.status === 401) unauthorized();
        setFailed(e instanceof Error ? e.message : String(e));
      },
    );
  };
  return (
    <tr data-trigger={t.id} className="border-t align-top">
      <td className="py-2 pr-4">
        <div className="font-mono text-ak-text">{t.id}</div>
        <div className="text-xs text-ak-muted">
          {t.schedule ? "Schedule" : "Webhook"}
          {t.static ? " · from the config" : ""}
        </div>
      </td>
      <td className="py-2 pr-4">{t.agent ? <Tag>{t.agent}</Tag> : <span className="text-ak-muted">default</span>}</td>
      <td className="py-2 pr-4 font-mono text-xs break-all">{t.schedule ?? t.webhook?.path ?? ""}</td>
      <td className="py-2 pr-4">{next}</td>
      <td className="py-2 pr-4">
        <Status tone={last.tone}>{last.word}</Status>
        {t.last_firing?.run_id ? (
          <div className="text-xs">
            <RunLink id={t.last_firing.run_id}>{t.last_firing.run_id}</RunLink>
          </div>
        ) : null}
      </td>
      <td className="py-2">
        <Button
          variant="outline"
          size="sm"
          disabled={busy}
          onClick={toggle}
          aria-label={`${t.paused ? "Resume" : "Pause"} ${t.id}`}
        >
          {t.paused ? "Resume" : "Pause"}
        </Button>
        {failed ? (
          <p role="alert" className="pt-1 text-xs text-ak-bad">
            {failed}
          </p>
        ) : null}
      </td>
    </tr>
  );
}

/** /tasks/triggers (admin; the API answers 403 to others). */
export function TriggersPage() {
  const { phone } = useLayout();
  const res = usePolled((c, signal) => c.listTriggers({ signal }), "triggers", 10_000);
  const [changed, setChanged] = useState<ReadonlyMap<string, Trigger>>(new Map());
  const forbidden = res.state === "failed" && res.error instanceof ApiError && res.error.status === 403;
  return (
    <Page title="Triggers">
      {phone ? (
        <div className="-mx-4 -mt-4">
          <TasksList heading={false} />
        </div>
      ) : null}
      {forbidden ? (
        <p className="text-ak-muted">Only admins see the triggers.</p>
      ) : (
        <Loaded res={res} what="triggers">
          {(list) => {
            const shown = list.map((t) => {
              const c = changed.get(t.id);
              return c && c.version >= t.version ? c : t;
            });
            if (shown.length === 0)
              return (
                <p className="text-ak-muted">
                  No triggers. Add one with POST /v1/triggers or an agent&apos;s triggers in the config.
                </p>
              );
            return (
              <div className="overflow-x-auto">
                <table aria-label="Triggers" className="w-full text-left text-(length:--ak-text-sm)">
                  <thead className="text-xs text-ak-muted">
                    <tr>
                      {["Trigger", "Agent", "When", "Next", "Last outcome", ""].map((h) => (
                        <th key={h} scope="col" className="pr-4 pb-2 font-normal">
                          {h}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {shown.map((t) => (
                      <TriggerRow key={t.id} t={t} onChange={(got) => setChanged((m) => new Map(m).set(got.id, got))} />
                    ))}
                  </tbody>
                </table>
              </div>
            );
          }}
        </Loaded>
      )}
    </Page>
  );
}
