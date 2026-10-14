import type { Meta, Story } from "./stories";
import { ToolStep, type ToolStepProps } from "./tool-step";

export default { title: "agent-ui/ToolStep", component: ToolStep } satisfies Meta<ToolStepProps>;

export const Running: Story<ToolStepProps> = {
  args: { kind: "search", state: "running", label: "Searching the web", detail: "“go 1.27 release notes”" },
};
export const Done: Story<ToolStepProps> = {
  args: {
    kind: "search",
    state: "done",
    label: "Searched the web",
    detail: "“go 1.27 release notes”",
    durationMs: 1400,
  },
};
export const Failed: Story<ToolStepProps> = {
  args: { kind: "page", state: "failed", label: "Read 2 pages", detail: "go.dev, tip.golang.org", durationMs: 3100 },
};
export const Stopped: Story<ToolStepProps> = {
  args: { kind: "search", state: "stopped", label: "Searching the web" },
};
export const WithDetails: Story<ToolStepProps> = {
  args: {
    kind: "search",
    state: "done",
    label: "Searched the web",
    detail: "“go 1.27”",
    durationMs: 1400,
    input: { query: "go 1.27", max_results: 5 },
    result: JSON.stringify({ results: [{ title: "Go 1.27 Release Notes", url: "https://go.dev/doc/go1.27" }] }),
  },
};
