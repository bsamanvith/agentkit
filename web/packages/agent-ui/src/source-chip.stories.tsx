import type { Meta, Story } from "./stories";
import { SourceChip } from "./source-chip";

type Props = { url: string };

export default { title: "agent-ui/SourceChip", component: SourceChip } satisfies Meta<Props>;

export const Page: Story<Props> = { args: { url: "https://go.dev/doc/go1.27" } };
export const Site: Story<Props> = { args: { url: "https://www.example.com/" } };
