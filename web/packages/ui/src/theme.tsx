import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";

/** What the user picked in the theme menu. "system" follows the OS. */
export type ThemePreference = "system" | "light" | "dark";

/** The localStorage key of the preference. It survives sign-out (docs/UI.md "Sign in"). */
export const themeStorageKey = "agentkit.theme";

export const themePreferences: readonly ThemePreference[] = ["system", "light", "dark"];

function isPreference(v: unknown): v is ThemePreference {
  return typeof v === "string" && (themePreferences as readonly string[]).includes(v);
}

/** Reads the saved preference; "system" when none is saved or storage is blocked. */
export function readThemePreference(storage?: Pick<Storage, "getItem">): ThemePreference {
  try {
    // Inside the try: with site data blocked, reading localStorage itself throws.
    const v = (storage ?? globalThis.localStorage)?.getItem(themeStorageKey);
    return isPreference(v) ? v : "system";
  } catch {
    return "system";
  }
}

/**
 * Sets the preference on <html>: data-theme="light" or "dark" pins it, no
 * attribute follows prefers-color-scheme (tokens.css). The console's
 * public/theme.js does the same before first paint, so a pinned theme
 * doesn't flash.
 */
export function applyThemePreference(pref: ThemePreference, root: HTMLElement = document.documentElement) {
  if (pref === "system") root.removeAttribute("data-theme");
  else root.setAttribute("data-theme", pref);
}

type ThemeContextValue = {
  preference: ThemePreference;
  setPreference: (pref: ThemePreference) => void;
};

const ThemeContext = createContext<ThemeContextValue | null>(null);

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [preference, setState] = useState<ThemePreference>(() => readThemePreference());

  useEffect(() => applyThemePreference(preference), [preference]);

  const setPreference = useCallback((pref: ThemePreference) => {
    setState(pref);
    try {
      if (pref === "system") localStorage.removeItem(themeStorageKey);
      else localStorage.setItem(themeStorageKey, pref);
    } catch {
      // Storage blocked: the choice lasts for this page only.
    }
  }, []);

  const value = useMemo(() => ({ preference, setPreference }), [preference, setPreference]);
  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}

export function useTheme(): ThemeContextValue {
  const ctx = useContext(ThemeContext);
  if (!ctx) throw new Error("useTheme needs a <ThemeProvider> above it");
  return ctx;
}
