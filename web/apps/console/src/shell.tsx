// The app shell (docs/UI.md "Layouts", and the mockup docs/ui/console-mockup.html):
//
// - desktop and laptop: a 56 px rail of sections, the section's list column
//   (288 px, 248 px below 1200 px; Mod+Shift+S or the head bar's toggle
//   hides it, and the choice is remembered), and the content;
// - phone (< 768 px): the content with a top bar; its Menu button opens a
//   drawer with the sections as a grid of icon and label, the Chats list and
//   the account. Picking anything closes it.
//
// The drawer is ours, not shadcn's Sheet: Radix's modal scroll lock injects a
// <style>, which the CSP blocks. The page doesn't scroll behind it anyway (the
// shell is the height of the screen; only its panes scroll), the app behind
// is inert while it is open, and Tab stays in it.

import type { Me } from "@agentkit/agent-ui/api";
import { isPreference, useTheme, themePreferences, type ThemePreference } from "@agentkit/ui";
import { Button } from "@agentkit/ui/components/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@agentkit/ui/components/dropdown-menu";
import { Skeleton } from "@agentkit/ui/components/skeleton";
import { useIsMobile } from "@agentkit/ui/hooks/use-mobile";
import { cn } from "@agentkit/ui/lib/utils";
import { Ellipsis, Plus, SquarePen, UserRound } from "lucide-react";
import {
  Suspense,
  useCallback,
  useContext,
  useEffect,
  useEffectEvent,
  useMemo,
  useRef,
  useState,
  type MouseEvent,
  type ReactNode,
} from "react";
import { Link, Outlet, useLocation, useNavigate } from "react-router";
import { Toasts } from "./tasks/context";
import { waitingCount } from "./tasks/store";
import { useTasks } from "./tasks/use-tasks";
import { useApi, useResource, type Resource } from "./api";
import { ChatsList } from "./chat/list";
import { RunsContext } from "./chat/use-runs";
import { composerId, searchId } from "./chat/shared";
import { LayoutContext, isApple, listStorageKey, modHint, modKey, type Layout } from "./layout";
import { ListRow } from "./list-row";
import { TasksList } from "./pages/lazy";
import { Loading } from "./pages/page";
import { agentRow, keepsAgents, listAgents, ListsContext, listToolsets, toolsetRow, type Lists } from "./lists";
import { tasks, adminSections, mayAdmin, sectionOf, sections, type Section } from "./sections";

const themeLabels: Record<ThemePreference, string> = { system: "System", light: "Light", dark: "Dark" };

// Below 900 px the list starts hidden (the content would be under 600 px),
// unless you showed it (docs/UI.md "Layouts", laptop).
function initialListShown(): boolean {
  try {
    const v = localStorage.getItem(listStorageKey);
    if (v === "shown" || v === "hidden") return v === "shown";
  } catch {
    // Storage blocked: use the width.
  }
  return window.innerWidth >= 900;
}

// What Tab can reach in the drawer. It has no hidden or disabled controls.
const focusable = 'a[href], button:not(:disabled), input:not(:disabled), [tabindex]:not([tabindex="-1"])';

/**
 * Keeps Tab in the open drawer (it is modal): from its last control to its
 * first and back, and from outside it (focus on <body> after a tap on its
 * padding) to its first or last.
 */
function trapTab(e: globalThis.KeyboardEvent, root: HTMLElement) {
  const items = root.querySelectorAll<HTMLElement>(focusable);
  const first = items[0];
  const last = items[items.length - 1];
  if (!first || !last) return;
  const active = document.activeElement;
  let to: HTMLElement | null = null;
  if (!root.contains(active)) to = e.shiftKey ? last : first;
  else if (e.shiftKey && active === first) to = last;
  else if (!e.shiftKey && active === last) to = first;
  if (to) {
    e.preventDefault();
    to.focus();
  }
}

const tip =
  "pointer-events-none absolute z-30 rounded-sm bg-primary px-2 py-0.5 text-xs whitespace-nowrap text-primary-foreground opacity-0 transition-opacity group-hover:opacity-100 group-focus-visible:opacity-100";

/** Who is signed in, for the account menu: name, role and tenant (docs/UI.md "Sign in"). */
function whoLabel(me: Me): string {
  if (!me.credential) return "Local, no sign-in";
  const role = me.roles.includes("admin") ? "admin" : me.roles[0];
  const name = me.username || (me.credential === "api_key" ? "API token" : me.credential);
  return [name, role, me.tenant].filter(Boolean).join(" · ");
}

/** The account menu: who you are, the theme, and Sign out or Forget token. */
function UserMenu({ variant }: { variant: "rail" | "drawer" }) {
  const { preference, setPreference } = useTheme();
  const { hasToken, forgetToken, me, loadMe, signOut } = useApi();
  const runs = useContext(RunsContext);
  const session = me?.credential === "session";
  // Signing out stops a running answer: ask first (docs/UI.md "Sign in").
  const leave = (go: () => void) => () => {
    if (runs?.anyRunning() && !window.confirm("An answer is running. Signing out stops it. Sign out anyway?")) return;
    go();
  };
  // Not modal: a modal menu locks scrolling with an injected <style>, which
  // the CSP blocks (no inline styles).
  return (
    <DropdownMenu modal={false} onOpenChange={(open) => open && loadMe()}>
      <DropdownMenuTrigger asChild>
        {variant === "rail" ? (
          <button
            type="button"
            aria-label="Account and theme"
            className="grid size-10 place-items-center rounded-md hover:bg-ak-hover"
          >
            <span className="grid size-7 place-items-center rounded-full bg-ak-select text-ak-text">
              <UserRound aria-hidden="true" className="size-3.5" />
            </span>
          </button>
        ) : (
          <button
            type="button"
            aria-label="Account and theme"
            className="grid size-8 place-items-center rounded-md text-ak-muted hover:bg-ak-hover hover:text-ak-text"
          >
            <Ellipsis aria-hidden="true" className="size-4" />
          </button>
        )}
      </DropdownMenuTrigger>
      <DropdownMenuContent side={variant === "rail" ? "right" : "top"} align="end" className="w-56">
        {me ? (
          <>
            <DropdownMenuLabel className="truncate font-normal text-ak-text">{whoLabel(me)}</DropdownMenuLabel>
            <DropdownMenuSeparator />
          </>
        ) : null}
        <DropdownMenuLabel className="text-ak-muted">Theme</DropdownMenuLabel>
        <DropdownMenuRadioGroup value={preference} onValueChange={(v) => isPreference(v) && setPreference(v)}>
          {themePreferences.map((p) => (
            <DropdownMenuRadioItem key={p} value={p}>
              {themeLabels[p]}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
        {session ? (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem onSelect={leave(() => void signOut())}>Sign out</DropdownMenuItem>
          </>
        ) : hasToken ? (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem onSelect={leave(forgetToken)}>Forget token</DropdownMenuItem>
          </>
        ) : null}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/** A section's badge: Tasks counts the tasks waiting for you (docs/UI.md "Tasks"). */
function useBadge(section: Section): number {
  const { runs } = useTasks();
  return section === tasks ? waitingCount(runs) : 0;
}

function Badge({ n, className }: { n: number; className?: string }) {
  if (n === 0) return null;
  return (
    <span
      aria-hidden="true"
      className={cn(
        "absolute grid h-4 min-w-4 place-items-center rounded-full bg-primary px-1 text-[10px] leading-none font-medium text-primary-foreground tabular-nums",
        className,
      )}
    >
      {n > 9 ? "9+" : n}
    </span>
  );
}

function RailLink({ section, current }: { section: Section; current: boolean }) {
  const Icon = section.icon;
  const badge = useBadge(section);
  const label = badge > 0 ? `${section.label}, ${badge} waiting for you` : section.label;
  return (
    <Link
      to={section.to}
      aria-label={label}
      aria-current={current ? "page" : undefined}
      className="group relative grid size-10 place-items-center rounded-md text-ak-muted hover:bg-ak-hover hover:text-ak-text aria-[current=page]:bg-ak-select aria-[current=page]:text-ak-text"
    >
      <Icon aria-hidden="true" className="size-4.5" />
      <Badge n={badge} className="top-0.5 right-0.5" />
      <span aria-hidden="true" className={cn(tip, "top-1/2 left-12 -translate-y-1/2")}>
        {section.label}
      </span>
    </Link>
  );
}

/** Laptop and desktop: the rail. */
function Rail({ current }: { current: Section }) {
  const admin = mayAdmin(useApi().me);
  return (
    <nav aria-label="Sections" className="flex min-h-0 flex-col items-center gap-1 border-r bg-ak-bg py-3">
      <div
        aria-hidden="true"
        className="mb-2.5 grid size-7 flex-none place-items-center rounded-[7px] bg-primary font-mono text-xs font-medium text-primary-foreground"
      >
        ak
      </div>
      {sections.map((s) => (
        <RailLink key={s.to} section={s} current={s === current} />
      ))}
      {admin ? (
        <>
          <div role="separator" className="my-1.5 h-px w-6 flex-none bg-ak-line-strong" />
          {adminSections.map((s) => (
            <RailLink key={s.to} section={s} current={s === current} />
          ))}
        </>
      ) : null}
      <div className="flex-1" />
      <UserMenu variant="rail" />
    </nav>
  );
}

/** A list column's head: the section's name, and a count or an action. */
function ListHead({ title, extra, below }: { title: string; extra?: ReactNode; below?: ReactNode }) {
  return (
    <div className="flex flex-col gap-2 px-3 pt-3 pb-2">
      <div className="flex min-h-7 items-center justify-between gap-2 px-1">
        <h2 className="text-(length:--ak-text-md) font-semibold text-ak-text">{title}</h2>
        {extra}
      </div>
      {below}
    </div>
  );
}

/** Toolsets' list head: Add an MCP server (admins; the API checks). */
function AddServerButton() {
  return (
    <Link
      to="/toolsets/new"
      aria-label="Add an MCP server"
      className="group relative grid size-8 place-items-center rounded-md text-ak-muted hover:bg-ak-hover hover:text-ak-text"
    >
      <Plus aria-hidden="true" className="size-4" />
      <span aria-hidden="true" className={cn(tip, "top-9 right-0")}>
        Add an MCP server
      </span>
    </Link>
  );
}

/** Agents' list head: New agent, on a server that keeps agents (the API checks). */
function NewAgentButton() {
  return (
    <Link
      to="/agents/new"
      aria-label="New agent"
      className="group relative grid size-8 place-items-center rounded-md text-ak-muted hover:bg-ak-hover hover:text-ak-text"
    >
      <Plus aria-hidden="true" className="size-4" />
      <span aria-hidden="true" className={cn(tip, "top-9 right-0")}>
        New agent
      </span>
    </Link>
  );
}

function NewChatButton() {
  return (
    <Link
      to="/new"
      state={{ focus: "composer" }}
      aria-label="New chat"
      aria-keyshortcuts={`${modKey}+Shift+O`}
      className="group relative grid size-8 place-items-center rounded-md text-ak-muted hover:bg-ak-hover hover:text-ak-text"
    >
      <SquarePen aria-hidden="true" className="size-4" />
      <span aria-hidden="true" className={cn(tip, "top-9 right-0")}>
        New chat {modHint}⇧O
      </span>
    </Link>
  );
}

/** Chats: the caller's conversations (chat/list.tsx). */
function Chats() {
  return <ChatsList head={(search) => <ListHead title="Chats" extra={<NewChatButton />} below={search} />} />;
}

function ListBody<T>({
  label,
  res,
  empty,
  children,
}: {
  label: string;
  res: Resource<T[]>;
  empty: string;
  children: (item: T) => ReactNode;
}) {
  let body: ReactNode;
  if (res.state === "loading") {
    body = (
      <div aria-busy="true" className="flex flex-col gap-3 px-2 py-2">
        <span className="sr-only">Loading</span>
        {["w-3/5", "w-1/2", "w-2/3"].map((w) => (
          <div key={w} className="flex flex-col gap-1.5">
            <Skeleton className={cn("h-3", w)} />
            <Skeleton className="h-2.5 w-1/3" />
          </div>
        ))}
      </div>
    );
  } else if (res.state === "failed") {
    // Not an alert: the page says it too, with the reason.
    body = (
      <div className="flex items-center gap-2 px-2 py-2 text-(length:--ak-text-sm) text-ak-muted">
        Couldn&apos;t load {label.toLowerCase()}.
        <Button variant="outline" size="sm" onClick={res.retry}>
          Retry
        </Button>
      </div>
    );
  } else if (res.data.length === 0) {
    body = <p className="px-2 py-2 text-(length:--ak-text-sm) text-ak-muted">{empty}</p>;
  } else {
    body = res.data.map(children);
  }
  return (
    <nav aria-label={label} className="min-h-0 flex-1 overflow-auto px-2 pb-3">
      {body}
    </nav>
  );
}

const count = (res: Resource<unknown[]>) => (
  <span className="text-xs text-ak-muted">{res.state === "ready" ? res.data.length : ""}</span>
);

/** The item in the URL, "researcher" in /agents/researcher; "" when none or malformed. */
function selectedName(pathname: string): string {
  try {
    return decodeURIComponent(pathname.split("/")[2] ?? "");
  } catch {
    return "";
  }
}

/** Laptop and desktop: the section's list. */
function ListColumn({ current, lists }: { current: Section; lists: Lists }) {
  const selected = selectedName(useLocation().pathname);
  const admin = mayAdmin(useApi().me);
  let content: ReactNode;
  if (current.to === "/agents" && lists.agents) {
    content = (
      <>
        <ListHead
          title="Agents"
          extra={
            <span className="flex items-center gap-1">
              {count(lists.agents)}
              {admin && lists.agents.state === "ready" && keepsAgents(lists.agents.data) ? <NewAgentButton /> : null}
            </span>
          }
        />
        <ListBody label="Agents" res={lists.agents} empty="No agents are configured.">
          {(a) => (
            <ListRow
              key={a.name}
              to={`/agents/${encodeURIComponent(a.name)}`}
              current={a.name === selected}
              {...agentRow(a)}
            />
          )}
        </ListBody>
      </>
    );
  } else if (current.to === "/toolsets" && lists.toolsets) {
    content = (
      <>
        <ListHead
          title="Toolsets"
          extra={
            <span className="flex items-center gap-1">
              {count(lists.toolsets)}
              {admin ? <AddServerButton /> : null}
            </span>
          }
        />
        <ListBody label="Toolsets" res={lists.toolsets} empty="No toolsets are configured.">
          {(t) => (
            <ListRow
              key={t.name}
              to={`/toolsets/${encodeURIComponent(t.name)}`}
              current={t.name === selected}
              {...toolsetRow(t)}
            />
          )}
        </ListBody>
      </>
    );
  } else if (current === tasks) {
    content = (
      <Suspense
        fallback={
          <div className="p-3">
            <Loading />
          </div>
        }
      >
        <TasksList />
      </Suspense>
    );
  } else {
    content = <Chats />;
  }
  return <div className="flex min-h-0 min-w-0 flex-col border-r bg-ak-bg">{content}</div>;
}

function DrawerLink({ section, current }: { section: Section; current: boolean }) {
  const Icon = section.icon;
  const badge = useBadge(section);
  return (
    <Link
      to={section.to}
      aria-current={current ? "page" : undefined}
      className="relative flex min-h-11 flex-col items-center justify-center gap-0.5 rounded-md px-1 py-1.5 text-center text-xs text-ak-muted aria-[current=page]:bg-ak-select aria-[current=page]:text-ak-text"
    >
      <Icon aria-hidden="true" className="size-4" />
      {section.label}
      {badge > 0 ? <span className="sr-only">, {badge} waiting for you</span> : null}
      <Badge n={badge} className="top-1 right-[calc(50%-20px)]" />
    </Link>
  );
}

/** Phone: the drawer. */
function Drawer({ open, current, close }: { open: boolean; current: Section; close: (returnFocus: boolean) => void }) {
  const admin = mayAdmin(useApi().me);
  // Picking anything in it (a section, a conversation, New chat) closes it.
  const onClick = (e: MouseEvent) => {
    if ((e.target as Element).closest("a")) close(false);
  };
  return (
    <>
      {open ? <div aria-hidden="true" className="fixed inset-0 z-30 bg-black/30" onClick={() => close(true)} /> : null}
      <div
        id="drawer"
        role="dialog"
        aria-modal="true"
        aria-label="Menu"
        inert={!open}
        onClick={onClick}
        className={cn(
          "fixed inset-y-0 left-0 z-40 flex w-[min(320px,86%)] flex-col border-r border-ak-line-strong bg-ak-bg transition-transform duration-(--ak-slow) ease-ak",
          open ? "translate-x-0 shadow-ak" : "-translate-x-[102%]",
        )}
      >
        <nav aria-label="Sections" className="grid grid-cols-3 gap-0.5 px-3 pt-3 pb-0.5">
          {[...sections, ...(admin ? adminSections : [])].map((s) => (
            <DrawerLink key={s.to} section={s} current={s === current} />
          ))}
        </nav>
        {/* On phones the drawer always holds the Chats list; admin lists are pages. */}
        <Chats />
        <div className="flex items-center gap-2.5 border-t px-4 pt-2.5 pb-[calc(10px+env(safe-area-inset-bottom,0px))]">
          <span className="grid size-7 place-items-center rounded-full bg-ak-select text-ak-text">
            <UserRound aria-hidden="true" className="size-3.5" />
          </span>
          <span className="flex-1 text-(length:--ak-text-sm) text-ak-text-2">Account</span>
          <UserMenu variant="drawer" />
        </div>
      </div>
    </>
  );
}

/** The pages that take the list column's place too (docs/UI.md "Routes"). */
const fullWidth = /^\/agents\/(new|[^/]+\/(edit|versions)(\/[^/]*)?)\/?$/;

export function Shell({ children }: { children?: ReactNode }) {
  const { pathname } = useLocation();
  const current = sectionOf(pathname);
  const phone = useIsMobile();
  // Connections is one page; the agent builder and versions are full width.
  const hasList = current.to !== "/connections" && !fullWidth.test(pathname);
  const [listShown, setListShown] = useState(initialListShown);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const { prompt, me, loadMe } = useApi();
  // Who is signed in is read once per sign-in (the API provider); again when
  // the tab regains focus, so a role change shows up (docs/UI.md "403").
  useEffect(() => {
    if (prompt !== null) return;
    const onFocus = () => loadMe();
    window.addEventListener("focus", onFocus);
    return () => window.removeEventListener("focus", onFocus);
  }, [prompt, loadMe]);
  // The admin sections (Rail, Drawer: mayAdmin) show only once /me says the
  // caller is an admin (docs/UI.md "Rules"). A direct link still loads its
  // list until the caller is known not to be one; the page shows the 403 then.
  const mayList = me === null || mayAdmin(me);
  const menuButton = useRef<HTMLElement | null>(null);
  const drawer = useRef<HTMLDivElement>(null);
  const navigate = useNavigate();

  const [agentsLoad, setAgentsLoad] = useState(0);
  const agents = useResource(listAgents, `agents\n${agentsLoad}`, current.to !== "/agents" || !mayList);
  const [toolsetsLoad, setToolsetsLoad] = useState(0);
  const toolsets = useResource(listToolsets, `toolsets\n${toolsetsLoad}`, current.to !== "/toolsets" || !mayList);
  const lists = useMemo<Lists>(
    () => ({
      agents: current.to === "/agents" && mayList ? agents : null,
      toolsets: current.to === "/toolsets" && mayList ? toolsets : null,
      reloadAgents: () => setAgentsLoad((n) => n + 1),
      reloadToolsets: () => setToolsetsLoad((n) => n + 1),
    }),
    [current, mayList, agents, toolsets],
  );

  const toggleList = useCallback(() => {
    setListShown((shown) => {
      try {
        localStorage.setItem(listStorageKey, shown ? "hidden" : "shown");
      } catch {
        // Storage blocked: remembered for this page only.
      }
      return !shown;
    });
  }, []);
  const openDrawer = useCallback(() => {
    menuButton.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    setDrawerOpen(true);
    // Focus where you are, not the search, so the keyboard doesn't open.
    requestAnimationFrame(() => drawer.current?.querySelector<HTMLElement>("a[aria-current], a")?.focus());
  }, []);
  const closeDrawer = useCallback((returnFocus: boolean) => {
    setDrawerOpen(false);
    // After the render: until then the app behind is inert and can't take focus.
    if (returnFocus) requestAnimationFrame(() => menuButton.current?.focus());
  }, []);

  const onKey = useEffectEvent((e: globalThis.KeyboardEvent) => {
    // The sign-in card is a dialog over the app: only its own keys work.
    if (prompt !== null) return;
    if (e.key === "Tab" && phone && drawerOpen) {
      // Radix keeps Tab in an open menu (preventDefault); leave it there.
      if (!e.defaultPrevented && drawer.current) trapTab(e, drawer.current);
      return;
    }
    // Esc closes the drawer wherever focus is (a tap on its padding moves
    // focus to <body>); in the account menu it closes the menu only
    // (docs/UI.md: the topmost first).
    if (e.key === "Escape" && phone && drawerOpen) {
      if (!(e.target instanceof Element && e.target.closest("[role=menu]"))) closeDrawer(true);
      return;
    }
    const mod = isApple ? e.metaKey : e.ctrlKey;
    if (!mod || e.altKey || e.repeat) return;
    if (e.shiftKey && e.code === "KeyO") {
      e.preventDefault();
      if (phone && drawerOpen) closeDrawer(false);
      navigate("/new", { state: { focus: "composer" } });
      return;
    }
    if (!e.shiftKey && e.code === "KeyK") {
      // Search conversations: the Chats list, wherever it is.
      e.preventDefault();
      if (phone) {
        if (!drawerOpen) openDrawer();
      } else {
        if (current.to !== "/") navigate("/");
        if (!listShown) toggleList();
      }
      setTimeout(() => {
        const box = (phone ? drawer.current : document)?.querySelector<HTMLInputElement>(`#${searchId}`);
        box?.focus();
      }, 50);
      return;
    }
    if (e.shiftKey && e.key.toLowerCase() === "s") {
      e.preventDefault();
      if (phone) {
        if (drawerOpen) closeDrawer(true);
        else openDrawer();
      } else if (hasList) toggleList();
    }
  });
  useEffect(() => {
    const listener = (e: globalThis.KeyboardEvent) => onKey(e);
    window.addEventListener("keydown", listener);
    return () => window.removeEventListener("keydown", listener);
  }, []);

  const layout = useMemo<Layout>(
    () => ({ phone, hasList, listShown, toggleList, drawerOpen: phone && drawerOpen, openDrawer }),
    [phone, hasList, listShown, toggleList, drawerOpen, openDrawer],
  );
  const showList = !phone && hasList && listShown;

  return (
    <LayoutContext.Provider value={layout}>
      <ListsContext.Provider value={lists}>
        <a
          href="#content"
          className="sr-only z-50 rounded-md bg-ak-surface px-3 py-2 focus:not-sr-only focus:fixed focus:top-2 focus:left-2"
        >
          Skip to content
        </a>
        {current.to === "/" ? (
          <a
            href={`#${composerId}`}
            className="sr-only z-50 rounded-md bg-ak-surface px-3 py-2 focus:not-sr-only focus:fixed focus:top-2 focus:left-2"
          >
            Skip to message box
          </a>
        ) : null}
        <div
          className={cn(
            "grid h-svh overflow-hidden bg-ak-bg",
            phone
              ? "grid-cols-[minmax(0,1fr)]"
              : showList
                ? "grid-cols-[var(--ak-rail-w)_var(--ak-list-w-laptop)_minmax(0,1fr)] xl:grid-cols-[var(--ak-rail-w)_var(--ak-list-w)_minmax(0,1fr)]"
                : "grid-cols-[var(--ak-rail-w)_minmax(0,1fr)]",
          )}
        >
          {phone ? null : <Rail current={current} />}
          {showList ? <ListColumn current={current} lists={lists} /> : null}
          <main
            id="content"
            tabIndex={-1}
            inert={layout.drawerOpen}
            className="flex min-h-0 min-w-0 flex-col bg-ak-surface outline-none"
          >
            <Suspense
              fallback={
                <div className={phone ? "p-4" : "p-6"}>
                  <Loading />
                </div>
              }
            >
              {children ?? <Outlet />}
            </Suspense>
          </main>
        </div>
        {phone ? (
          <div ref={drawer} className="contents">
            <Drawer open={drawerOpen} current={current} close={closeDrawer} />
          </div>
        ) : null}
        <Toasts />
      </ListsContext.Provider>
    </LayoutContext.Provider>
  );
}
