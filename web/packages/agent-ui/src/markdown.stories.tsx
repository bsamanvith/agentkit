import { Markdown } from "./markdown";
import type { Meta, Story } from "./stories";

type Props = Parameters<typeof Markdown>[0];

export default { title: "agent-ui/Markdown", component: Markdown } satisfies Meta<Props>;

export const Answer: Story<Props> = {
  args: {
    text: 'Go **1.27** is out ([notes](https://go.dev/doc/go1.27)).\n\n- faster builds\n- `iter` helpers\n\n```go\nfmt.Println("hi")\n```',
  },
};
export const Hostile: Story<Props> = {
  args: { text: "<script>alert(1)</script> [x](javascript:alert(1)) <img src=x onerror=alert(1)>" },
};
