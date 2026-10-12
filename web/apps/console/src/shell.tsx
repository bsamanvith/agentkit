// The app shell (docs/UI.md "Information architecture", "Layouts"): a 56 px
// icon rail of sections, the section's list column, and the content. It is
// shadcn's Sidebar in its nested form (the "sidebar-09" block): one
// collapsible Sidebar holding the rail and the list. On phones (< 768 px)
// shadcn turns it into a Sheet, the drawer.

import { useTheme, themePreferences, type ThemePreference } from "@agentkit/ui";
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
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarHeader,
  SidebarInset,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarProvider,
  SidebarSeparator,
  SidebarTrigger,
} from "@agentkit/ui/components/sidebar";
import { Plus, UserRound } from "lucide-react";
import type { CSSProperties, ReactNode } from "react";
import { NavLink, Outlet, useLocation } from "react-router";
import { useApi } from "./api";
import { adminSections, sectionOf, sections, type Section } from "./sections";

const themeLabels: Record<ThemePreference, string> = { system: "System", light: "Light", dark: "Dark" };

function RailItem({ section }: { section: Section }) {
  const { pathname } = useLocation();
  const current = sectionOf(pathname).to === section.to;
  const Icon = section.icon;
  return (
    <SidebarMenuItem>
      <SidebarMenuButton
        asChild
        isActive={current}
        tooltip={{ children: section.label, hidden: false }}
        className="size-10 justify-center p-0"
      >
        <NavLink to={section.to} aria-current={current ? "page" : undefined}>
          <Icon aria-hidden="true" />
          <span className="sr-only">{section.label}</span>
        </NavLink>
      </SidebarMenuButton>
    </SidebarMenuItem>
  );
}

function UserMenu() {
  const { preference, setPreference } = useTheme();
  const { hasToken, forgetToken } = useApi();
  // Not modal: a modal menu locks scrolling with an injected <style>, which
  // the CSP blocks (no inline styles).
  return (
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger asChild>
        <SidebarMenuButton aria-label="Account and theme" className="size-10 justify-center p-0">
          <UserRound aria-hidden="true" />
        </SidebarMenuButton>
      </DropdownMenuTrigger>
      <DropdownMenuContent side="right" align="end" className="w-56">
        <DropdownMenuLabel className="text-ak-muted">Theme</DropdownMenuLabel>
        <DropdownMenuRadioGroup value={preference} onValueChange={(v) => setPreference(v as ThemePreference)}>
          {themePreferences.map((p) => (
            <DropdownMenuRadioItem key={p} value={p}>
              {themeLabels[p]}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
        {hasToken ? (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem onSelect={forgetToken}>Forget token</DropdownMenuItem>
          </>
        ) : null}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/** The list column's content per section. T055 fills Chats; T064 the admin lists. */
function ListColumn() {
  const { pathname } = useLocation();
  const section = sectionOf(pathname);
  return (
    <Sidebar collapsible="none" className="hidden flex-1 md:flex">
      <nav aria-label={section.label} className="contents">
        <SidebarHeader className="gap-2 border-b p-3">
          <div className="text-sm font-semibold text-ak-text">{section.label}</div>
          {section.to === "/" ? (
            <SidebarMenuButton asChild className="border border-ak-line-strong bg-ak-surface">
              <NavLink to="/new">
                <Plus aria-hidden="true" />
                New chat
              </NavLink>
            </SidebarMenuButton>
          ) : null}
        </SidebarHeader>
        <SidebarContent>
          <SidebarGroup>
            <SidebarGroupContent className="px-2 py-1 text-sm text-ak-muted">
              {section.to === "/" ? "No conversations yet" : "Nothing here yet"}
            </SidebarGroupContent>
          </SidebarGroup>
        </SidebarContent>
      </nav>
    </Sidebar>
  );
}

export function Shell({ children }: { children?: ReactNode }) {
  return (
    <SidebarProvider
      style={
        {
          "--sidebar-width": "calc(var(--ak-rail-w) + var(--ak-list-w))",
          "--sidebar-width-icon": "var(--ak-rail-w)",
        } as CSSProperties
      }
    >
      <a
        href="#content"
        className="sr-only z-50 rounded-md bg-ak-surface px-3 py-2 focus:not-sr-only focus:fixed focus:top-2 focus:left-2"
      >
        Skip to content
      </a>
      <Sidebar collapsible="icon" className="overflow-hidden *:data-[sidebar=sidebar]:flex-row">
        <Sidebar collapsible="none" className="w-[calc(var(--sidebar-width-icon)+1px)]! border-r">
          <nav aria-label="Sections" className="flex h-full flex-col">
            <SidebarContent className="items-center pt-2">
              <SidebarMenu className="items-center gap-1">
                {sections.map((s) => (
                  <RailItem key={s.to} section={s} />
                ))}
              </SidebarMenu>
              <SidebarSeparator className="mx-0 w-8" />
              <SidebarMenu className="items-center gap-1">
                {adminSections.map((s) => (
                  <RailItem key={s.to} section={s} />
                ))}
              </SidebarMenu>
            </SidebarContent>
            <SidebarFooter className="items-center pb-3">
              <SidebarMenu className="items-center">
                <SidebarMenuItem>
                  <UserMenu />
                </SidebarMenuItem>
              </SidebarMenu>
            </SidebarFooter>
          </nav>
        </Sidebar>
        <ListColumn />
      </Sidebar>
      <SidebarInset>
        <header className="flex h-12 items-center gap-2 border-b px-3">
          <SidebarTrigger aria-label="Show or hide the list" />
        </header>
        <main id="content" tabIndex={-1} className="flex min-h-0 flex-1 flex-col outline-none">
          {children ?? <Outlet />}
        </main>
      </SidebarInset>
    </SidebarProvider>
  );
}
