// An agent's memory on its page (T124): the files it keeps for the caller
// (GET /v1/agents/{name}/memory), how much of its limits they use, a file's
// text, and delete after asking. No editing: the agent writes its notes.
// Deleting the tenant's shared memory needs agent:admin; the API decides,
// and the button is hidden from those it would refuse.

import { ApiError, NetworkError, type MemoryEntry, type MemoryView } from "@agentkit/agent-ui/api";
import { Button } from "@agentkit/ui/components/button";
import { File as FileIcon, Folder } from "lucide-react";
import { useState } from "react";
import { hasScope } from "../access";
import { useApi, useResource } from "../api";
import { Loaded } from "./page";
import { Modal, Section } from "./parts";

const root = "/memories";

/** A size in bytes, as people read it. */
function bytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${+(n / 1024).toFixed(1)} KB`;
  return `${+(n / (1024 * 1024)).toFixed(1)} MB`;
}

/** The entries directly in dir: a view lists two levels. */
function children(v: MemoryView): MemoryEntry[] {
  const depth = v.path.split("/").length + 1;
  return (v.entries ?? []).filter((e) => e.path.split("/").length === depth);
}

function name(path: string): string {
  return path.slice(path.lastIndexOf("/") + 1);
}

function errorText(e: unknown, what: string): string {
  if (e instanceof ApiError) return `${what}: ${e.message} (${e.status} ${e.code}).`;
  if (e instanceof NetworkError) return `${what}: couldn't reach the server.`;
  return `${what}.`;
}

function DeleteDialog({
  agent,
  entry,
  onClose,
  onDone,
}: {
  agent: string;
  entry: MemoryEntry;
  onClose: () => void;
  onDone: () => void;
}) {
  const { client } = useApi();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const go = () => {
    setBusy(true);
    client.deleteMemory(agent, entry.path).then(onDone, (err: unknown) => {
      setBusy(false);
      setError(errorText(err, "It wasn't deleted"));
    });
  };
  return (
    <Modal title={`Delete ${name(entry.path)}?`} onClose={onClose}>
      <p className="text-(length:--ak-text-sm) text-ak-text-2">
        {entry.dir ? "The folder and every note in it are" : "The note is"} gone for good, and {agent} won&apos;t
        remember what {entry.dir ? "they said" : "it said"}.
      </p>
      {error ? (
        <p role="alert" className="mt-2 text-(length:--ak-text-sm) text-ak-bad">
          {error}
        </p>
      ) : null}
      <div className="mt-4 flex justify-end gap-2">
        <Button type="button" variant="outline" data-autofocus="" onClick={onClose}>
          Cancel
        </Button>
        <Button type="button" disabled={busy} onClick={go}>
          Delete
        </Button>
      </div>
    </Modal>
  );
}

function Usage({ v }: { v: MemoryView }) {
  return (
    <p className="text-(length:--ak-text-sm) text-ak-text-2">
      {v.scope === "user" ? "Your notes" : "Notes everyone in the tenant shares"}: {bytes(v.usage.bytes)} of{" "}
      {bytes(v.limits.scope_bytes)}, {v.usage.files} of {v.limits.files} files, at most {bytes(v.limits.file_bytes)} a
      file.
    </p>
  );
}

/** The memory of an agent that has one. */
export function AgentMemory({ agent }: { agent: string }) {
  const { me } = useApi();
  const [path, setPath] = useState(root);
  const [reload, setReload] = useState(0);
  const [doomed, setDoomed] = useState<MemoryEntry | null>(null);
  const [note, setNote] = useState("");
  const res = useResource((c, signal) => c.getMemory(agent, path, { signal }), `memory\n${agent}\n${path}\n${reload}`);
  const open = (p: string) => {
    setNote("");
    setPath(p);
  };
  const up = path.slice(0, path.lastIndexOf("/")) || root;
  return (
    <Section title="Memory">
      <Loaded res={res} what="its memory">
        {(v) => {
          const canDelete = v.scope === "user" || me === null || !me.credential || hasScope(me, "agent:admin");
          const del = (e: MemoryEntry) =>
            canDelete ? (
              <Button
                type="button"
                variant="outline"
                size="sm"
                aria-label={`Delete ${name(e.path)}`}
                onClick={() => setDoomed(e)}
              >
                Delete
              </Button>
            ) : null;
          const list = children(v);
          return (
            <>
              <Usage v={v} />
              {path !== root ? (
                <div className="flex flex-wrap items-center gap-2">
                  <Button type="button" variant="outline" size="sm" onClick={() => open(up)}>
                    Back to {name(up)}
                  </Button>
                  <span className="font-mono text-(length:--ak-text-sm) break-all text-ak-text">{v.path}</span>
                  {del({ path: v.path, dir: v.dir, size: v.size })}
                </div>
              ) : null}
              {!v.dir ? (
                <pre
                  aria-label={`The text of ${name(v.path)}`}
                  className="overflow-x-auto rounded-md border bg-ak-surface-2 px-3 py-2.5 font-mono text-(length:--ak-text-sm) whitespace-pre-wrap text-ak-text"
                >
                  {v.text || "(empty)"}
                </pre>
              ) : list.length === 0 ? (
                <p className="text-(length:--ak-text-sm) text-ak-muted">
                  {path === root ? `No notes yet: ${agent} writes them as you talk.` : "This folder is empty."}
                </p>
              ) : (
                <ul aria-label="Memory files" className="flex flex-col divide-y rounded-md border">
                  {list.map((e) => (
                    <li key={e.path} className="flex items-center gap-2 px-3 py-1.5 text-(length:--ak-text-sm)">
                      {e.dir ? (
                        <Folder aria-hidden="true" className="size-4 flex-none text-ak-muted" />
                      ) : (
                        <FileIcon aria-hidden="true" className="size-4 flex-none text-ak-muted" />
                      )}
                      <button
                        type="button"
                        onClick={() => open(e.path)}
                        className="min-w-0 flex-1 truncate text-left font-mono text-ak-text hover:underline"
                      >
                        {name(e.path)}
                        {e.dir ? "/" : ""}
                      </button>
                      <span className="flex-none text-ak-muted">{bytes(e.size)}</span>
                      {del(e)}
                    </li>
                  ))}
                </ul>
              )}
              {note ? (
                <p role="status" className="text-(length:--ak-text-sm) text-ak-text-2">
                  {note}
                </p>
              ) : null}
            </>
          );
        }}
      </Loaded>
      {doomed ? (
        <DeleteDialog
          agent={agent}
          entry={doomed}
          onClose={() => setDoomed(null)}
          onDone={() => {
            setDoomed(null);
            // Back to the top when the view goes with it: the path deleted,
            // or a folder whose last entry it was (an empty folder is no more).
            const last = res.state === "ready" && children(res.data).length === 1;
            if (path === doomed.path || path.startsWith(doomed.path + "/") || last) setPath(root);
            setReload((n) => n + 1);
            setNote(`${name(doomed.path)} deleted.`);
          }}
        />
      ) : null}
    </Section>
  );
}
