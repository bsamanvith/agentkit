import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ComponentType } from "react";
import { describe, expect, it, vi } from "vitest";
import { Composer, Message, SourceChip, StepGroup, ToolStep, stepText, usageText } from "./index";

type StoryModule = { default: { title: string; component: ComponentType<object> } } & Record<string, unknown>;

// Every story renders: a story that throws, or a component that stops
// accepting its story's args, fails here.
const modules = import.meta.glob<StoryModule>("./*.stories.tsx", { eager: true });

describe("stories", () => {
  it("has stories for every component", () => {
    expect(Object.values(modules).map((m) => m.default.title)).toEqual(
      expect.arrayContaining([
        "agent-ui/ToolStep",
        "agent-ui/StepGroup",
        "agent-ui/SourceChip",
        "agent-ui/Message",
        "agent-ui/Composer",
        "agent-ui/A2UISurface",
      ]),
    );
  });
  for (const [file, mod] of Object.entries(modules)) {
    for (const [name, story] of Object.entries(mod)) {
      if (name === "default") continue;
      it(`${file} ${name}`, () => {
        const { component: C } = mod.default;
        const { container } = render(<C {...(story as { args: object }).args} />);
        expect(container.firstChild).not.toBeNull();
      });
    }
  }
});

describe("untrusted text stays text", () => {
  const hostile = `<img src=x onerror="window.__xss=1"><script>window.__xss=1</script>`;

  it("in a message", () => {
    const { container } = render(<Message role="assistant" text={hostile} />);
    expect(container.querySelector("img, script")).toBeNull();
    expect(container.textContent).toContain("<script>");
  });

  it("in a tool step", () => {
    const { container } = render(<ToolStep state="done" label="Searched the web" detail={hostile} />);
    expect(container.querySelector("img, script")).toBeNull();
  });

  it("source chips take only http(s) links", () => {
    const { container } = render(
      <>
        <SourceChip url="javascript:alert(1)" />
        <SourceChip url="data:text/html,hi" />
        <SourceChip url="not a url" />
      </>,
    );
    expect(container.querySelector("a")).toBeNull();
    render(<SourceChip url="https://go.dev/doc" />);
    expect(screen.getByRole("link")).toHaveAttribute("href", "https://go.dev/doc");
  });
});

describe("Composer", () => {
  it("sends on Enter, adds a line on Shift+Enter", async () => {
    const onSend = vi.fn();
    render(<Composer onSend={onSend} />);
    const box = screen.getByRole("textbox", { name: "Message" });
    await userEvent.type(box, "hello{Shift>}{Enter}{/Shift}there{Enter}");
    expect(onSend).toHaveBeenCalledExactlyOnceWith("hello\nthere");
    expect(box).toHaveValue("");
  });

  it("shows Stop while running, and Enter doesn't send", async () => {
    const onSend = vi.fn();
    const onStop = vi.fn();
    render(<Composer onSend={onSend} running onStop={onStop} />);
    await userEvent.type(screen.getByRole("textbox", { name: "Message" }), "next{Enter}");
    expect(onSend).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Stop" }));
    expect(onStop).toHaveBeenCalledOnce();
  });
});

describe("StepGroup", () => {
  const step = (id: string) => ({ id, state: "done" as const, label: `Step ${id}`, durationMs: 1000 });

  it("is a plain step for one call", () => {
    render(<StepGroup steps={[step("1")]} />);
    expect(screen.queryByRole("button")).toBeNull();
    expect(screen.getByText("Step 1")).toBeVisible();
  });

  it("is open up to 3 calls and collapsed above", () => {
    const { unmount } = render(<StepGroup steps={[step("1"), step("2"), step("3")]} />);
    expect(screen.getByRole("button", { name: /Used 3 tools · 3.0 s/ })).toHaveAttribute("aria-expanded", "true");
    unmount();
    render(<StepGroup steps={[step("1"), step("2"), step("3"), step("4")]} />);
    expect(screen.getByRole("button", { name: /Used 4 tools/ })).toHaveAttribute("aria-expanded", "false");
  });
});

describe("StepGroup windowing", () => {
  const steps = (n: number, running = false, failedAt = -1) =>
    Array.from({ length: n }, (_, i) => ({
      id: String(i + 1),
      state: running && i === n - 1 ? ("running" as const) : i === failedAt ? ("failed" as const) : ("done" as const),
      label: `Step ${i + 1}`,
    }));
  const open = async (name: RegExp) => userEvent.click(screen.getByRole("button", { name }));

  it("shows the first 5 of a finished group, the failed ones, and the rest on demand", async () => {
    render(<StepGroup steps={steps(9, false, 7)} />);
    await open(/Used 9 tools · 1 failed/);
    expect(screen.getByText("Step 5")).toBeVisible();
    expect(screen.queryByText("Step 6")).toBeNull();
    expect(screen.getByText("Step 8 failed")).toBeVisible();
    await userEvent.click(screen.getByRole("button", { name: "Show 3 more" }));
    expect(screen.getByText("Step 9")).toBeVisible();
  });

  it("shows the latest 5 of a running group", async () => {
    render(<StepGroup steps={steps(8, true)} />);
    await open(/Used 8 tools/);
    expect(screen.queryByText("Step 3")).toBeNull();
    expect(screen.getByText("Step 4")).toBeVisible();
    expect(screen.getByRole("button", { name: "Show 3 earlier" })).toBeVisible();
  });

  it("keeps the user's choice as calls arrive", async () => {
    const { rerender } = render(<StepGroup steps={steps(2, true)} />);
    await open(/Used 2 tools/);
    rerender(<StepGroup steps={steps(4, true)} />);
    // Above 3 calls a group starts collapsed, but the user closed this one already.
    expect(screen.getByRole("button", { name: /Used 4 tools/ })).toHaveAttribute("aria-expanded", "false");
    await open(/Used 4 tools/);
    rerender(<StepGroup steps={steps(5, true)} />);
    expect(screen.getByRole("button", { name: /Used 5 tools/ })).toHaveAttribute("aria-expanded", "true");
  });
});

describe("ToolStep details", () => {
  it("shows the request and a text result as text, and the raw JSON on demand", async () => {
    const hostile = `<img src=x onerror="window.__xss=1">`;
    const { container } = render(
      <ToolStep state="done" label="Echoed" input={{ text: hostile }} result={JSON.stringify({ text: hostile })} />,
    );
    await userEvent.click(screen.getByRole("button", { name: /Echoed/ }));
    expect(screen.getByText("text")).toBeVisible();
    expect(screen.getByLabelText("Result")).toHaveTextContent("<img src=x onerror=");
    await userEvent.click(screen.getByRole("button", { name: "Show raw JSON" }));
    expect(screen.getByLabelText("Raw JSON")).toHaveTextContent('"input"');
    expect(container.querySelector("img, script")).toBeNull();
  });

  it("lists search-like results, 20 at first", async () => {
    const results = Array.from({ length: 25 }, (_, i) => ({ title: `Result ${i}`, url: `https://r${i}.example/x` }));
    render(<ToolStep state="done" label="Searched" result={JSON.stringify({ results })} />);
    await userEvent.click(screen.getByRole("button", { name: /Searched/ }));
    expect(screen.getByText("Result 19")).toBeVisible();
    expect(screen.queryByText("Result 20")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Show all 25" }));
    expect(screen.getByText("Result 24")).toBeVisible();
  });

  it("cuts a long result", async () => {
    render(<ToolStep state="done" label="Read" result={"x".repeat(5000)} />);
    await userEvent.click(screen.getByRole("button", { name: /Read/ }));
    expect(screen.getByLabelText("Result").textContent).toHaveLength(4001);
    await userEvent.click(screen.getByRole("button", { name: /Show all \(4.9 KB\)/ }));
    expect(screen.getByLabelText("Result").textContent).toHaveLength(5000);
  });
});

describe("stepText", () => {
  it("uses the display verbs, else the tool's name", () => {
    const d = { title: "Echo", icon: "clock", present: "Echoing", past: "Echoed" };
    expect(stepText("echo", { text: "hi" }, true, d)).toEqual({ kind: "clock", label: "Echoing", detail: "“hi”" });
    expect(stepText("echo", { n: 3 }, false, d)).toEqual({ kind: "clock", label: "Echoed", detail: "3" });
    expect(stepText("echo", {}, false)).toEqual({ kind: "tool", label: "Ran echo" });
    expect(stepText("x", { q: "a".repeat(100) }, true, { icon: "toString" }).kind).toBe("tool");
    expect(stepText("x", { q: "a".repeat(100) }, true).detail).toHaveLength(83);
  });
});

describe("Composer, controlled", () => {
  it("keeps the caller's draft and shows why Send is off", async () => {
    const onValueChange = vi.fn();
    const onSend = vi.fn();
    render(
      <Composer id="c" onSend={onSend} value="draft" onValueChange={onValueChange} sendDisabledReason="Offline" />,
    );
    const box = screen.getByRole("textbox", { name: "Message" });
    expect(box).toHaveValue("draft");
    expect(box).toHaveAccessibleDescription("Offline");
    await userEvent.type(box, "!{Enter}");
    expect(onValueChange).toHaveBeenCalledWith("draft!");
    expect(onSend).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();
  });
});

describe("usageText", () => {
  it("says what is known", () => {
    expect(
      usageText({
        turns: 2,
        usage: { input_tokens: 100, output_tokens: 34, cache_read_input_tokens: 1000 },
        durationMs: 3200,
      }),
    ).toBe("2 turns · 1,134 tokens (1,000 cached) · 3.2 s");
    expect(usageText({ turns: 1 })).toBe("1 turn");
    expect(usageText({})).toBe("");
  });
});
