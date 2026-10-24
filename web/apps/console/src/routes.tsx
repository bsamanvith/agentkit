import { Suspense } from "react";
import type { RouteObject } from "react-router";
import { RouteError } from "./pages/error";
import {
  TasksPage,
  AgentBuilderPage,
  AgentPage,
  AgentVersionsPage,
  AgentsPage,
  ChatPage,
  ConnectDonePage,
  ConnectionsPage,
  McpAddPage,
  NotFoundPage,
  ReviewPage,
  RunPage,
  ThreadPage,
  TriggersPage,
  ToolsetPage,
  ToolsetsPage,
} from "./pages/lazy";
import { Shell } from "./shell";

/**
 * The shell around pages. A page that throws shows the error in the shell
 * (the pathless route's errorElement renders in its outlet), so the rail
 * stays; the outer one is for the shell itself.
 */
export function inShell(pages: RouteObject[]): RouteObject[] {
  return [
    { element: <Shell />, errorElement: <RouteError />, children: [{ errorElement: <RouteError />, children: pages }] },
  ];
}

// Where a connect flow comes back from the provider (often in a popup): no shell.
const connectDone: RouteObject = {
  path: "/connections/done",
  errorElement: <RouteError />,
  element: (
    <Suspense fallback={null}>
      <ConnectDonePage />
    </Suspense>
  ),
};

// Every view has a URL (docs/UI.md "Routes"). The server answers every
// extension-less path with index.html, so these work on reload.
export const routes: RouteObject[] = inShell([
  { path: "/", element: <ChatPage /> },
  { path: "/new", element: <ChatPage /> },
  { path: "/c/:id", element: <ChatPage /> },
  { path: "/agents", element: <AgentsPage /> },
  { path: "/agents/new", element: <AgentBuilderPage /> },
  { path: "/agents/:name", element: <AgentPage /> },
  { path: "/agents/:name/edit", element: <AgentBuilderPage /> },
  { path: "/agents/:name/versions", element: <AgentVersionsPage /> },
  { path: "/agents/:name/versions/:v", element: <AgentVersionsPage /> },
  { path: "/toolsets", element: <ToolsetsPage /> },
  { path: "/toolsets/new", element: <McpAddPage /> },
  { path: "/toolsets/:name", element: <ToolsetPage /> },
  { path: "/toolsets/:name/review", element: <ReviewPage /> },
  { path: "/connections", element: <ConnectionsPage /> },
  { path: "/tasks", element: <TasksPage /> },
  { path: "/tasks/threads", element: <TasksPage /> },
  { path: "/tasks/triggers", element: <TriggersPage /> },
  { path: "/tasks/:id", element: <RunPage /> },
  { path: "/tasks/threads/:id", element: <ThreadPage /> },
  { path: "*", element: <NotFoundPage /> },
]).concat(connectDone);
