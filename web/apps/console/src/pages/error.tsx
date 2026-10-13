import { Button } from "@agentkit/ui/components/button";
import { TriangleAlert } from "lucide-react";
import { useContext } from "react";
import { useRouteError } from "react-router";
import { LayoutContext } from "../layout";
import { Empty, Page } from "./page";

/**
 * A page's code that couldn't be fetched: the tab is older than the server,
 * which no longer has the old hashed chunks (pkg/web answers them 404).
 * Browsers word it differently; Vite's preload says "Unable to preload".
 */
function isStaleChunk(error: unknown): boolean {
  return (
    error instanceof Error &&
    /dynamically imported module|importing a module script failed|unable to preload/i.test(error.message)
  );
}

/**
 * A route's error element: a page that threw, or whose code didn't load.
 * In the shell it is a page (the rail, and on a phone the Menu, still work,
 * and going elsewhere clears it); when the shell itself threw, it stands
 * alone. Reload is the way out in both.
 */
export function RouteError() {
  const error = useRouteError();
  const inShell = useContext(LayoutContext) !== null;
  const stale = isStaleChunk(error);
  const title = stale ? "The console was updated" : "This page couldn't be shown";
  const empty = (
    <Empty icon={TriangleAlert} title={title}>
      <p className="text-ak-muted">
        {stale ? "Reload to get the new version." : "Something went wrong in the console. Reload to try again."}
      </p>
      {!stale && error instanceof Error ? <p className="text-ak-muted">{error.message}</p> : null}
      <Button variant="outline" size="sm" onClick={() => window.location.reload()}>
        Reload
      </Button>
    </Empty>
  );
  if (!inShell) return <main className="flex h-svh bg-ak-surface">{empty}</main>;
  return (
    <Page title={title} heading="body" body="bare">
      {empty}
    </Page>
  );
}
