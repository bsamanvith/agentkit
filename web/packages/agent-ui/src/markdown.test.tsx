import { render } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { Markdown, safeHref } from "./markdown";

declare global {
  interface Window {
    __xss?: number;
  }
}

/** The elements a rendered answer may contain (docs/UI.md "Rules"). */
const allowed = new Set([
  "DIV",
  "P",
  "H3",
  "H4",
  "UL",
  "OL",
  "LI",
  "BLOCKQUOTE",
  "PRE",
  "CODE",
  "TABLE",
  "THEAD",
  "TBODY",
  "TR",
  "TH",
  "TD",
  "HR",
  "STRONG",
  "EM",
  "DEL",
  "A",
  "SPAN",
]);

/** Renders and checks what holds for every answer: allowed elements, no handlers, only http(s) links. */
function md(text: string): HTMLElement {
  const { container } = render(<Markdown text={text} />);
  for (const el of container.querySelectorAll("*")) {
    expect(allowed, `<${el.tagName.toLowerCase()}>`).toContain(el.tagName);
    for (const a of el.attributes) {
      expect(a.name.startsWith("on"), `${el.tagName} ${a.name}`).toBe(false);
      expect(a.name).not.toBe("src");
    }
    if (el.tagName === "A") {
      expect(el.getAttribute("href")).toMatch(/^https?:\/\//);
      expect(el.getAttribute("rel")).toBe("noopener noreferrer");
      expect(el.getAttribute("target")).toBe("_blank");
    }
  }
  return container;
}

afterEach(() => {
  expect(window.__xss).toBeUndefined();
});

describe("Markdown", () => {
  it("renders the allowed elements", () => {
    const c = md(
      [
        "# Title",
        "",
        "Some **bold**, *em*, _em too_, ~~gone~~ and `code`.",
        "",
        "- one",
        "- two with [a link](https://go.dev/doc)",
        "",
        "1. first",
        "2. second",
        "",
        "> quoted",
        "",
        "```go",
        'fmt.Println("<b>hi</b>")',
        "```",
        "",
        "| a | b |",
        "|---|---|",
        "| 1 | 2 |",
        "",
        "---",
        "See https://example.com/x.",
      ].join("\n"),
    );
    expect(c.querySelector("h3")?.textContent).toBe("Title");
    expect(c.querySelector("strong")?.textContent).toBe("bold");
    expect([...c.querySelectorAll("em")].map((e) => e.textContent)).toEqual(["em", "em too"]);
    expect(c.querySelector("del")?.textContent).toBe("gone");
    expect(c.querySelectorAll("ul > li")).toHaveLength(2);
    expect(c.querySelectorAll("ol > li")).toHaveLength(2);
    expect(c.querySelector("blockquote")?.textContent).toBe("quoted");
    expect(c.querySelector("pre code")?.textContent).toBe('fmt.Println("<b>hi</b>")');
    expect(c.querySelector("pre")?.getAttribute("tabindex")).toBe("0");
    expect([...c.querySelectorAll("td")].map((t) => t.textContent)).toEqual(["1", "2"]);
    expect(c.querySelector("hr")).not.toBeNull();
    expect([...c.querySelectorAll("a")].map((a) => a.getAttribute("href"))).toEqual([
      "https://go.dev/doc",
      "https://example.com/x",
    ]);
    // The full stop after a bare URL isn't part of it.
    expect(c.textContent).toContain("https://example.com/x.");
  });

  it("keeps raw HTML as text", () => {
    const hostile = `<script>window.__xss=1</script><img src=x onerror="window.__xss=1"> <a href="javascript:window.__xss=1">x</a>`;
    const c = md(`${hostile}\n\n- ${hostile}\n\n> ${hostile}`);
    expect(c.querySelector("script, img, iframe")).toBeNull();
    expect(c.textContent).toContain("<script>window.__xss=1</script>");
    expect(c.textContent).toContain('<img src=x onerror="window.__xss=1">');
  });

  it("drops links that aren't http(s), keeping their text", () => {
    const c = md(
      "[click](javascript:window.__xss=1) [data](data:text/html,<script>1</script>) [rel](/v1/agents) " +
        "[spaced]( javascript:alert(1)) [JS](JaVaScRiPt:alert(1)) ![img](https://evil.example/x.png)",
    );
    expect(c.querySelectorAll("a")).toHaveLength(0);
    expect(c.textContent).toContain("click");
    expect(c.textContent).toContain("img");
  });

  it("handles unclosed and nested markers without losing text", () => {
    const text = "a **b *c* d** e `f ** g` [h](https://x.example/(y)) **unclosed";
    const c = md(text);
    expect(c.querySelector("strong em")?.textContent).toBe("c");
    expect(c.querySelector("code")?.textContent).toBe("f ** g");
    expect(c.querySelector("a")?.getAttribute("href")).toBe("https://x.example/(y)");
    expect(c.textContent).toContain("**unclosed");
  });

  it("renders an unclosed code fence to the end (a streaming answer)", () => {
    const c = md("Here:\n```\nline 1\nline 2");
    expect(c.querySelector("pre code")?.textContent).toBe("line 1\nline 2");
  });

  it("renders any input to allowed elements only", () => {
    // A cheap fuzz: random mixes of the syntax's characters.
    const alphabet = ["*", "_", "`", "[", "]", "(", ")", "#", ">", "-", "|", "\n", " ", "a", "1.", "~", "<", "\\", "h"];
    let seed = 7;
    const rand = () => (seed = (seed * 1103515245 + 12345) % 2147483648) / 2147483648;
    for (let n = 0; n < 300; n++) {
      let s = "";
      for (let j = 0; j < 60; j++) s += alphabet[Math.floor(rand() * alphabet.length)];
      // md checks the elements, attributes and links of each.
      md(s);
    }
  });
});

describe("safeHref", () => {
  it("allows only absolute http(s) URLs", () => {
    expect(safeHref("https://go.dev")).toBe("https://go.dev/");
    expect(safeHref("http://localhost:8080/x")).toBe("http://localhost:8080/x");
    for (const bad of [
      "javascript:alert(1)",
      " javascript:alert(1)",
      "data:text/html,x",
      "/relative",
      "mailto:a@b.c",
      "vbscript:x",
    ]) {
      expect(safeHref(bad), bad).toBeUndefined();
    }
  });
});
