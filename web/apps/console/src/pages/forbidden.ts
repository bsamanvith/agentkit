// The 403 page's condition, shared by the admin pages.

import { ApiError } from "@agentkit/agent-ui/api";
import { isAdmin } from "../access";
import { useApi, type Resource } from "../api";

/**
 * Whether to show the 403 page instead: the caller is known not to be an
 * admin, or the API refused the list (a stale tab after a role change). A
 * CSRF refusal is never this (the client retries it).
 */
export function useForbidden(res: Resource<unknown> | null): boolean {
  const { me } = useApi();
  if (me && !isAdmin(me)) return true;
  return res?.state === "failed" && res.error instanceof ApiError && res.error.status === 403;
}
