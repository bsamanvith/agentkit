// The pages, fetched when first shown (React.lazy; web/README.md "Size"):
// the first load is the shell, which shows a skeleton until the page's
// chunk arrives. Admin pages share one chunk.

import { lazy } from "react";

const admin = () => import("./admin");

export const ChatPage = lazy(() => import("../chat/page").then((m) => ({ default: m.ChatPage })));
export const AgentsPage = lazy(() => admin().then((m) => ({ default: m.AgentsPage })));
export const AgentPage = lazy(() => admin().then((m) => ({ default: m.AgentPage })));
export const ToolsetsPage = lazy(() => admin().then((m) => ({ default: m.ToolsetsPage })));
export const ToolsetPage = lazy(() => admin().then((m) => ({ default: m.ToolsetPage })));
export const ConnectionsPage = lazy(() => admin().then((m) => ({ default: m.ConnectionsPage })));
export const NotFoundPage = lazy(() => admin().then((m) => ({ default: m.NotFoundPage })));
