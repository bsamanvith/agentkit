import { createContext, useContext, useSyncExternalStore } from "react";
import type { ConversationState, RunStore } from "./runs";

export const RunsContext = createContext<RunStore | null>(null);

export function useRuns(): RunStore {
  const store = useContext(RunsContext);
  if (!store) throw new Error("useRuns needs a <RunsProvider> above it");
  return store;
}

/** A conversation's runs, drafts and errors, re-rendering when they change. */
export function useConversationState(id: string): ConversationState {
  const store = useRuns();
  return useSyncExternalStore(store.subscribe, () => store.get(id));
}

/** A draft, by conversation ("new" for New chat). */
export function useDraft(key: string): [string, (text: string) => void] {
  const { drafts } = useRuns();
  const text = useSyncExternalStore(drafts.subscribe, () => drafts.get(key));
  return [text, (t: string) => drafts.set(key, t)];
}
