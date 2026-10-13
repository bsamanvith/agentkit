import type { RouteObject } from "react-router";
import { AgentPage, AgentsPage, ConnectionsPage, NotFoundPage, ToolsetPage, ToolsetsPage } from "./pages/admin";
import { ChatPage } from "./pages/chat";
import { Shell } from "./shell";

// Every view has a URL (docs/UI.md "Routes"). The server answers every
// extension-less path with index.html, so these work on reload.
export const routes: RouteObject[] = [
  {
    element: <Shell />,
    children: [
      { path: "/", element: <ChatPage /> },
      { path: "/new", element: <ChatPage /> },
      { path: "/c/:id", element: <ChatPage /> },
      { path: "/agents", element: <AgentsPage /> },
      { path: "/agents/:name", element: <AgentPage /> },
      { path: "/toolsets", element: <ToolsetsPage /> },
      { path: "/toolsets/:name", element: <ToolsetPage /> },
      { path: "/connections", element: <ConnectionsPage /> },
      { path: "*", element: <NotFoundPage /> },
    ],
  },
];
