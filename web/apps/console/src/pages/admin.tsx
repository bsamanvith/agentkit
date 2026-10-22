import { Status } from "@agentkit/ui";
import { Button } from "@agentkit/ui/components/button";
import { ApiError } from "@agentkit/agent-ui/api";
import { Bot, ChevronRight, Info, Wrench, type LucideIcon } from "lucide-react";
import type { ReactNode } from "react";
import { Link, Navigate, useParams } from "react-router";
import { isAdmin } from "../access";
import { useApi, type Resource } from "../api";
import { useLayout } from "../layout";
import { ListRow } from "../list-row";
import { agentRow, toolsetRow, useLists, type RowInfo } from "../lists";
import { AgentDetail } from "./agent";
import { Empty, Loaded, Page } from "./page";
import { Forbidden } from "./parts";
import { ToolsetDetail } from "./toolset";

export { ConnectDonePage, ConnectionsPage } from "./connections";

/**
 * Whether to show the 403 page instead: the caller is known not to be an
 * admin, or the API refused the list (a stale tab after a role change). A
 * CSRF refusal is never this (the client retries it).
 */
function useForbidden(res: Resource<unknown> | null): boolean {
  const { me } = useApi();
  if (me && !isAdmin(me)) return true;
  return res?.state === "failed" && res.error instanceof ApiError && res.error.status === 403;
}

type Kind = {
  title: string;
  /** In the URL: /agents/{name}. */
  base: string;
  icon: LucideIcon;
  empty: ReactNode;
};

const kinds = {
  agents: {
    title: "Agents",
    base: "/agents",
    icon: Bot,
    empty: (
      <p className="text-ak-muted">
        Add an <code className="font-mono">agents:</code> list to the server config and restart.
      </p>
    ),
  },
  toolsets: {
    title: "Toolsets",
    base: "/toolsets",
    icon: Wrench,
    empty: <p className="text-ak-muted">An admin adds MCP servers to the server config.</p>,
  },
} satisfies Record<string, Kind>;

/**
 * An admin section's own page, /agents or /toolsets. On a laptop or desktop
 * the list is the column beside it, so this shows the first item (the
 * mockup selects it); on a phone it is the list, a row per item.
 */
function SectionPage<T extends { name: string }>({
  kind,
  res,
  row,
}: {
  kind: Kind;
  res: Resource<T[]> | null;
  row: (item: T) => RowInfo;
}) {
  const { phone } = useLayout();
  const forbidden = useForbidden(res);
  if (forbidden) return <Forbidden />;
  if (res === null) return null; // the shell loads the section on screen
  if (res.state === "ready" && res.data.length === 0) {
    return (
      <Page title={kind.title} heading="body" body="bare">
        <Empty icon={kind.icon} title={`No ${kind.title.toLowerCase()} are configured`}>
          {kind.empty}
        </Empty>
      </Page>
    );
  }
  const first = res.state === "ready" ? res.data[0] : undefined;
  if (!phone && first) return <Navigate replace to={`${kind.base}/${encodeURIComponent(first.name)}`} />;
  return (
    <Page title={kind.title}>
      <Loaded res={res} what={kind.title.toLowerCase()}>
        {(list) => (
          <nav aria-label={kind.title} className="-mx-2 flex flex-col gap-1">
            {list.map((item) => (
              <ListRow
                key={item.name}
                to={`${kind.base}/${encodeURIComponent(item.name)}`}
                chevron={<ChevronRight aria-hidden="true" className="size-4 flex-none text-ak-muted" />}
                {...row(item)}
              />
            ))}
          </nav>
        )}
      </Loaded>
    </Page>
  );
}

export function AgentsPage() {
  return <SectionPage kind={kinds.agents} res={useLists().agents} row={agentRow} />;
}

export function ToolsetsPage() {
  return <SectionPage kind={kinds.toolsets} res={useLists().toolsets} row={toolsetRow} />;
}

/**
 * An item's page, /agents/{name} or /toolsets/{name}: its head bar (name,
 * status, action) and its detail.
 */
function ItemPage<T extends { name: string }>({
  kind,
  res,
  row,
  action,
  detail,
}: {
  kind: Kind;
  res: Resource<T[]> | null;
  row: (item: T) => RowInfo;
  action?: (item: T) => ReactNode;
  detail: (item: T) => ReactNode;
}) {
  const { name = "" } = useParams();
  const forbidden = useForbidden(res);
  if (forbidden) return <Forbidden />;
  if (res === null) return null;
  const back = { to: kind.base, label: kind.title.toLowerCase() };
  if (res.state !== "ready") {
    return (
      <Page title={name} mono back={back}>
        <Loaded res={res} what={kind.title.toLowerCase()}>
          {() => null}
        </Loaded>
      </Page>
    );
  }
  const item = res.data.find((i) => i.name === name);
  if (!item) {
    return (
      <Page title="Not found" heading="body" body="bare" back={back}>
        <Empty icon={Info} title={`No ${kind.title.toLowerCase().replace(/s$/, "")} named ${name}`}>
          <p className="text-ak-muted">It isn&apos;t in the server config.</p>
          <Button asChild variant="outline" size="sm">
            <Link to={kind.base}>Back to {kind.title.toLowerCase()}</Link>
          </Button>
        </Empty>
      </Page>
    );
  }
  const info = row(item);
  return (
    <Page
      title={item.name}
      mono
      back={back}
      wrap={action !== undefined}
      aside={
        <>
          <Status tone={info.tone} className="text-(length:--ak-text-sm)">
            {info.word}
          </Status>
          {action?.(item)}
        </>
      }
    >
      {/* Keyed: another item's page starts afresh (its disclosures closed). */}
      <div key={item.name} className="contents">
        {detail(item)}
      </div>
    </Page>
  );
}

export function AgentPage() {
  return (
    <ItemPage
      kind={kinds.agents}
      res={useLists().agents}
      row={agentRow}
      detail={(a) => <AgentDetail agent={a} />}
      action={(a) => (
        <Button asChild size="sm">
          <Link to={`/new?agent=${encodeURIComponent(a.name)}`}>Chat with {a.name}</Link>
        </Button>
      )}
    />
  );
}

export function ToolsetPage() {
  return (
    <ItemPage
      kind={kinds.toolsets}
      res={useLists().toolsets}
      row={toolsetRow}
      detail={(t) => <ToolsetDetail toolset={t} />}
    />
  );
}

export function NotFoundPage() {
  return (
    <Page title="Not found" heading="body" body="bare">
      <Empty icon={Info} title="Not found">
        <p className="text-ak-muted">This page doesn&apos;t exist.</p>
        <Button asChild variant="outline" size="sm">
          <Link to="/">Back to chats</Link>
        </Button>
      </Empty>
    </Page>
  );
}
