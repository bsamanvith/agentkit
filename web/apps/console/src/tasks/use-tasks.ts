import { createContext, useContext, useSyncExternalStore } from "react";
import type { TasksState, TaskStore } from "./store";

export const TasksContext = createContext<TaskStore | null>(null);

/** The store, or null outside a provider (a page rendered on its own in a test). */
export function useTaskStore(): TaskStore | null {
  return useContext(TasksContext);
}

const none: TasksState = { runs: null, error: null, unread: new Set(), toasts: [] };
const noSubscribe = () => () => {};

export function useTasks(): TasksState {
  const store = useTaskStore();
  return useSyncExternalStore(store?.subscribe ?? noSubscribe, store?.get ?? (() => none));
}
