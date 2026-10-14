import type { Meta, Story } from "./stories";
import { UsageLine } from "./usage-line";

type Props = Parameters<typeof UsageLine>[0];

export default { title: "agent-ui/UsageLine", component: UsageLine } satisfies Meta<Props>;

export const Run: Story<Props> = {
  args: { turns: 2, usage: { input_tokens: 120, output_tokens: 80, cache_read_input_tokens: 900 }, durationMs: 3200 },
};
