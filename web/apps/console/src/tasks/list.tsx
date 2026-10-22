// The Tasks list (docs/UI.md "Tasks"): three tabs, Tasks (the background
// runs the shell watches, grouped Waiting for you, Running and paused,
// Finished, with an agent filter, marks and Cancel…), Threads (agent-to-agent
// threads: who, hops, the last message) and Triggers (admins; the table is
// the page). It is the list column on a laptop or desktop and the page on a
// phone.

import type { Run, Thread } from "@agentkit/agent-ui/api";
import { Status } from "@agentkit/ui";
import { Button } from "@agentkit/ui/components/button";
import { Skeleton } from "@agentkit/ui/components/skeleton";
import { Spinner } from "@agentkit/ui/components/spinner";
import { cn } from "@agentkit/ui/lib/utils";
import { X } from "lucide-react";
import { useState, type ReactNode } from "react";
import { Link, useLocation } from "react-router";
import { useApi } from "../api";
import { useTasks, useTaskStore } from "./use-tasks";
import {
  agentName,
  confirmCancel,
  participants,
  segmentOf,
  selectedOf,
  startedText,
  threadStatus,
  threadsWithMessages,
  tokensText,
  usePolled,
} from "./shared";
import { mayAdmin } from "../sections";
import { groupOf, runStatus, type Group } from "./store";

const groups: [Group, string][] = [
  ["waiting", "Waiting for you"],
  ["running", "Running and paused"],
  ["finished", "Finished"],
];

const rowClass =
  "flex min-h-10 min-w-0 flex-1 items-center gap-2 rounded-md px-2 py-1.5 text-ak-text-2 hover:bg-ak-hover hover:text-ak-text aria-[current=page]:bg-ak-select aria-[current=page]:text-ak-text max-md:min-h-11";

function Skeletons() {
  return (
    <div aria-busy="true" className="flex flex-col gap-3 px-2 py-2">
      <span className="sr-only">Loading</span>
      {["w-3/5", "w-1/2", "w-2/3"].map((w) => (
        <div key={w} className="flex flex-col gap-1.5">
          <Skeleton className={cn("h-3", w)} />
          <Skeleton className="h-2.5 w-1/3" />
        </div>
      ))}
    </div>
  );
}

function Muted({ children }: { children: ReactNode }) {
  return <p className="px-2 py-2 text-(length:--ak-text-sm) text-ak-muted">{children}</p>;
}

/** The row's mark: a spinner while running, amber while waiting, a dot on a finished task not opened yet. */
function Mark({ run, unread }: { run: Run; unread: boolean }) {
  const g = groupOf(run.status);
  if (run.status === "running") {
    return (
      <span className="flex flex-none items-center">
        <Spinner className="size-3" />
      </span>
    );
  }
  if (g === "waiting" || g === "running") {
    return <span aria-hidden="true" className="size-2 flex-none rounded-full bg-ak-warn" />;
  }
  if (unread) {
    return (
      <span className="flex flex-none items-center">
        <span aria-hidden="true" className="size-2 rounded-full bg-ak-text" />
        <span className="sr-only">Not opened yet</span>
      </span>
    );
  }
  return null;
}

function RunRow({ run, current, unread }: { run: Run; current: boolean; unread: boolean }) {
  const store = useTaskStore();
  const s = runStatus(run.status);
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState("");
  const sub = [startedText(run.created_at, new Date()), tokensText(run)].filter(Boolean).join(" · ");
  const cancel = () => {
    if (!store || !confirmCancel(run.agent)) return;
    setBusy(true);
    setFailed("");
    store.cancel(run.id).then(
      () => setBusy(false),
      (e: unknown) => {
        setBusy(false);
        setFailed(e instanceof Error ? e.message : String(e));
      },
    );
  };
  return (
    <li data-run={run.id} className="flex flex-col">
      <div className="flex items-center gap-0.5">
        <Link
          to={`/tasks/${encodeURIComponent(run.id)}`}
          aria-current={current ? "page" : undefined}
          className={rowClass}
        >
          <span className="flex min-w-0 flex-1 flex-col">
            <span className="truncate font-mono text-(length:--ak-text-sm)">{run.agent}</span>
            <span className="truncate text-xs text-ak-muted">
              {s.word}
              {sub ? ` · ${sub}` : ""}
            </span>
          </span>
          <Mark run={run} unread={unread} />
        </Link>
        {s.active ? (
          <button
            type="button"
            aria-label={`Cancel task of ${run.agent}`}
            disabled={busy}
            onClick={cancel}
            className="grid size-8 flex-none place-items-center rounded-md text-ak-muted hover:bg-ak-hover hover:text-ak-text disabled:opacity-50"
          >
            <X aria-hidden="true" className="size-4" />
          </button>
        ) : null}
      </div>
      {failed ? (
        <p role="alert" className="px-2 pb-1 text-xs text-ak-bad">
          Couldn&apos;t cancel: {failed}
        </p>
      ) : null}
    </li>
  );
}

function Runs({ selected }: { selected: string }) {
  const store = useTaskStore();
  const { runs, error, unread } = useTasks();
  const [agent, setAgent] = useState("");
  let body: ReactNode;
  if (runs === null && error) {
    body = (
      <div className="flex items-center gap-2 px-2 py-2 text-(length:--ak-text-sm) text-ak-muted">
        Couldn&apos;t load tasks.
        <Button variant="outline" size="sm" onClick={() => void store?.refresh()}>
          Retry
        </Button>
      </div>
    );
  } else if (runs === null) {
    body = <Skeletons />;
  } else {
    const shown = runs.filter((r) => !agent || r.agent === agent);
    body =
      shown.length === 0 ? (
        <Muted>{runs.length === 0 ? "No tasks yet." : "No tasks of this agent."}</Muted>
      ) : (
        groups.map(([g, label]) => {
          const rows = shown.filter((r) => groupOf(r.status) === g);
          if (rows.length === 0) return null;
          return (
            <section key={g} aria-label={label} className="flex flex-col gap-0.5 pb-2">
              <h3 className="px-2 pt-1 pb-0.5 text-xs font-medium text-ak-muted">{label}</h3>
              <ul className="flex flex-col gap-0.5">
                {rows.map((r) => (
                  <RunRow key={r.id} run={r} current={r.id === selected} unread={unread.has(r.id)} />
                ))}
              </ul>
            </section>
          );
        })
      );
  }
  const agents = [...new Set((runs ?? []).map((r) => r.agent))].sort();
  return (
    <>
      <div className="px-3 pb-2">
        <select
          aria-label="Agent"
          value={agent}
          onChange={(e) => setAgent(e.target.value)}
          className="h-8 w-full rounded-md border bg-ak-surface px-2 text-(length:--ak-text-sm) text-ak-text"
        >
          <option value="">All agents</option>
          {agents.map((a) => (
            <option key={a} value={a}>
              {a}
            </option>
          ))}
        </select>
      </div>
      <nav aria-label="Tasks" className="min-h-0 flex-1 overflow-auto px-2 pb-3">
        {body}
      </nav>
    </>
  );
}

function ThreadRow({ t, current }: { t: Thread; current: boolean }) {
  const ms = t.messages ?? [];
  const who = participants(ms);
  const last = ms[ms.length - 1];
  const s = threadStatus(t);
  return (
    <li>
      <Link
        to={`/tasks/threads/${encodeURIComponent(t.id)}`}
        aria-current={current ? "page" : undefined}
        className={rowClass}
      >
        <span className="flex min-w-0 flex-1 flex-col">
          <span className="truncate font-mono text-(length:--ak-text-sm)">
            {who.length > 0 ? who.join(" ⇄ ") : t.id}
          </span>
          <span className="truncate text-xs text-ak-muted">
            {t.hops} {t.hops === 1 ? "hop" : "hops"}
            {last ? ` · ${agentName(last.from)}: ${last.text}` : ""}
          </span>
        </span>
        <Status tone={s.tone} hideLabel>
          {s.word}
        </Status>
      </Link>
    </li>
  );
}

function Threads({ selected }: { selected: string }) {
  const res = usePolled(threadsWithMessages, "threads", 5000);
  let body: ReactNode;
  if (res.state === "loading") body = <Skeletons />;
  else if (res.state === "failed") {
    body = (
      <div className="flex items-center gap-2 px-2 py-2 text-(length:--ak-text-sm) text-ak-muted">
        Couldn&apos;t load threads.
        <Button variant="outline" size="sm" onClick={res.retry}>
          Retry
        </Button>
      </div>
    );
  } else if (res.data.length === 0) body = <Muted>No agent has messaged another yet.</Muted>;
  else {
    body = (
      <ul className="flex flex-col gap-0.5">
        {res.data.map((t) => (
          <ThreadRow key={t.id} t={t} current={t.id === selected} />
        ))}
      </ul>
    );
  }
  return (
    <nav aria-label="Threads" className="min-h-0 flex-1 overflow-auto px-2 pb-3">
      {body}
    </nav>
  );
}

const tab =
  "flex h-7 flex-1 items-center justify-center rounded-sm text-(length:--ak-text-sm) text-ak-muted hover:text-ak-text aria-[current=page]:bg-ak-surface aria-[current=page]:text-ak-text aria-[current=page]:shadow-xs";

/** The Tasks list: its head (title, tabs) and the tab's rows. */
export function TasksList({ heading = true }: { heading?: boolean }) {
  const { pathname } = useLocation();
  const segment = segmentOf(pathname);
  const selected = selectedOf(pathname);
  const { me } = useApi();
  let body: ReactNode;
  if (segment === "tasks") body = <Runs selected={selected} />;
  else if (segment === "threads") body = <Threads selected={selected} />;
  else
    body = (
      <Muted>Schedules and inbound webhooks that start background runs. They are in the table beside this list.</Muted>
    );
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex flex-col gap-2 px-3 pt-3 pb-2">
        {heading ? (
          <div className="flex min-h-7 items-center justify-between gap-2 px-1">
            <h2 className="text-(length:--ak-text-md) font-semibold text-ak-text">Tasks</h2>
          </div>
        ) : null}
        <nav aria-label="Tabs" className="flex gap-0.5 rounded-md bg-ak-hover p-0.5">
          <Link to="/tasks" aria-current={segment === "tasks" ? "page" : undefined} className={tab}>
            Tasks
          </Link>
          <Link to="/tasks/threads" aria-current={segment === "threads" ? "page" : undefined} className={tab}>
            Threads
          </Link>
          {mayAdmin(me) ? (
            <Link to="/tasks/triggers" aria-current={segment === "triggers" ? "page" : undefined} className={tab}>
              Triggers
            </Link>
          ) : null}
        </nav>
      </div>
      {body}
    </div>
  );
}
