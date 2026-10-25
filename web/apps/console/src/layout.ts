// The shell's layout state (docs/UI.md "Layouts"), for the head bar's toggle.

import { createContext, useContext } from "react";

export type Layout = {
  /** < 768 px: no rail; the list is in the drawer. */
  phone: boolean;
  /** Whether this section has a list column (Connectors has none). */
  hasList: boolean;
  /** Laptop and desktop: whether the list column is shown. */
  listShown: boolean;
  toggleList: () => void;
  /** Phone: the drawer. */
  drawerOpen: boolean;
  openDrawer: () => void;
};

export const LayoutContext = createContext<Layout | null>(null);

export function useLayout(): Layout {
  const ctx = useContext(LayoutContext);
  if (!ctx) throw new Error("useLayout needs the <Shell> above it");
  return ctx;
}

/** The platform's modifier: ⌘ on Apple devices, Ctrl elsewhere. */
export const isApple =
  typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);
export const modHint = isApple ? "⌘" : "Ctrl+";
export const modKey = isApple ? "Meta" : "Control";

/** localStorage: whether the list column is shown ("shown" or "hidden"). */
export const listStorageKey = "agentkit.list";
