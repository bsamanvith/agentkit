// The chat (docs/UI.md "Chat", "Runs and conversations"): New chat with the
// agent picker at /new, and a conversation at /c/{id}. Runs live in the app's
// run store (runs.ts), so this page only shows them.

import { Composer, threadFromHistory, type ToolDisplay, type Turn } from "@agentkit/agent-ui";
import { ApiError, NetworkError, type Agent, type AgentClient, type Conversation } from "@agentkit/agent-ui/api";
import { Status } from "@agentkit/ui";
import { Button } from "@agentkit/ui/components/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@agentkit/ui/components/dropdown-menu";
import { cn } from "@agentkit/ui/lib/utils";
import { ChevronDown, Ellipsis, SquarePen } from "lucide-react";
import { useEffect, useEffectEvent, useMemo, useState, type ReactNode } from "react";
import { Link, useLocation, useNavigate, useParams, useSearchParams } from "react-router";
import { useApi, useResource } from "../api";
import { useLayout } from "../layout";
import { listAgents } from "../lists";
import { iconButton, Loaded, Page } from "../pages/page";
import { readiness } from "../pages/readiness";
import { composerId, preloadMarkdown, withMeta } from "./shared";
import { useConversationState, useDraft, useRuns } from "./use-runs";
import type { Problem } from "./runs";
import { Notice, Thread } from "./thread";

/** localStorage: the agent of the last new chat (docs/UI.md "The agent picker"). */
const lastAgentKey = "agentkit.agent";

function lastAgent(): string | null {
  try {
    return localStorage.getItem(lastAgentKey);
  } catch {
    return null;
  }
}

/** What the page was opened with: a conversation just created by its first send, or New chat's focus. */
type NavState = { sent?: boolean; focus?: "composer" } | null;

function focusComposer() {
  requestAnimationFrame(() => document.getElementById(composerId)?.focus());
}

/** The tools' display metadata of the agent's ready toolsets, by the name the model calls them. */
async function loadDisplays(c: AgentClient, agent: Agent, signal: AbortSignal): Promise<Map<string, ToolDisplay>> {
  const out = new Map<string, ToolDisplay>();
  await Promise.all(
    agent.toolsets
      .filter((ts) => ts.status === "ready")
      .map(async (ts) => {
        const body = await c.getJSON<{ tools?: { name: string; display?: ToolDisplay }[] }>(
          `/v1/toolsets/${encodeURIComponent(ts.name)}/tools`,
          { signal },
        );
        for (const t of body.tools ?? []) {
          if (!t.display) continue;
          const prefixed = `${ts.name}__${t.name}`;
          out.set(ts.tools.includes(prefixed) ? prefixed : t.name, t.display);
        }
      }),
  );
  return out;
}

const noDisplays: ReadonlyMap<string, ToolDisplay> = new Map();

function useDisplays(agent: Agent | undefined): ReadonlyMap<string, ToolDisplay> {
  const res = useResource(
    (c, signal) => (agent ? loadDisplays(c, agent, signal) : Promise.resolve(new Map())),
    `displays:${agent?.name ?? ""}`,
    !agent,
  );
  return res.state === "ready" ? res.data : noDisplays;
}

/** The online state: Send is off while offline (docs/UI.md "Offline"). */
function useOnline(): boolean {
  const [online, setOnline] = useState(() => navigator.onLine);
  useEffect(() => {
    const on = () => setOnline(true);
    const off = () => setOnline(false);
    window.addEventListener("online", on);
    window.addEventListener("offline", off);
    return () => {
      window.removeEventListener("online", on);
      window.removeEventListener("offline", off);
    };
  }, []);
  return online;
}

/** The text of a send that didn't start (docs/UI.md "States"). */
function problemText(p: { error: Error }, agent: string): string {
  const e = p.error;
  if (e instanceof NetworkError) return "Couldn't reach the server. Your message is back in the box.";
  if (!(e instanceof ApiError)) return e.message;
  switch (e.code) {
    case "conversation_interrupted":
      return "The last answer didn't finish. Resume continues from the last saved step.";
    case "conversation_busy":
      return "This conversation is answering in another tab or window. Your message is back in the box.";
    case "too_large":
      return "This message is too long to send (the limit is 1 MiB). The draft is kept.";
    case "invalid_request":
      return `The server couldn't read this request: ${e.message}`;
    case "agent_mismatch":
      return `This conversation belongs to ${agent}.`;
    case "not_found":
      return "This conversation no longer exists. Your message is back in the box.";
    default:
      return `${e.message} (${e.status} ${e.code})`;
  }
}

/** The bar under the thread: notices, then the composer. */
function Bottom({ children }: { children: ReactNode }) {
  const { phone } = useLayout();
  return (
    <div
      className={cn(
        "flex-none",
        phone ? "px-4 pt-1.5 pb-[calc(12px+env(safe-area-inset-bottom,0px))]" : "px-6 pt-2 pb-3.5",
      )}
    >
      <div className="mx-auto flex max-w-(--ak-chat-w) flex-col gap-2">{children}</div>
    </div>
  );
}

function threadPadding(phone: boolean) {
  return phone ? "px-4 pt-5 pb-2" : "px-6 pt-7 pb-3";
}

/** Esc stops the answer when no menu is open; "/" focuses the message box outside text fields. */
function useChatKeys(running: boolean, stop: () => void) {
  const onKey = useEffectEvent((e: KeyboardEvent) => {
    if (e.defaultPrevented || e.repeat) return;
    const t = e.target instanceof Element ? e.target : null;
    if (e.key === "Escape" && running) {
      if (document.querySelector('[role="menu"], [role="dialog"][aria-modal="true"]:not([inert])')) return;
      e.preventDefault();
      stop();
      return;
    }
    if (e.key === "/" && !e.metaKey && !e.ctrlKey && !e.altKey) {
      if (t?.closest("input, textarea, select, [contenteditable=true]")) return;
      e.preventDefault();
      document.getElementById(composerId)?.focus();
    }
  });
  useEffect(() => {
    const listener = (e: KeyboardEvent) => onKey(e);
    window.addEventListener("keydown", listener);
    return () => window.removeEventListener("keydown", listener);
  }, []);
}

export function ChatPage() {
  const { id } = useParams();
  useEffect(preloadMarkdown, []);
  return id ? <ConversationPage key={id} id={id} /> : <NewChatPage />;
}

/** The New chat agent picker: a button with the chosen agent, opening the list (DropdownMenu, not modal: CSP). */
function AgentPicker({ agents, chosen, onPick }: { agents: Agent[]; chosen: Agent; onPick: (name: string) => void }) {
  if (agents.length === 1) return <AgentChip name={chosen.name} />;
  return (
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          aria-label={`Agent: ${chosen.name}`}
          className="inline-flex h-8 min-w-0 flex-none items-center gap-1 rounded-md px-2 font-mono text-(length:--ak-text-sm) text-ak-text hover:bg-ak-hover aria-expanded:bg-ak-select"
        >
          <span className="truncate">{chosen.name}</span>
          <ChevronDown aria-hidden="true" className="size-3.5 text-ak-muted" />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-72">
        <DropdownMenuLabel className="text-ak-muted">Agent</DropdownMenuLabel>
        <DropdownMenuRadioGroup value={chosen.name} onValueChange={onPick}>
          {agents.map((a) => (
            <DropdownMenuRadioItem key={a.name} value={a.name} className="flex flex-col items-start gap-0.5">
              <span className="font-mono">{a.name}</span>
              <span className="text-xs text-ak-muted">{a.description || readiness(a).text}</span>
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/** A conversation's agent: a read-only chip with New chat with it (docs/UI.md "The agent picker"). */
function AgentChip({ name, removed = false }: { name: string; removed?: boolean }) {
  return (
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          aria-label={`Agent: ${name}${removed ? " (removed)" : ""}`}
          className="inline-flex h-7 max-w-48 flex-none items-center rounded-full border border-ak-line-strong px-2.5 font-mono text-xs text-ak-text-2 hover:bg-ak-hover aria-expanded:bg-ak-select"
        >
          <span className="truncate">{name}</span>
          {removed ? <span className="ml-1 font-sans text-ak-muted">(removed)</span> : null}
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-60">
        {removed ? null : (
          <DropdownMenuItem asChild>
            <Link to={`/new?agent=${encodeURIComponent(name)}`} state={{ focus: "composer" }}>
              New chat with {name}
            </Link>
          </DropdownMenuItem>
        )}
        <DropdownMenuItem asChild>
          <Link to="/new" state={{ focus: "composer" }}>
            New chat with another agent…
          </Link>
        </DropdownMenuItem>
        {removed ? null : (
          <DropdownMenuItem asChild>
            <Link to={`/agents/${encodeURIComponent(name)}`}>Open agent details</Link>
          </DropdownMenuItem>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function NewChatLink() {
  const { phone } = useLayout();
  // The drawer's New chat is a tap further away on a phone.
  if (!phone) return null;
  return (
    <Link to="/new" state={{ focus: "composer" }} aria-label="New chat" className={iconButton}>
      <SquarePen aria-hidden="true" className="size-4" />
    </Link>
  );
}

function NewChatPage() {
  const { client, unauthorized } = useApi();
  const runs = useRuns();
  const navigate = useNavigate();
  const location = useLocation();
  const [params] = useSearchParams();
  const { phone } = useLayout();
  const agents = useResource(listAgents, "agents");
  const [picked, setPicked] = useState<string | null>(null);
  const [draft, setDraft] = useDraft("new");
  const [creating, setCreating] = useState(false);
  const [problem, setProblem] = useState<Problem | null>(null);
  const online = useOnline();
  const wantFocus = (location.state as NavState)?.focus === "composer" && !phone;

  useEffect(() => {
    if (wantFocus) focusComposer();
  }, [wantFocus, location.key]);

  const list = agents.state === "ready" ? agents.data : [];
  const wanted = picked ?? params.get("agent") ?? lastAgent();
  const agent = list.find((a) => a.name === wanted) ?? list.find((a) => a.default) ?? list[0];

  async function send(text: string) {
    if (!agent) return;
    setCreating(true);
    setProblem(null);
    try {
      // Created on the first send, so an abandoned new chat leaves nothing (T092).
      const conv = await client.createConversation(agent.name);
      try {
        localStorage.setItem(lastAgentKey, agent.name);
      } catch {
        // Storage blocked.
      }
      runs.send(client, conv.id, text, [], agent.name);
      navigate(`/c/${encodeURIComponent(conv.id)}`, { replace: true, state: { sent: true } satisfies NavState });
    } catch (e) {
      setDraft(text);
      if (e instanceof ApiError && e.status === 401) unauthorized();
      else setProblem({ error: e instanceof Error ? e : new Error(String(e)), text });
      setCreating(false);
    }
  }

  return (
    <Page
      title="New chat"
      muted
      body="bare"
      autoFocus={!wantFocus}
      lead={agent ? <AgentPicker agents={list} chosen={agent} onPick={setPicked} /> : undefined}
      aside={<NewChatLink />}
    >
      <Thread
        turns={[]}
        label="New chat"
        agent={agent?.name ?? ""}
        displays={noDisplays}
        actions={{}}
        className={threadPadding(phone)}
      >
        <Loaded res={agents} what="agents">
          {(all) => {
            if (!agent || all.length === 0) {
              return (
                <p className="text-ak-muted">
                  No agents are configured. An admin adds them to the server config (
                  <code className="font-mono">agents:</code>).
                </p>
              );
            }
            const r = readiness(agent);
            return (
              <section aria-label="Agent" className="flex flex-col gap-1 rounded-lg border p-4">
                <div className="font-mono text-sm font-medium text-ak-text">{agent.name}</div>
                {agent.description ? <p className="text-ak-text-2">{agent.description}</p> : null}
                <Status tone={r.tone} className="text-sm">
                  {r.text}
                </Status>
              </section>
            );
          }}
        </Loaded>
      </Thread>
      <Bottom>
        {problem ? (
          <Notice tone="bad" role="alert">
            {problemText(problem, agent?.name ?? "")}
          </Notice>
        ) : null}
        <Composer
          id={composerId}
          value={draft}
          onValueChange={setDraft}
          onSend={(t) => void send(t)}
          {...(agent ? {} : { disabledReason: "Choose an agent to start a chat." })}
          {...(creating ? { sendDisabledReason: "Sending…" } : !online ? { sendDisabledReason: offline } : {})}
          placeholder={agent ? `Message ${agent.name}` : "Message"}
        />
      </Bottom>
    </Page>
  );
}

const offline = "You're offline. Messages will not send until the connection is back.";

function ConversationPage({ id }: { id: string }) {
  const { client } = useApi();
  const runs = useRuns();
  const navigate = useNavigate();
  const location = useLocation();
  const { phone } = useLayout();
  const state = useConversationState(id);
  const [draft, setDraft] = useDraft(id);
  const online = useOnline();
  const sent = (location.state as NavState)?.sent === true;

  const history = useResource((c, signal) => c.getConversation(id, { signal }), `conversation:${id}:${state.version}`);
  const agents = useResource(listAgents, "agents");
  const [conv, setConv] = useState<Conversation | null>(null);
  if (history.state === "ready" && history.data !== conv) setConv(history.data);

  useEffect(() => {
    // Back to a conversation whose answer ended meanwhile: its history has it.
    // Not right after the first send: the run is this page's own.
    if (!sent) runs.settle(id);
    runs.view(id);
    return () => runs.view(undefined);
  }, [runs, id, sent]);
  useEffect(() => {
    if (sent && !phone) focusComposer();
  }, [sent, phone]);

  const agentName = conv?.agent ?? "";
  const agent = agents.state === "ready" ? agents.data.find((a) => a.name === agentName) : undefined;
  const removed = agents.state === "ready" && conv !== null && !agent;
  const displays = useDisplays(agent);

  const live = state.live;
  const running = live?.answer.state === "streaming";
  const busy = state.problem?.busy;
  const turns: Turn[] = useMemo(() => {
    if (live) {
      const t: Turn[] = [...live.base];
      if (live.user) t.push(live.user);
      if (!busy) t.push(live.answer);
      return t;
    }
    return conv ? withMeta(threadFromHistory(conv.messages), state.meta) : [];
  }, [live, busy, conv, state.meta]);

  const firstUser = turns.find((t) => t.role === "user");
  const title = firstUser?.text.split("\n")[0]?.slice(0, 120) || (conv || live ? "New chat" : "Conversation");
  useChatKeys(running, () => runs.stop(id));

  const resume = () => runs.resume(client, id, turns, agentName);
  // The thread's own note offers Resume after a Stop or a run error; otherwise the composer does.
  const last = turns.at(-1);
  const noted = last?.role === "assistant" && (last.state === "stopped" || last.state === "failed");
  const interrupted =
    (!running && !busy && !noted && conv?.status === "interrupted") ||
    (state.problem?.error instanceof ApiError && state.problem.error.code === "conversation_interrupted");

  if (history.state === "failed" && history.error instanceof ApiError && history.error.status === 404 && !live) {
    return (
      <Page title="Not found" body="page">
        <p className="text-ak-text-2">This conversation doesn&apos;t exist, or it was deleted.</p>
        <Button asChild variant="outline" className="self-start">
          <Link to="/new">New chat</Link>
        </Button>
      </Page>
    );
  }

  async function remove() {
    const message = running
      ? "Delete this conversation? This also stops the running answer."
      : "Delete this conversation?";
    if (!window.confirm(message)) return;
    if (running) runs.stop(id);
    try {
      await client.deleteConversation(id);
      runs.forget(id);
      navigate(agentName ? `/new?agent=${encodeURIComponent(agentName)}` : "/new", { replace: true });
    } catch (e) {
      window.alert(`Couldn't delete it: ${e instanceof Error ? e.message : String(e)}`);
    }
  }

  return (
    <Page
      title={title}
      body="bare"
      autoFocus={!sent}
      lead={agentName ? <AgentChip name={agentName} removed={removed} /> : undefined}
      aside={
        <>
          <DropdownMenu modal={false}>
            <DropdownMenuTrigger asChild>
              <button type="button" aria-label="Conversation actions" className={iconButton}>
                <Ellipsis aria-hidden="true" className="size-4" />
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-48">
              <DropdownMenuItem onSelect={() => void navigator.clipboard?.writeText(id)}>Copy ID</DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem onSelect={() => void remove()} className="text-ak-bad">
                Delete
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
          <NewChatLink />
        </>
      }
    >
      {history.state === "failed" && !live ? (
        <div className={threadPadding(phone)}>
          <div className="mx-auto max-w-(--ak-chat-w)">
            <Loaded res={history} what="the conversation">
              {() => null}
            </Loaded>
          </div>
        </div>
      ) : (
        <Thread
          turns={turns}
          label={title}
          agent={agentName}
          displays={displays}
          actions={{
            onResume: resume,
            onNewChat: () => navigate(`/new?agent=${encodeURIComponent(agentName)}`, { state: { focus: "composer" } }),
          }}
          className={threadPadding(phone)}
          ui={{
            edits: runs.edits,
            scope: id,
            ...(removed || !online ? {} : { onAction: (a) => runs.act(client, id, a, turns, agentName) }),
          }}
          after={
            busy ? (
              <Notice
                tone="info"
                role="status"
                actions={
                  busy === "still" ? (
                    <>
                      <Button variant="outline" size="sm" onClick={() => runs.retryNow(client, id, agentName)}>
                        Retry now
                      </Button>
                      <Button variant="ghost" size="sm" onClick={() => runs.cancel(id)}>
                        Cancel
                      </Button>
                    </>
                  ) : null
                }
              >
                {busy === "still"
                  ? "Still busy."
                  : (state.problem?.error as ApiError | undefined)?.code === "shutting_down"
                    ? "The server is restarting. Retrying in 2 s…"
                    : "The server is busy. Retrying in 2 s…"}
              </Notice>
            ) : null
          }
        >
          {history.state === "loading" && !live ? (
            <Loaded res={history} what="the conversation">
              {() => null}
            </Loaded>
          ) : null}
        </Thread>
      )}
      <Bottom>
        {interrupted ? (
          <Notice
            tone="warn"
            actions={
              <Button variant="outline" size="sm" onClick={resume}>
                Resume
              </Button>
            }
          >
            The last answer didn&apos;t finish. Resume continues from the last saved step.
          </Notice>
        ) : state.problem && !busy ? (
          <Notice
            tone="bad"
            role="alert"
            actions={
              <Button variant="ghost" size="sm" onClick={() => runs.dismiss(id)}>
                Dismiss
              </Button>
            }
          >
            {problemText(state.problem, agentName)}
          </Notice>
        ) : null}
        <Composer
          id={composerId}
          value={draft}
          onValueChange={setDraft}
          running={running}
          onStop={() => runs.stop(id)}
          onSend={(t) => runs.send(client, id, t, turns, agentName)}
          {...(removed ? { disabledReason: "This agent no longer exists, so this conversation is read-only." } : {})}
          {...(busy
            ? { sendDisabledReason: "Your last message is waiting to be sent." }
            : !online
              ? { sendDisabledReason: offline }
              : {})}
          placeholder={agentName ? `Message ${agentName}` : "Message"}
        />
      </Bottom>
    </Page>
  );
}
