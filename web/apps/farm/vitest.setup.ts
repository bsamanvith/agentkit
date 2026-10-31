import "@testing-library/jest-dom/vitest";
import { cleanup, configure } from "@testing-library/react";
import { afterEach } from "vitest";

afterEach(() => cleanup());

// findBy* and waitFor give up after 1 s by default. The first lookup in a
// test waits for the whole render (lazy routes, fetches, effects), which
// takes longer than that when the machine is busy (make check runs the Go
// tests beside these). One shared, longer wait instead of a timeout per call.
configure({ asyncUtilTimeout: 5000 });

// jsdom has no matchMedia; the shell asks it whether this is a phone.
// Answer as a desktop with the light OS theme.
if (!window.matchMedia) {
  window.matchMedia = (query: string) =>
    ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    }) as MediaQueryList;
}
