import { act, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ThemeProvider, readThemePreference, themeStorageKey, useTheme } from "./theme";

function Picker() {
  const { preference, setPreference } = useTheme();
  return (
    <>
      <span data-testid="pref">{preference}</span>
      <button onClick={() => setPreference("dark")}>dark</button>
      <button onClick={() => setPreference("system")}>system</button>
    </>
  );
}

afterEach(() => {
  vi.restoreAllMocks();
  localStorage.clear();
  document.documentElement.removeAttribute("data-theme");
});

describe("theme", () => {
  it("defaults to system: no data-theme, so the OS setting applies", () => {
    render(
      <ThemeProvider>
        <Picker />
      </ThemeProvider>,
    );
    expect(screen.getByTestId("pref")).toHaveTextContent("system");
    expect(document.documentElement).not.toHaveAttribute("data-theme");
  });

  it("pins a choice on <html> and keeps it", () => {
    render(
      <ThemeProvider>
        <Picker />
      </ThemeProvider>,
    );
    act(() => screen.getByText("dark").click());
    expect(document.documentElement).toHaveAttribute("data-theme", "dark");
    expect(localStorage.getItem(themeStorageKey)).toBe("dark");
    act(() => screen.getByText("system").click());
    expect(document.documentElement).not.toHaveAttribute("data-theme");
    expect(localStorage.getItem(themeStorageKey)).toBeNull();
  });

  it("reads a saved choice, and ignores junk", () => {
    localStorage.setItem(themeStorageKey, "light");
    expect(readThemePreference()).toBe("light");
    localStorage.setItem(themeStorageKey, "purple");
    expect(readThemePreference()).toBe("system");
  });

  it("survives blocked storage", () => {
    const blocked = {
      getItem: () => {
        throw new Error("SecurityError");
      },
    };
    expect(readThemePreference(blocked)).toBe("system");
  });

  it("survives site data being blocked, where reading localStorage throws", () => {
    vi.spyOn(window, "localStorage", "get").mockImplementation(() => {
      throw new DOMException("The operation is insecure.", "SecurityError");
    });
    expect(readThemePreference()).toBe("system");
    render(
      <ThemeProvider>
        <Picker />
      </ThemeProvider>,
    );
    expect(screen.getByTestId("pref")).toHaveTextContent("system");
    act(() => screen.getByText("dark").click());
    expect(document.documentElement).toHaveAttribute("data-theme", "dark");
  });
});
