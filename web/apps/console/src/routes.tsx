import type { RouteObject } from "react-router";
import { RouteError } from "./pages/error";
import {
  AgentPage,
  AgentsPage,
  ChatPage,
  ConnectionsPage,
  NotFoundPage,
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

// Every view has a URL (docs/UI.md "Routes"). The server answers every
// extension-less path with index.html, so these work on reload.
export const routes: RouteObject[] = inShell([
  { path: "/", element: <ChatPage /> },
  { path: "/new", element: <ChatPage /> },
  { path: "/c/:id", element: <ChatPage /> },
  { path: "/agents", element: <AgentsPage /> },
  { path: "/agents/:name", element: <AgentPage /> },
  { path: "/toolsets", element: <ToolsetsPage /> },
  { path: "/toolsets/:name", element: <ToolsetPage /> },
  { path: "/connections", element: <ConnectionsPage /> },
  { path: "*", element: <NotFoundPage /> },
]);
