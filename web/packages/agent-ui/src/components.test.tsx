import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ComponentType } from "react";
import { describe, expect, it, vi } from "vitest";
import { Composer, Message, SourceChip, StepGroup, ToolStep } from "./index";

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
