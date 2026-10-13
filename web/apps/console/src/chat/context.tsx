import { useEffect, useState, type ReactNode } from "react";
import { useApi } from "../api";
import { Drafts } from "./drafts";
import { RunStore } from "./runs";
import { RunsContext } from "./use-runs";

/**
 * The run store, above the router so runs outlive pages, and the status
 * region that announces them (docs/UI.md "Accessibility", "Streaming").
 * While an answer streams, closing or reloading the tab asks first: it would
 * stop the run.
 */
export function RunsProvider({ children }: { children: ReactNode }) {
  const { unauthorized, hasToken } = useApi();
  const [said, setSaid] = useState("");
  const [store] = useState(() => new RunStore(new Drafts(), { unauthorized, announce: setSaid }));
  useEffect(() => store.setHooks({ unauthorized, announce: setSaid }), [store, unauthorized]);

  // Forget token clears everything the console holds (drafts, runs).
  useEffect(() => {
    if (!hasToken) store.clear();
  }, [store, hasToken]);

  useEffect(() => {
    const onLeave = (e: BeforeUnloadEvent) => {
      if (store.anyRunning()) e.preventDefault();
    };
    window.addEventListener("beforeunload", onLeave);
    return () => window.removeEventListener("beforeunload", onLeave);
  }, [store]);

  return (
    <RunsContext.Provider value={store}>
      {children}
      <div role="status" className="sr-only">
        {said}
      </div>
    </RunsContext.Provider>
  );
}
