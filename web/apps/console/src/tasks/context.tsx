import { X } from "lucide-react";
import { useEffect, useEffectEvent, useRef, useState, type ReactNode } from "react";
import { Link } from "react-router";
import { useApi } from "../api";
import { TaskStore, toastMs, type Toast } from "./store";
import { TasksContext, useTasks, useTaskStore } from "./use-tasks";

/**
 * The background runs store, above the router like the chat runs. It loads
 * while someone is signed in (no request loop on a 401: it waits for the
 * card), and is cleared on sign out.
 */
export function TasksProvider({ children, every }: { children: ReactNode; every?: number }) {
  const { client, unauthorized, accepted, prompt, epoch, cleared } = useApi();
  const [store] = useState(() => new TaskStore({ client: () => client, unauthorized, accepted }, every));
  useEffect(
    () => store.setHooks({ client: () => client, unauthorized, accepted }),
    [store, client, unauthorized, accepted],
  );

  const seen = useRef(cleared);
  useEffect(() => {
    if (cleared === seen.current) return;
    seen.current = cleared;
    store.clear();
  }, [store, cleared]);

  useEffect(() => {
    if (prompt !== null) return;
    store.start();
    return () => store.stop();
  }, [store, prompt, epoch]);

  return <TasksContext.Provider value={store}>{children}</TasksContext.Provider>;
}

function ToastItem({ toast, dismiss }: { toast: Toast; dismiss: () => void }) {
  const [held, setHeld] = useState(false);
  const expire = useEffectEvent(dismiss);
  useEffect(() => {
    if (held) return;
    const t = setTimeout(() => expire(), toastMs);
    return () => clearTimeout(t);
  }, [held]);
  return (
    <li
      onPointerEnter={() => setHeld(true)}
      onPointerLeave={() => setHeld(false)}
      onFocus={() => setHeld(true)}
      onBlur={() => setHeld(false)}
      className="pointer-events-auto flex items-center gap-3 rounded-md border border-ak-line-strong bg-ak-surface px-3 py-2 text-(length:--ak-text-sm) text-ak-text shadow-ak"
    >
      <span className="min-w-0 flex-1">{toast.text}</span>
      <Link
        to={`/tasks/${encodeURIComponent(toast.runId)}`}
        onClick={dismiss}
        className="font-medium underline-offset-4 hover:underline"
      >
        Open
      </Link>
      <button
        type="button"
        aria-label="Dismiss"
        onClick={dismiss}
        className="grid size-6 place-items-center rounded-sm text-ak-muted hover:bg-ak-hover hover:text-ak-text"
      >
        <X aria-hidden="true" className="size-3.5" />
      </button>
    </li>
  );
}

/**
 * The notifications of background runs: quiet toasts at the bottom right
 * (bottom on phones), announced politely. Never the only way to a run: the
 * badge and the unread marks stay.
 */
export function Toasts() {
  const store = useTaskStore();
  const { toasts } = useTasks();
  return (
    <div
      role="status"
      aria-label="Notifications"
      className="pointer-events-none fixed right-4 bottom-4 z-50 w-[min(380px,calc(100%-32px))]"
    >
      <ul className="flex flex-col gap-2">
        {toasts.map((t) => (
          <ToastItem key={t.key} toast={t} dismiss={() => store?.dismiss(t.key)} />
        ))}
      </ul>
    </div>
  );
}
