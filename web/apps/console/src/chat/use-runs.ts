import { createContext, useContext, useSyncExternalStore } from "react";
import type { ConversationState, RunStore } from "./runs";
import type { Choice } from "./tool-choices";

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

/** A chat's tool choice (T115), by conversation ("new" for New chat). */
export function useToolChoice(key: string): [Choice, (c: Choice) => void] {
  const { choices } = useRuns();
  const choice = useSyncExternalStore(choices.subscribe, () => choices.get(key));
  return [choice, (c: Choice) => choices.set(key, c)];
}
