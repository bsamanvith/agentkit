import { Button } from "@agentkit/ui/components/button";
import { Skeleton } from "@agentkit/ui/components/skeleton";
import { cn } from "@agentkit/ui/lib/utils";
import { ChevronLeft, Menu, PanelLeft, type LucideIcon } from "lucide-react";
import { useEffect, useRef, type ReactNode } from "react";
import { Link } from "react-router";
import { ApiError, NetworkError } from "@agentkit/agent-ui/api";
import type { Resource } from "../api";
import { modHint, modKey, useLayout } from "../layout";

/** A 32 px icon button's look (the mockup's .icon-btn). */
export const iconButton =
  "group relative inline-grid size-8 flex-none place-items-center rounded-md text-ak-muted hover:bg-ak-hover hover:text-ak-text aria-expanded:bg-ak-select aria-expanded:text-ak-text";

/**
 * The head bar's first button: on a phone, Menu (opens the drawer); on a
 * laptop or desktop, the list toggle (Mod+Shift+S), in sections with a list.
 */
function ListToggle() {
  const { phone, hasList, listShown, toggleList, drawerOpen, openDrawer } = useLayout();
  if (phone) {
    return (
      <button
        type="button"
        aria-label="Menu"
        aria-expanded={drawerOpen}
        aria-controls="drawer"
        onClick={openDrawer}
        className={iconButton}
      >
        <Menu aria-hidden="true" className="size-4" />
      </button>
    );
  }
  if (!hasList) return null;
  return (
    <button
      type="button"
      aria-label="List"
      aria-expanded={listShown}
      aria-keyshortcuts={`${modKey}+Shift+S`}
      onClick={toggleList}
      className={iconButton}
    >
      <PanelLeft aria-hidden="true" className="size-4" />
      <span
        aria-hidden="true"
        className="pointer-events-none absolute top-9 left-0 z-30 rounded-sm bg-primary px-2 py-0.5 text-xs whitespace-nowrap text-primary-foreground opacity-0 transition-opacity group-hover:opacity-100 group-focus-visible:opacity-100"
      >
        {listShown ? "Hide" : "Show"} the list {modHint}⇧S
      </span>
    </button>
  );
}

/**
 * A page (docs/UI.md "Routes", the mockup's .head and .page): a head bar
 * with the list toggle, Back on a phone, the h1 and the page's status and
 * actions, then the body. The document title is "{title} · agentkit", and
 * the h1 gets focus when the page opens, so a screen reader hears where it
 * landed. With heading="body" the head shows the title as plain text and the
 * body brings its own h1 (an empty state).
 */
export function Page({
  title,
  children,
  aside,
  back,
  mono = false,
  muted = false,
  heading = "head",
  body = "page",
  wrap = false,
  lead,
  autoFocus = true,
}: {
  title: string;
  children: ReactNode;
  /** Status and actions, after the title. */
  aside?: ReactNode;
  /** Phone only: where Back goes, and what it's back to. */
  back?: { to: string; label: string };
  mono?: boolean;
  muted?: boolean;
  heading?: "head" | "body";
  /** "page": a scrolling body, 880 px at most; "bare": the children as they are. */
  body?: "page" | "bare";
  /** Phone: put the aside on a row of its own, under the title. */
  wrap?: boolean;
  /** Before the title: the chat's agent picker or chip. */
  lead?: ReactNode;
  /** Whether the h1 takes focus when the page opens (not when the composer should). */
  autoFocus?: boolean;
}) {
  const { phone } = useLayout();
  const wrapped = phone && wrap && aside !== undefined;
  const root = useRef<HTMLDivElement>(null);
  useEffect(() => {
    document.title = `${title} · agentkit`;
    if (!autoFocus) return;
    // Not out of an open menu or dialog: a page whose code arrives late
    // (React.lazy) would otherwise close the account menu just opened.
    if (document.activeElement?.closest('[role="menu"], [role="dialog"]')) return;
    root.current?.querySelector<HTMLElement>("h1")?.focus({ preventScroll: true });
  }, [title, autoFocus]);
  const titleClass = cn(
    "min-w-0 flex-1 truncate text-(length:--ak-text-md) font-medium text-ak-text outline-none",
    mono && "font-mono",
    muted && "text-ak-muted",
    wrapped && "basis-0",
  );
  return (
    <div ref={root} className="flex min-h-0 flex-1 flex-col">
      <div
        className={cn(
          "flex min-h-[52px] flex-none items-center gap-2 border-b py-2",
          phone ? "px-2" : "px-4",
          wrapped && "flex-wrap gap-y-1",
        )}
      >
        <ListToggle />
        {phone && back ? (
          <Link to={back.to} aria-label={`Back to ${back.label}`} className={iconButton}>
            <ChevronLeft aria-hidden="true" className="size-4" />
          </Link>
        ) : null}
        {lead}
        {heading === "head" ? (
          <h1 tabIndex={-1} className={titleClass}>
            {title}
          </h1>
        ) : (
          <span className={titleClass}>{title}</span>
        )}
        {wrapped ? <span aria-hidden="true" className="h-0 basis-full" /> : null}
        {aside}
      </div>
      {body === "page" ? (
        <div className="min-h-0 flex-1 overflow-auto">
          <div className={cn("flex max-w-(--ak-detail-w) flex-col gap-5", phone ? "p-4" : "p-6")}>{children}</div>
        </div>
      ) : (
        children
      )}
    </div>
  );
}

/** An empty state (the mockup's .empty): an icon, its h1 and a line. */
export function Empty({ icon: Icon, title, children }: { icon: LucideIcon; title: string; children?: ReactNode }) {
  return (
    <div className="m-auto flex max-w-[460px] flex-col items-center gap-2.5 px-4 py-6 text-center">
      <div className="grid size-10 place-items-center rounded-[10px] border bg-ak-surface-2 text-ak-muted">
        <Icon aria-hidden="true" className="size-4.5" />
      </div>
      <h1 tabIndex={-1} className="text-(length:--ak-text-lg) font-semibold text-ak-text outline-none">
        {title}
      </h1>
      {children}
    </div>
  );
}

/** A loading skeleton (docs/UI.md "Loading"): busy, with a hidden "Loading". */
export function Loading() {
  return (
    <div aria-busy="true" className="flex flex-col gap-2">
      <span className="sr-only">Loading</span>
      <Skeleton className="h-12 w-full" />
      <Skeleton className="h-12 w-full" />
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
  if (res.state === "loading") return <Loading />;
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
  if (error instanceof NetworkError) return "Couldn't reach the server.";
  return error.message;
}
