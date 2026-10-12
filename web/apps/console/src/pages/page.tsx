import { Button } from "@agentkit/ui/components/button";
import { Skeleton } from "@agentkit/ui/components/skeleton";
import { useEffect, useRef, type ReactNode } from "react";
import { ApiError } from "@agentkit/agent-ui/api";
import type { Resource } from "../api";

/**
 * A page: its h1, the document title "{title} · agentkit", and focus on the
 * h1 when the page opens, so a screen reader hears where it landed
 * (docs/UI.md "Routes").
 */
export function Page({ title, children, actions }: { title: string; children: ReactNode; actions?: ReactNode }) {
  const h1 = useRef<HTMLHeadingElement>(null);
  useEffect(() => {
    document.title = `${title} · agentkit`;
    h1.current?.focus({ preventScroll: true });
  }, [title]);
  return (
    <div className="mx-auto flex w-full max-w-(--ak-detail-w) flex-1 flex-col gap-4 p-4 md:p-6">
      <div className="flex items-center gap-3">
        <h1 ref={h1} tabIndex={-1} className="text-xl font-semibold text-ak-text outline-none">
          {title}
        </h1>
        <div className="ml-auto flex gap-2">{actions}</div>
      </div>
      {children}
    </div>
  );
}

/** A resource's loading skeleton, its error with Retry, or its content. */
export function Loaded<T>({
  res,
  what,
  children,
}: {
  res: Resource<T>;
  what: string;
  children: (data: T) => ReactNode;
}) {
  if (res.state === "loading") {
    return (
      <div aria-busy="true" className="flex flex-col gap-2">
        <span className="sr-only">Loading</span>
        <Skeleton className="h-12 w-full" />
        <Skeleton className="h-12 w-full" />
      </div>
    );
  }
  if (res.state === "failed") {
    return (
      <div role="alert" className="flex flex-col gap-2 text-sm">
        <div className="flex items-center gap-3">
          <span>Couldn&apos;t load {what}.</span>
          <Button variant="outline" size="sm" onClick={res.retry}>
            Retry
          </Button>
        </div>
        <span className="text-ak-muted">{failureDetail(res.error)}</span>
      </div>
    );
  }
  return <>{children(res.data)}</>;
}

/**
 * What went wrong, for the line under "Couldn't load …" (docs/UI.md "Load
 * failed"): the server's message, or that it couldn't be reached.
 */
function failureDetail(error: Error): string {
  if (error instanceof ApiError) {
    const wait = error.retryAfter ? ` Try again in ${error.retryAfter} s.` : "";
    return `${error.message} (${error.status} ${error.code}).${wait}`;
  }
  if (error instanceof TypeError) return "Couldn't reach the server.";
  return error.message;
}
