// The thread of a chat: messages, answers with their steps, notices, source
// chips and usage (docs/UI.md "Chat", "Tool calls", "Run errors"), and the
// agent's A2UI surfaces where it showed them.

import {
  answerText,
  foldSurfaces,
  Message,
  SourceChip,
  sourcesOf,
  StepGroup,
  stepText,
  uiToolName,
  UsageLine,
  type AssistantTurn,
  type Part,
  type Surface,
  type SurfaceEdits,
  type ToolDisplay,
  type Turn,
  type UIAction,
  type UserTurn,
} from "@agentkit/agent-ui";
import { Button } from "@agentkit/ui/components/button";
import { Spinner } from "@agentkit/ui/components/spinner";
import { cn } from "@agentkit/ui/lib/utils";
import { ArrowDown, CircleAlert } from "lucide-react";
import {
  lazy,
  Suspense,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
  type ReactNode,
  type RefObject,
} from "react";
import { runErrorText } from "./shared";

// Its own chunk: the first screen doesn't wait for it (web/README.md "Size").
const Markdown = lazy(() => import("@agentkit/agent-ui/markdown"));
// Only a thread with a surface loads it.
const A2UISurface = lazy(() => import("@agentkit/agent-ui/a2ui"));

/** The agent's surfaces in a thread: their state, the person's edits, and where actions go. */
export type ThreadUI = {
  edits: SurfaceEdits;
  /** The edits' scope: the conversation. */
  scope: string;
  /** Sends an action; absent, the surfaces can't be used (the agent was removed). */
  onAction?: (a: UIAction) => void;
};

type Surfaces = { all: ReadonlyMap<string, Surface>; ui?: ThreadUI | undefined; busy: boolean; agent: string };

const noEdits: ReadonlyMap<string, unknown> = new Map();
const noSubscribe = () => () => {};

function SurfacePart({ surface, surfaces }: { surface: Surface; surfaces: Surfaces }) {
  const { ui, busy, agent } = surfaces;
  const edits = useSyncExternalStore(ui?.edits.subscribe ?? noSubscribe, () =>
    ui ? ui.edits.get(ui.scope, surface) : noEdits,
  );
  const state = surface.sent ? "sent" : busy || !ui?.onAction ? "busy" : "live";
  return (
    <Suspense fallback={<div className="text-(length:--ak-text-sm) text-ak-muted">Loading the form…</div>}>
      <A2UISurface
        surface={surface}
        state={state}
        edits={edits}
        onEdit={(path, value) => ui?.edits.set(ui.scope, surface, path, value)}
        {...(ui?.onAction ? { onAction: ui.onAction } : {})}
        label={agent ? `From ${agent}` : "From the agent"}
      />
    </Suspense>
  );
}

/** A user message that is an action on a surface: what the person pressed (the button's label). */
function actionText(t: UserTurn, surfaces: ReadonlyMap<string, Surface>): string {
  const a = t.action;
  if (!a) return t.text;
  let label: string | undefined;
  for (const s of surfaces.values()) {
    if (s.id !== a.surfaceId) continue;
    const child = s.components.get(a.componentId)?.child;
    const c = typeof child === "string" ? s.components.get(child) : undefined;
    if (c?.component === "Text" && typeof c.text === "string" && c.text.trim() !== "") label = c.text.trim();
  }
  return `Sent “${label ?? a.name}”`;
}

function Parts({
  parts,
  displays,
  running,
  surfaces,
}: {
  parts: Part[];
  displays: ReadonlyMap<string, ToolDisplay>;
  running: boolean;
  surfaces: Surfaces;
}) {
  return parts.map((p, i) => {
    if (p.kind === "surface") {
      const s = surfaces.all.get(p.key);
      return s ? <SurfacePart key={p.key} surface={s} surfaces={surfaces} /> : null;
    }
    if (p.kind === "text") {
      return (
        <Suspense key={i} fallback={<div className="break-words whitespace-pre-wrap">{p.text}</div>}>
          <Markdown text={p.text} />
        </Suspense>
      );
    }
    if (p.kind === "tools") {
      // A ui_render call is the surface it showed (or, failed, a retry the model makes).
      const steps = p.steps.filter((s) => s.name !== uiToolName);
      if (steps.length === 0) return null;
      return (
        <StepGroup
          key={i}
          steps={steps.map((s) => ({
            id: s.id,
            ...(s.label !== undefined
              ? { label: s.label }
              : stepText(s.name, s.input, s.state === "running" && running, displays.get(s.name))),
            state: s.state,
            input: s.input,
            ...(s.result !== undefined ? { result: s.result } : {}),
            ...(s.durationMs !== undefined ? { durationMs: s.durationMs } : {}),
          }))}
        />
      );
    }
    return (
      <p key={i} className="text-(length:--ak-text-sm) text-ak-muted">
        A part of type <code className="font-mono">{p.type}</code> isn&apos;t shown here.
      </p>
    );
  });
}

/** A red-marked block after an answer: what happened and what to do. */
export function Notice({
  tone,
  children,
  actions,
  role,
}: {
  tone: "bad" | "warn" | "info";
  children: ReactNode;
  actions?: ReactNode;
  role?: "alert" | "status";
}) {
  return (
    <div
      role={role}
      className={cn(
        "flex flex-wrap items-center gap-x-3 gap-y-2 rounded-md border px-3 py-2 text-(length:--ak-text-sm)",
        tone === "bad" ? "border-ak-bad/40 text-ak-text" : "text-ak-text-2",
      )}
    >
      {tone === "bad" ? <CircleAlert aria-hidden="true" className="size-4 flex-none text-ak-bad" /> : null}
      <span className={cn("min-w-0 flex-1", tone === "warn" && "text-ak-text")}>{children}</span>
      {actions}
    </div>
  );
}

export type AnswerActions = {
  /** Resume the conversation (the last answer only). */
  onResume?: () => void;
  onNewChat?: () => void;
};

function Answer({
  a,
  agent,
  displays,
  last,
  actions,
  surfaces,
}: {
  a: AssistantTurn;
  agent: string;
  displays: ReadonlyMap<string, ToolDisplay>;
  last: boolean;
  actions: AnswerActions;
  surfaces: Surfaces;
}) {
  const streaming = a.state === "streaming";
  const text = answerText(a);
  const sources = a.state === "done" ? sourcesOf(text) : [];
  let notice: ReactNode = null;
  if (a.state === "stopped") {
    notice = (
      <Notice
        tone="info"
        actions={
          last && actions.onResume ? (
            <Button variant="outline" size="sm" onClick={actions.onResume} aria-label={`Resume: let ${agent} continue`}>
              Resume
            </Button>
          ) : null
        }
      >
        Stopped. This part isn&apos;t saved.
      </Notice>
    );
  } else if (a.state === "lost") {
    notice = (
      <Notice tone="bad" role={last ? "alert" : undefined}>
        Connection lost. The run was stopped on the server. Your messages and finished steps are saved; the unfinished
        part of this answer isn&apos;t.
      </Notice>
    );
  } else if (a.state === "failed" && a.error) {
    const e = runErrorText(a.error);
    const button =
      last && (e.action === "retry" || e.action === "resume") && actions.onResume ? (
        <Button variant="outline" size="sm" onClick={actions.onResume}>
          {e.action === "retry" ? "Retry" : "Resume"}
        </Button>
      ) : last && e.action === "new" && actions.onNewChat ? (
        <Button variant="outline" size="sm" onClick={actions.onNewChat}>
          New chat
        </Button>
      ) : null;
    notice = (
      <Notice tone="bad" role={last ? "alert" : undefined} actions={button}>
        {e.text}
      </Notice>
    );
  }
  return (
    <Message
      role="assistant"
      text={text}
      state={streaming ? "streaming" : "done"}
      runId={a.runId}
      body={<Parts parts={a.parts} displays={displays} running={streaming} surfaces={surfaces} />}
    >
      {streaming && a.parts.length === 0 ? (
        <div className="flex items-center gap-2 text-ak-muted">
          <Spinner className="size-3.5" />
          Thinking…
        </div>
      ) : null}
      {notice}
      {sources.length > 0 ? (
        <div className="flex flex-wrap gap-1.5" aria-label="Sources" role="list">
          {sources.map((u) => (
            <span key={u} role="listitem">
              <SourceChip url={u} />
            </span>
          ))}
        </div>
      ) : null}
      {streaming ? null : (
        <UsageLine
          {...(a.usage ? { usage: a.usage } : {})}
          {...(a.turns ? { turns: a.turns } : {})}
          {...(a.durationMs !== undefined ? { durationMs: a.durationMs } : {})}
          {...(a.model ? { model: a.model } : {})}
        />
      )}
    </Message>
  );
}

/** Follows new content only while the reader is at the bottom; otherwise offers Jump to latest. */
function useStickToBottom(ref: RefObject<HTMLDivElement | null>, content: unknown) {
  const [atBottom, setAtBottom] = useState(true);
  const stick = useRef(true);
  useLayoutEffect(() => {
    const el = ref.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  }, [ref, content]);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const onScroll = () => {
      const bottom = el.scrollHeight - el.scrollTop - el.clientHeight < 48;
      stick.current = bottom;
      setAtBottom(bottom);
    };
    el.addEventListener("scroll", onScroll, { passive: true });
    return () => el.removeEventListener("scroll", onScroll);
  }, [ref]);
  const jump = () => {
    const el = ref.current;
    if (!el) return;
    stick.current = true;
    setAtBottom(true);
    el.scrollTop = el.scrollHeight;
  };
  return { atBottom, jump };
}

export function Thread({
  turns,
  label,
  agent,
  displays,
  actions,
  after,
  ui,
  className,
  children,
}: {
  turns: Turn[];
  label: string;
  agent: string;
  displays: ReadonlyMap<string, ToolDisplay>;
  actions: AnswerActions;
  /** Under the last message (a pending message's busy line). */
  after?: ReactNode;
  /** The agent's surfaces: edits and actions. */
  ui?: ThreadUI;
  className?: string;
  /** Above the messages (the new chat's agent card). */
  children?: ReactNode;
}) {
  const scroller = useRef<HTMLDivElement>(null);
  const { atBottom, jump } = useStickToBottom(scroller, turns);
  const lastAnswer = turns.findLastIndex((t) => t.role === "assistant");
  const all = useMemo(() => foldSurfaces(turns), [turns]);
  // While a run goes on (or a message waits), surfaces wait too.
  const busy = turns.some((t) => (t.role === "assistant" ? t.state === "streaming" : t.pending === true));
  const surfaces: Surfaces = { all, ui, busy, agent };
  return (
    <div className="relative flex min-h-0 flex-1 flex-col">
      <div
        ref={scroller}
        role="log"
        aria-live="off"
        aria-label={label}
        className={cn("min-h-0 flex-1 overflow-auto", className)}
      >
        <div className="mx-auto flex max-w-(--ak-chat-w) flex-col gap-5">
          {children}
          {turns.map((t, i) =>
            t.role === "user" ? (
              <Message key={t.key} role="user" text={actionText(t, all)} state={t.pending ? "pending" : "done"} />
            ) : (
              <Answer
                key={t.key}
                a={t}
                agent={agent}
                displays={displays}
                last={i === lastAnswer && i === turns.length - 1}
                actions={actions}
                surfaces={surfaces}
              />
            ),
          )}
          {after}
        </div>
      </div>
      {atBottom ? null : (
        <button
          type="button"
          onClick={jump}
          className="absolute bottom-3 left-1/2 inline-flex h-8 -translate-x-1/2 items-center gap-1.5 rounded-full border border-ak-line-strong bg-ak-surface px-3 text-xs text-ak-text-2 shadow-ak hover:text-ak-text"
        >
          <ArrowDown aria-hidden="true" className="size-3.5" />
          Jump to latest
        </button>
      )}
    </div>
  );
}
