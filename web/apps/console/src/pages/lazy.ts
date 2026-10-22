// The pages, fetched when first shown (React.lazy; web/README.md "Size"):
// the first load is the shell, which shows a skeleton until the page's
// chunk arrives. Admin pages share one chunk.

import { lazy } from "react";

const admin = () => import("./admin");
const tasks = () => import("../tasks/pages");

export const ChatPage = lazy(() => import("../chat/page").then((m) => ({ default: m.ChatPage })));
export const AgentsPage = lazy(() => admin().then((m) => ({ default: m.AgentsPage })));
export const AgentPage = lazy(() => admin().then((m) => ({ default: m.AgentPage })));
export const ToolsetsPage = lazy(() => admin().then((m) => ({ default: m.ToolsetsPage })));
export const ToolsetPage = lazy(() => admin().then((m) => ({ default: m.ToolsetPage })));
export const McpAddPage = lazy(() => admin().then((m) => ({ default: m.McpAddPage })));
export const ReviewPage = lazy(() => admin().then((m) => ({ default: m.ReviewPage })));
export const ConnectionsPage = lazy(() => admin().then((m) => ({ default: m.ConnectionsPage })));
export const TasksPage = lazy(() => tasks().then((m) => ({ default: m.TasksPage })));
export const RunPage = lazy(() => tasks().then((m) => ({ default: m.RunPage })));
export const ThreadPage = lazy(() => tasks().then((m) => ({ default: m.ThreadPage })));
export const TriggersPage = lazy(() => tasks().then((m) => ({ default: m.TriggersPage })));
/** The list column of Tasks: in the same chunk as its pages. */
export const TasksList = lazy(() => tasks().then((m) => ({ default: m.TasksList })));
export const ConnectDonePage = lazy(() => admin().then((m) => ({ default: m.ConnectDonePage })));
export const NotFoundPage = lazy(() => admin().then((m) => ({ default: m.NotFoundPage })));
