// What the signed-in caller may see, from GET /v1/auth/me. Only to hide
// controls: the API enforces every role (docs/UI.md "Rules"), so a wrong
// answer here shows a control that fails with 403, never data.

import type { Me } from "@agentkit/agent-ui/api";

/** Whether glob (path.Match's *, ? and [...]; Go's auth.Principal.HasScope) matches name. */
function matches(glob: string, name: string): boolean {
  let re = "";
  for (let i = 0; i < glob.length; i++) {
    const c = glob.charAt(i);
    if (c === "*") re += "[^/]*";
    else if (c === "?") re += "[^/]";
    else if (c === "[") {
      const end = glob.indexOf("]", i + 1);
      if (end < 0) return false; // a malformed pattern matches nothing, as in Go
      re += `[${glob.slice(i + 1, end)}]`; // Go's [^a-z] negates, as a RegExp's does
      i = end;
    } else re += c.replace(/[.+^${}()|\\\]]/g, "\\$&");
  }
  try {
    return new RegExp(`^${re}$`).test(name);
  } catch {
    return false;
  }
}

/** Whether the caller holds scope: one of its patterns matches it. */
export function hasScope(me: Me, scope: string): boolean {
  return me.scopes.some((p) => matches(p, scope));
}

/**
 * Whether the admin pages are for the caller: an admin (the admin role, or
 * the scope agent:admin, which "*" keys have too), or anyone on a server
 * without credentials.
 */
export function isAdmin(me: Me): boolean {
  return !me.credential || me.roles.includes("admin") || hasScope(me, "agent:admin");
}

/** Whether the caller may set the tenant's shared credentials (credentials:admin). */
export function canSetTenantCredentials(me: Me | null): boolean {
  return me !== null && (!me.credential || hasScope(me, "credentials:admin"));
}
