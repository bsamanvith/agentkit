import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { Component, Surface } from "./model";
import { A2UISurface, drawn } from "./surface";

function surface(components: object[], data: unknown = {}, extra: Partial<Surface> = {}): Surface {
  return {
    id: "s",
    key: "a:ui0",
    components: new Map((components as Component[]).map((c) => [c.id, c])),
    data,
    revision: 1,
    deleted: false,
    ...extra,
  };
}

const col = (...children: string[]) => ({ id: "root", component: "Column", children });
const hostile = `<img src=x onerror="window.__xss=1"><script>window.__xss=2</script>`;

describe("A2UISurface", () => {
  it("draws every component of the catalog", () => {
    const all = [
      col("t", "img", "icon", "row", "list", "card", "div", "btn", "tf", "cb", "cp", "dt"),
      { id: "t", component: "Text", text: "Title", variant: "h2" },
      { id: "img", component: "Image", url: `${location.origin}/a.png`, description: "A picture" },
      { id: "icon", component: "Icon", name: "search", accessibility: { label: "Search icon" } },
      { id: "row", component: "Row", children: ["t2"], justify: "spaceBetween", align: "center" },
      { id: "t2", component: "Text", text: "In a row", weight: 2 },
      { id: "list", component: "List", children: ["t3"] },
      { id: "t3", component: "Text", text: "Listed" },
      { id: "card", component: "Card", child: "t4" },
      { id: "t4", component: "Text", text: "Carded" },
      { id: "div", component: "Divider" },
      { id: "btn", component: "Button", child: "bl", action: { event: { name: "go" } } },
      { id: "bl", component: "Text", text: "Go" },
      { id: "tf", component: "TextField", label: "Name", variant: "longText" },
      { id: "cb", component: "CheckBox", label: "Agree", value: true },
      { id: "cp", component: "ChoicePicker", label: "Pick", options: [{ label: "A", value: "a" }], value: ["a"] },
      { id: "dt", component: "DateTimeInput", label: "When", enableDate: true, enableTime: true, value: "" },
    ];
    expect(new Set(all.map((c) => c.component))).toEqual(new Set(drawn));
    render(<A2UISurface surface={surface(all)} onAction={() => {}} />);
    expect(screen.getByRole("heading", { name: "Title" })).toBeInTheDocument();
    expect(screen.getByRole("img", { name: "A picture" })).toHaveAttribute("src", `${location.origin}/a.png`);
    expect(screen.getByLabelText("Search icon")).toBeInTheDocument();
    expect(screen.getByRole("listitem")).toHaveTextContent("Listed");
    expect(screen.getByText("Carded")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Go" })).toBeEnabled();
    expect(screen.getByRole("textbox", { name: "Name" }).tagName).toBe("TEXTAREA");
    expect(screen.getByRole("checkbox", { name: "Agree" })).toBeChecked();
    expect(screen.getByRole("radio", { name: "A" })).toBeChecked();
    expect(screen.getByLabelText("When")).toHaveAttribute("type", "datetime-local");
    expect(screen.queryByText(/Unsupported/)).toBeNull();
  });

  it("draws text as text: no markup, no Markdown", () => {
    const { container } = render(
      <A2UISurface
        surface={surface(
          [
            col("a", "b"),
            { id: "a", component: "Text", text: hostile },
            { id: "b", component: "Text", text: { path: "/evil" }, variant: "h1" },
          ],
          { evil: `# **${hostile}**` },
        )}
      />,
    );
    expect(container.querySelector("img, script, strong")).toBeNull();
    expect(container.textContent).toContain(hostile);
    // A heading drops its Markdown marker only.
    expect(screen.getByRole("heading")).toHaveTextContent(`**${hostile}**`);
  });

  it("drops unsafe and outside URLs, bound or computed, and keeps safe links", () => {
    const { container } = render(
      <A2UISurface
        surface={surface(
          [
            col("i1", "i2", "i3", "l1", "l2", "l3"),
            { id: "i1", component: "Image", url: { path: "/js" }, description: "one" },
            { id: "i2", component: "Image", url: "https://tracker.example/p.png", description: "two" },
            {
              id: "i3",
              component: "Image",
              url: { call: "formatString", args: { value: "data:image/png,${/x}" } },
            },
            {
              id: "l1",
              component: "Button",
              child: "t1",
              action: { functionCall: { call: "openUrl", args: { url: { path: "/js" } } } },
            },
            { id: "t1", component: "Text", text: "Bad" },
            {
              id: "l2",
              component: "Button",
              child: "t2",
              action: { functionCall: { call: "openUrl", args: { url: "javascript:alert(1)" } } },
            },
            { id: "t2", component: "Text", text: "Literal bad" },
            {
              id: "l3",
              component: "Button",
              child: "t3",
              action: { functionCall: { call: "openUrl", args: { url: { path: "/ok" } } } },
            },
            { id: "t3", component: "Text", text: "Good" },
          ],
          { js: "javascript:window.__xss=3", ok: "https://example.com/docs", x: "AAAA" },
        )}
      />,
    );
    expect(container.querySelector("img")).toBeNull();
    expect(screen.getAllByText(/^Image not shown/)).toHaveLength(3);
    expect(screen.getByRole("button", { name: "Bad" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Literal bad" })).toBeDisabled();
    expect([...container.querySelectorAll("a")].map((a) => a.getAttribute("href"))).toEqual([
      "https://example.com/docs",
    ]);
    expect(screen.getByRole("link", { name: "Good" })).toHaveAttribute("rel", "noopener noreferrer");
  });

  it("asks allowImage about each safe image URL", () => {
    const allow = vi.fn((u: URL) => u.host === "img.example");
    render(
      <A2UISurface
        allowImage={allow}
        surface={surface([
          col("a", "b"),
          { id: "a", component: "Image", url: "https://img.example/a.png", description: "ok" },
          { id: "b", component: "Image", url: "https://other.example/b.png", description: "no" },
        ])}
      />,
    );
    expect(screen.getByRole("img", { name: "ok" })).toBeInTheDocument();
    expect(screen.queryByRole("img", { name: "no" })).toBeNull();
    expect(allow).toHaveBeenCalledTimes(2);
  });

  it("shows a component it doesn't draw as a note, never its data", () => {
    const { container } = render(
      <A2UISurface
        surface={surface([col("x"), { id: "x", component: "Slider", label: "secret label", value: 3, max: 9 }])}
      />,
    );
    expect(screen.getByText("Unsupported component:")).toHaveTextContent("Unsupported component: Slider");
    expect(container.textContent).not.toContain("secret");
  });

  it("draws nothing without a root or once deleted, and survives cycles and missing children", () => {
    const { container, rerender } = render(
      <A2UISurface surface={surface([{ id: "x", component: "Text", text: "x" }])} />,
    );
    expect(container.textContent).toBe("");
    rerender(
      <A2UISurface surface={surface([col("t"), { id: "t", component: "Text", text: "t" }], {}, { deleted: true })} />,
    );
    expect(container.textContent).toBe("");
    rerender(
      <A2UISurface
        surface={surface([
          col("loop", "missing"),
          { id: "loop", component: "Card", child: "inner" },
          { id: "inner", component: "Column", children: ["loop", "leaf"] },
          { id: "leaf", component: "Text", text: "leaf" },
        ])}
      />,
    );
    expect(screen.getAllByText("leaf")).toHaveLength(1);
  });

  it("draws a template once per item, binding relative paths to the item", () => {
    render(
      <A2UISurface
        surface={surface(
          [
            { id: "root", component: "List", children: { componentId: "item", path: "/people" } },
            { id: "item", component: "Text", text: { call: "formatString", args: { value: "${name} (${/team})" } } },
          ],
          { team: "core", people: [{ name: "Ann" }, { name: "Bea" }] },
        )}
      />,
    );
    expect(screen.getAllByRole("listitem").map((li) => li.textContent)).toEqual(["Ann (core)", "Bea (core)"]);
  });

  it("sends an action with its context resolved from the data and the person's edits", async () => {
    const onAction = vi.fn();
    const onEdit = vi.fn();
    const s = surface(
      [
        col("q", "kind", "go"),
        { id: "q", component: "TextField", label: "Query", value: { path: "/q" } },
        {
          id: "kind",
          component: "ChoicePicker",
          label: "Kind",
          variant: "multipleSelection",
          displayStyle: "chips",
          options: [
            { label: "Web", value: "web" },
            { label: "News", value: "news" },
          ],
          value: { path: "/kinds" },
        },
        {
          id: "go",
          component: "Button",
          variant: "primary",
          child: "go_t",
          action: { event: { name: "search", context: { q: { path: "/q" }, kinds: { path: "/kinds" }, fixed: 1 } } },
        },
        { id: "go_t", component: "Text", text: "Search" },
      ],
      { q: "", kinds: ["web"] },
    );
    const { rerender } = render(
      <A2UISurface surface={s} edits={new Map([["/q", "go 1.27"]])} onEdit={onEdit} onAction={onAction} />,
    );
    expect(screen.getByRole("textbox", { name: "Query" })).toHaveValue("go 1.27");
    await userEvent.type(screen.getByRole("textbox", { name: "Query" }), "!");
    expect(onEdit).toHaveBeenLastCalledWith("/q", "go 1.27!");
    await userEvent.click(screen.getByRole("button", { name: "News" }));
    expect(onEdit).toHaveBeenLastCalledWith("/kinds", ["web", "news"]);
    rerender(
      <A2UISurface
        surface={s}
        edits={
          new Map<string, unknown>([
            ["/q", "go 1.27"],
            ["/kinds", ["web", "news"]],
          ])
        }
        onEdit={onEdit}
        onAction={onAction}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Search" }));
    expect(onAction).toHaveBeenCalledWith({
      surfaceId: "s",
      componentId: "go",
      name: "search",
      context: { q: "go 1.27", kinds: ["web", "news"], fixed: 1 },
    });
  });

  it("keeps edits inside when nobody holds them", async () => {
    render(
      <A2UISurface
        surface={surface([col("c"), { id: "c", component: "CheckBox", label: "Yes", value: { path: "/yes" } }], {
          yes: false,
        })}
      />,
    );
    await userEvent.click(screen.getByRole("checkbox", { name: "Yes" }));
    expect(screen.getByRole("checkbox", { name: "Yes" })).toBeChecked();
  });

  it("is disabled while busy, and sent keeps the values with a note", () => {
    const s = surface(
      [
        col("q", "go"),
        { id: "q", component: "TextField", label: "Query", value: { path: "/q" } },
        { id: "go", component: "Button", child: "t", action: { event: { name: "go" } } },
        { id: "t", component: "Text", text: "Go" },
      ],
      { q: "kept" },
    );
    const { rerender } = render(<A2UISurface surface={s} state="busy" onAction={() => {}} />);
    expect(screen.getByRole("textbox", { name: "Query" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Go" })).toBeDisabled();
    expect(screen.queryByText("Sent")).toBeNull();
    rerender(<A2UISurface surface={s} state="sent" onAction={() => {}} />);
    expect(screen.getByRole("textbox", { name: "Query" })).toHaveValue("kept");
    expect(screen.getByRole("textbox", { name: "Query" })).toBeDisabled();
    expect(screen.getByText("Sent")).toBeInTheDocument();
    // Without anywhere to send it, a button does nothing.
    rerender(<A2UISurface surface={s} />);
    expect(screen.getByRole("button", { name: "Go" })).toBeDisabled();
  });

  it("disables a button while its checks fail, saying why", () => {
    render(
      <A2UISurface
        onAction={() => {}}
        surface={surface(
          [
            col("go"),
            {
              id: "go",
              component: "Button",
              child: "t",
              action: { event: { name: "go" } },
              checks: [
                { condition: { call: "required", args: { value: { path: "/email" } } }, message: "Add an email." },
              ],
            },
            { id: "t", component: "Text", text: "Go" },
          ],
          { email: "" },
        )}
      />,
    );
    expect(screen.getByRole("button", { name: "Go" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Go" })).toHaveAccessibleDescription("Add an email.");
  });

  it("checks a text field's validationRegexp", async () => {
    render(
      <A2UISurface
        surface={surface([col("p"), { id: "p", component: "TextField", label: "Phone", validationRegexp: "^\\d{3}$" }])}
      />,
    );
    const box = screen.getByRole("textbox", { name: "Phone" });
    await userEvent.type(box, "12a");
    expect(box).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByText("This doesn't match the expected format.")).toBeInTheDocument();
  });

  it("uses a select for one of many options", () => {
    const options = ["a", "b", "c", "d", "e", "f", "g"].map((v) => ({ label: v.toUpperCase(), value: v }));
    render(
      <A2UISurface
        surface={surface([col("p"), { id: "p", component: "ChoicePicker", label: "Letter", options, value: ["c"] }])}
      />,
    );
    expect(screen.getByRole("combobox", { name: "Letter" })).toHaveValue("c");
  });
});
