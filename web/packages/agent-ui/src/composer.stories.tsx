import { Composer, type ComposerProps } from "./composer";
import type { Meta, Story } from "./stories";

export default { title: "agent-ui/Composer", component: Composer } satisfies Meta<ComposerProps>;

const noop = () => {};

export const Idle: Story<ComposerProps> = { args: { onSend: noop } };
export const Running: Story<ComposerProps> = { args: { onSend: noop, running: true, onStop: noop } };
export const Disabled: Story<ComposerProps> = {
  args: { onSend: noop, disabledReason: "This agent no longer exists, so this conversation is read-only." },
};
