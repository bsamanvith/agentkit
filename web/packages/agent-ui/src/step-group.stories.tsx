import type { Meta, Story } from "./stories";
import { StepGroup, type StepGroupProps } from "./step-group";

export default { title: "agent-ui/StepGroup", component: StepGroup } satisfies Meta<StepGroupProps>;

export const OneCall: Story<StepGroupProps> = {
  args: { steps: [{ id: "1", kind: "calc", state: "done", label: "Calculated", detail: "17 × 23", durationMs: 2 }] },
};
export const ThreeCalls: Story<StepGroupProps> = {
  args: {
    steps: [
      { id: "1", kind: "search", state: "done", label: "Searched the web", detail: "“go 1.27”", durationMs: 1400 },
      { id: "2", kind: "page", state: "done", label: "Read 2 pages", detail: "go.dev", durationMs: 1100 },
      { id: "3", kind: "calc", state: "failed", label: "Calculated", durationMs: 100 },
    ],
  },
};
