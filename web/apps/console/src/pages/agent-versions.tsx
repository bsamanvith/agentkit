// A stored agent's versions, /agents/{name}/versions[/{v}] (T077, docs/UI.md
// "Versions"): the list, newest first, with who saved each and when; the
// selected one compared with the published one (or, for the published one,
// the one before); and its actions: Publish (a draft newer than the
// published one), Roll back (an older one, published again), Edit from, and
// Delete draft (never the published one: runs refer to it).

import { ApiError, type AgentVersion, type AgentVersionList } from "@agentkit/agent-ui/api";
import { Badge } from "@agentkit/ui/components/badge";
import { Button } from "@agentkit/ui/components/button";
import { cn } from "@agentkit/ui/lib/utils";
import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useApi, useResource } from "../api";
import { useLayout } from "../layout";
import { useLists } from "../lists";
import { changedLines } from "./agent-def";
import { DefinitionDiff } from "./definition-diff";
import { useForbidden } from "./forbidden";
import { ErrorLine } from "./mcp-review";
import { Loaded, Page } from "./page";
import { Forbidden, Modal } from "./parts";

const when = (iso: string) => {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
};

type Ask = { kind: "publish" | "delete"; v: AgentVersion } | null;

function Versions({ name, start, selected }: { name: string; start: AgentVersionList; selected: number }) {
  const { client } = useApi();
  const { reloadAgents } = useLists();
  const { phone } = useLayout();
  const navigate = useNavigate();
  const [list, setList] = useState(start);
  const [ask, setAsk] = useState<Ask>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [compare, setCompare] = useState<number | null>(null);

  const ordered = [...list.versions].sort((a, b) => b.version - a.version);
  const sel = ordered.find((v) => v.version === selected) ?? ordered[0];
  const published = list.versions.find((v) => v.version === list.published) ?? null;
  // Compared with the published version; the published one with the one before it.
  const defaultOther =
    sel && sel.version !== list.published ? published : (ordered.find((v) => sel && v.version < sel.version) ?? null);
  const other = compare !== null ? (list.versions.find((v) => v.version === compare) ?? null) : defaultOther;

  const act = async (a: NonNullable<Ask>) => {
    setAsk(null);
    setBusy(true);
    setError("");
    try {
      if (a.kind === "publish") setList(await client.publishAgent(name, a.v.version));
      else {
        await client.deleteAgentVersion(name, a.v.version);
        setList(await client.listAgentVersions(name));
        navigate(`/agents/${encodeURIComponent(name)}/versions`, { replace: true });
      }
      reloadAgents();
    } catch (e) {
      setError(
        e instanceof ApiError && e.code === "version_published"
          ? "That version is published: runs refer to it, so it stays."
          : e instanceof ApiError
            ? `${e.message} (${e.status} ${e.code})`
            : "Couldn't reach the server.",
      );
    } finally {
      setBusy(false);
    }
  };

  const label = (v: AgentVersion) =>
    v.version === list.published ? "Published" : v.version > list.published ? "Draft" : "";

  return (
    <Page
      title={`${name}: versions`}
      back={{ to: `/agents/${encodeURIComponent(name)}`, label: name }}
      wrap
      aside={
        <Button asChild variant="outline" size="sm">
          <Link to={`/agents/${encodeURIComponent(name)}/edit`}>Edit</Link>
        </Button>
      }
      body="bare"
    >
      <div className={cn("flex min-h-0 flex-1", phone && "flex-col overflow-auto")}>
        <nav
          aria-label="Versions"
          className={cn("flex-none overflow-auto p-3", phone ? "border-b" : "w-[240px] border-r")}
        >
          {ordered.length === 0 ? <p className="text-(length:--ak-text-sm) text-ak-muted">No versions.</p> : null}
          <ul className="flex flex-col gap-0.5">
            {ordered.map((v) => (
              <li key={v.version}>
                <Link
                  to={`/agents/${encodeURIComponent(name)}/versions/${v.version}`}
                  aria-current={sel?.version === v.version ? "page" : undefined}
                  onClick={() => setCompare(null)}
                  className="flex flex-col rounded-md px-2 py-1.5 text-(length:--ak-text-sm) hover:bg-ak-hover aria-[current=page]:bg-ak-select"
                >
                  <span className="flex items-center gap-2 text-ak-text">
                    v{v.version}
                    {label(v) ? (
                      <Badge variant={label(v) === "Published" ? "default" : "outline"}>{label(v)}</Badge>
                    ) : null}
                  </span>
                  <span className="text-xs text-ak-muted">
                    {[v.author, when(v.created_at)].filter(Boolean).join(" · ")}
                  </span>
                  {v.message ? <span className="truncate text-xs text-ak-text-2">{v.message}</span> : null}
                </Link>
              </li>
            ))}
          </ul>
        </nav>
        <div className="min-h-0 flex-1 overflow-auto">
          {sel ? (
            <div className={cn("flex max-w-[880px] flex-col gap-4", phone ? "p-4" : "p-6")}>
              <div className="flex flex-wrap items-center gap-2">
                <h2 className="flex-1 text-(length:--ak-text-md) font-semibold text-ak-text">
                  v{sel.version}
                  {label(sel) ? ` · ${label(sel)}` : ""}
                </h2>
                {sel.version !== list.published ? (
                  <Button size="sm" disabled={busy} onClick={() => setAsk({ kind: "publish", v: sel })}>
                    {sel.version > list.published ? `Publish v${sel.version}…` : `Roll back to v${sel.version}…`}
                  </Button>
                ) : null}
                <Button asChild variant="outline" size="sm">
                  <Link to={`/agents/${encodeURIComponent(name)}/edit?from=${sel.version}`}>
                    Edit from v{sel.version}
                  </Link>
                </Button>
                {sel.version !== list.published ? (
                  <Button variant="ghost" size="sm" disabled={busy} onClick={() => setAsk({ kind: "delete", v: sel })}>
                    Delete draft
                  </Button>
                ) : null}
              </div>
              {error ? <ErrorLine>{error}</ErrorLine> : null}
              <div className="flex flex-wrap items-center gap-2 text-(length:--ak-text-sm)">
                <label htmlFor="compare-with" className="text-ak-text-2">
                  Compare with
                </label>
                <select
                  id="compare-with"
                  value={other?.version ?? ""}
                  onChange={(e) => setCompare(e.target.value === "" ? null : Number(e.target.value))}
                  className="h-8 rounded-md border border-input bg-transparent px-2"
                >
                  {other === null ? <option value="">Nothing</option> : null}
                  {ordered
                    .filter((v) => v.version !== sel.version)
                    .map((v) => (
                      <option key={v.version} value={v.version}>
                        v{v.version}
                        {label(v) ? ` (${label(v).toLowerCase()})` : ""}
                      </option>
                    ))}
                </select>
              </div>
              {other ? (
                <DefinitionDiff
                  from={other.definition}
                  to={sel.definition}
                  label={`Changes from v${other.version} to v${sel.version}`}
                />
              ) : (
                <DefinitionDiff from={{}} to={sel.definition} label={`v${sel.version}`} />
              )}
            </div>
          ) : null}
        </div>
      </div>
      {ask?.kind === "publish" ? (
        <Modal
          title={
            ask.v.version > list.published
              ? `Publish v${ask.v.version} of ${name}?`
              : `Roll back ${name} to v${ask.v.version}?`
          }
          onClose={() => setAsk(null)}
        >
          <p className="text-(length:--ak-text-sm) text-ak-text-2">
            {published
              ? `${changedLines(published.definition, ask.v.definition)} lines differ from v${published.version}, which is published now. `
              : ""}
            New chats and tasks use v{ask.v.version}. Runs already going keep the version they started with.
          </p>
          <div className="mt-4 flex justify-end gap-2">
            <Button variant="outline" onClick={() => setAsk(null)}>
              Cancel
            </Button>
            <Button onClick={() => void act(ask)} data-autofocus="">
              {ask.v.version > list.published ? `Publish v${ask.v.version}` : `Roll back to v${ask.v.version}`}
            </Button>
          </div>
        </Modal>
      ) : null}
      {ask?.kind === "delete" ? (
        <Modal title={`Delete v${ask.v.version} of ${name}?`} onClose={() => setAsk(null)}>
          <p className="text-(length:--ak-text-sm) text-ak-text-2">
            It isn&apos;t published, so nothing runs it. Its number isn&apos;t given again.
          </p>
          <div className="mt-4 flex justify-end gap-2">
            <Button variant="outline" onClick={() => setAsk(null)} data-autofocus="">
              Cancel
            </Button>
            <Button variant="destructive" onClick={() => void act(ask)}>
              Delete v{ask.v.version}
            </Button>
          </div>
        </Modal>
      ) : null}
    </Page>
  );
}

export function AgentVersionsPage() {
  const { name = "", v } = useParams();
  const res = useResource((c, signal) => c.listAgentVersions(name, { signal }), `versions\n${name}`);
  const forbidden = useForbidden(res);
  if (forbidden) return <Forbidden />;
  if (res.state !== "ready") {
    return (
      <Page title={`${name}: versions`}>
        <Loaded res={res} what={`${name}'s versions`}>
          {() => null}
        </Loaded>
      </Page>
    );
  }
  if (res.data.read_only) {
    return (
      <Page title={`${name}: versions`}>
        <p className="text-ak-text-2">
          {name} comes from the server config file: it has no versions here.{" "}
          <Link to={`/agents/new?from=${encodeURIComponent(name)}`} className="underline">
            Duplicate it
          </Link>{" "}
          to make a stored agent.
        </p>
      </Page>
    );
  }
  return <Versions name={name} start={res.data} selected={Number(v ?? NaN)} />;
}
