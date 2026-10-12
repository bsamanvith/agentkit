import { Message, type MessageProps } from "./message";
import type { Meta, Story } from "./stories";

export default { title: "agent-ui/Message", component: Message } satisfies Meta<MessageProps>;

export const User: Story<MessageProps> = { args: { role: "user", text: "What changed in the latest Go release?" } };
export const Pending: Story<MessageProps> = { args: { role: "user", text: "Still sending…", state: "pending" } };
export const Assistant: Story<MessageProps> = {
  args: { role: "assistant", text: "Go 1.27 was released in August. The main changes are…" },
};
export const Streaming: Story<MessageProps> = { args: { role: "assistant", text: "Go 1.27 was", state: "streaming" } };
