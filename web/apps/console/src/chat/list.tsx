// The Chats list (docs/UI.md "Conversations have one home"): New chat,
// search over the loaded rows, an agent filter, date groups, status marks and
// Load more by cursor. It is part of the shell, so it stays small.

import type { ConversationSummary } from "@agentkit/agent-ui/api";
import { Button } from "@agentkit/ui/components/button";
import { Skeleton } from "@agentkit/ui/components/skeleton";
import { Spinner } from "@agentkit/ui/components/spinner";
import { cn } from "@agentkit/ui/lib/utils";
import { MessageCircle, Search } from "lucide-react";
import { useState, useSyncExternalStore, type ReactNode } from "react";
import { Link, useLocation } from "react-router";
import { useApi, useResource } from "../api";
import { modHint, modKey } from "../layout";
import { listAgents } from "../lists";
import { dateGroup, searchId } from "./shared";
import { useRuns } from "./use-runs";

const pageSize = 50;

function shortTime(iso: string, group: string): string {
  const t = new Date(iso);
  if (Number.isNaN(t.getTime())) return "";
  return group === "Today" || group === "Yesterday"
    ? t.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })
    : t.toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

function Mark({ running, interrupted, unread }: { running: boolean; interrupted: boolean; unread: boolean }) {
  if (running) {
    return (
      <span className="flex flex-none items-center">
        <Spinner className="size-3" />
        <span className="sr-only">Answering</span>
      </span>
    );
  }
  if (unread || interrupted) {
    return (
      <span className="flex flex-none items-center">
        <span aria-hidden="true" className={cn("size-2 rounded-full", interrupted ? "bg-ak-warn" : "bg-ak-text")} />
        <span className="sr-only">{interrupted ? "Interrupted" : "New answer"}</span>
      </span>
    );
  }
  return null;
}

function Row({ c, group, current }: { c: ConversationSummary; group: string; current: boolean }) {
  const runs = useRuns();
  const s = useSyncExternalStore(runs.subscribe, () => runs.get(c.id));
  const running = s.live?.answer.state === "streaming";
  return (
    <li>
      <Link
        to={`/c/${encodeURIComponent(c.id)}`}
        aria-current={current ? "page" : undefined}
        className="flex min-h-10 items-center gap-2 rounded-md px-2 py-1.5 text-ak-text-2 hover:bg-ak-hover hover:text-ak-text aria-[current=page]:bg-ak-select aria-[current=page]:text-ak-text max-md:min-h-11"
      >
        <span className="flex min-w-0 flex-1 flex-col">
          <span className="truncate text-(length:--ak-text-sm)">{c.title || "New chat"}</span>
          <span className="flex gap-2 truncate text-xs text-ak-muted">
            <span className="font-mono">{c.agent}</span>
            <span>{shortTime(c.updated_at, group)}</span>
          </span>
        </span>
        <Mark running={running} interrupted={!running && c.status === "interrupted"} unread={s.unread} />
      </Link>
    </li>
  );
}

const field =
  "h-8 w-full min-w-0 rounded-md border border-input bg-ak-surface px-2 text-(length:--ak-text-sm) text-ak-text outline-none placeholder:text-ak-muted focus-visible:border-ak-line-strong";

export function ChatsList({ head }: { head: (extra: ReactNode) => ReactNode }) {
  const { client, unauthorized } = useApi();
  const runs = useRuns();
  const version = useSyncExternalStore(runs.subscribe, runs.listVersion);
  const { pathname } = useLocation();
  const [agent, setAgent] = useState("");
  const [query, setQuery] = useState("");
  const agents = useResource(listAgents, "agents");
  const first = useResource(
    (c, signal) => c.listConversations({ limit: pageSize, ...(agent ? { agent } : {}) }, { signal }),
    `conversations:${agent}:${version}`,
  );
  // Older pages, for the first page they follow.
  const [older, setOlder] = useState<{ after: unknown; rows: ConversationSummary[]; next: string | undefined }>();
  const [loadingMore, setLoadingMore] = useState(false);
  const firstData = first.state === "ready" ? first.data : undefined;
  const more = older?.after === firstData ? older : undefined;

  async function loadMore() {
    const cursor = more ? more.next : firstData?.nextCursor;
    if (!cursor || !firstData) return;
    setLoadingMore(true);
    try {
      const page = await client.listConversations({ limit: pageSize, cursor, ...(agent ? { agent } : {}) });
      setOlder({ after: firstData, rows: [...(more?.rows ?? []), ...page.conversations], next: page.nextCursor });
    } catch (e) {
      if (e instanceof Error && "status" in e && e.status === 401) unauthorized();
    } finally {
      setLoadingMore(false);
    }
  }

  const current = decodeURIComponent(/^\/c\/([^/]+)/.exec(pathname)?.[1] ?? "");
  const next = more ? more.next : firstData?.nextCursor;
  const q = query.trim().toLowerCase();
  let body: ReactNode;
  if (first.state === "loading") {
    body = (
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
  } else if (first.state === "failed") {
    body = (
      <div className="flex items-center gap-2 px-2 py-2 text-(length:--ak-text-sm) text-ak-muted">
        Couldn&apos;t load conversations.
        <Button variant="outline" size="sm" onClick={first.retry}>
          Retry
        </Button>
      </div>
    );
  } else {
    // An empty conversation (created, never sent) isn't listed unless it is answering (T092).
    const all = [...first.data.conversations, ...(more?.rows ?? [])].filter(
      (c) => c.message_count > 0 || runs.running(c.id) || c.id === current,
    );
    const rows = q ? all.filter((c) => (c.title || "New chat").toLowerCase().includes(q)) : all;
    if (all.length === 0 && !agent) {
      body = (
        <div className="flex flex-col items-center gap-2.5 px-4 pt-10 text-center">
          <div className="grid size-10 place-items-center rounded-[10px] border bg-ak-surface-2 text-ak-muted">
            <MessageCircle aria-hidden="true" className="size-4.5" />
          </div>
          <p className="text-ak-muted">No conversations yet.</p>
          <Button asChild variant="outline" size="sm" className="bg-ak-surface">
            <Link to="/new" state={{ focus: "composer" }}>
              New chat
            </Link>
          </Button>
        </div>
      );
    } else {
      const now = new Date();
      const groups = new Map<string, ConversationSummary[]>();
      for (const c of rows) {
        const g = dateGroup(c.updated_at, now);
        groups.set(g, [...(groups.get(g) ?? []), c]);
      }
      body = (
        <>
          {rows.length === 0 ? (
            <p className="px-2 py-2 text-(length:--ak-text-sm) text-ak-muted">
              {q ? "No loaded chats match." : "No chats with this agent."}
            </p>
          ) : null}
          {[...groups].map(([g, cs]) => (
            <section key={g} aria-label={g} className="flex flex-col">
              <h3 className="px-2 pt-3 pb-1 text-xs font-medium text-ak-muted">{g}</h3>
              <ul className="flex flex-col">
                {cs.map((c) => (
                  <Row key={c.id} c={c} group={g} current={c.id === current} />
                ))}
              </ul>
            </section>
          ))}
          {next ? (
            <Button
              variant="ghost"
              size="sm"
              className="mt-2 self-start"
              onClick={() => void loadMore()}
              disabled={loadingMore}
            >
              {q ? "Load older to search more" : "Load more"}
            </Button>
          ) : null}
        </>
      );
    }
  }

  return (
    <>
      {head(
        <div className="flex flex-col gap-2">
          <label className="relative block">
            <span className="sr-only">Search loaded chats</span>
            <Search aria-hidden="true" className="pointer-events-none absolute top-2 left-2 size-4 text-ak-muted" />
            <input
              id={searchId}
              type="search"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search loaded chats"
              aria-keyshortcuts={`${modKey}+K`}
              className={cn(field, "pl-8")}
            />
            <kbd aria-hidden="true" className="pointer-events-none absolute top-1.5 right-2 text-xs text-ak-muted">
              {modHint}K
            </kbd>
          </label>
          {agents.state === "ready" && agents.data.length > 1 ? (
            <label className="block">
              <span className="sr-only">Agent</span>
              <select value={agent} onChange={(e) => setAgent(e.target.value)} className={field}>
                <option value="">All agents</option>
                {agents.data.map((a) => (
                  <option key={a.name} value={a.name}>
                    {a.name}
                  </option>
                ))}
              </select>
            </label>
          ) : null}
        </div>,
      )}
      <nav aria-label="Conversations" className="flex min-h-0 flex-1 flex-col overflow-auto px-2 pb-3">
        {body}
      </nav>
    </>
  );
}
