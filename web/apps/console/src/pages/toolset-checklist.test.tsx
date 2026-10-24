// The toolset checklist the agent builder uses: connector names, tool
// counts, what needs connecting, and checking toolsets on and off.

import type { Toolset } from "@agentkit/agent-ui/api";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it } from "vitest";
import { ToolsetChecklist } from "./toolset-checklist";

const toolsets: Toolset[] = [
  {
    name: "gmail",
    type: "gmail",
    status: "ready",
    tool_count: 6,
    credential: { status: "not_connected", slot: "gmail", scope: "user" },
  },
  { name: "math", type: "math", status: "ready", tool_count: 1 },
  { name: "crm", type: "hubspot", status: "unavailable", tool_count: 0 },
];

function Harness({ initial }: { initial: string[] }) {
  const [value, setValue] = useState(initial);
  return (
    <>
      <ToolsetChecklist toolsets={toolsets} value={value} onChange={setValue} />
      <output>{value.join(",")}</output>
    </>
  );
}

describe("ToolsetChecklist", () => {
  it("names connectors, says what needs connecting, and checks toolsets on and off", async () => {
    const user = userEvent.setup();
    render(<Harness initial={["math"]} />);
    const gmail = screen.getByRole("checkbox", { name: /Gmail\s*gmail/ });
    const math = screen.getByRole("checkbox", { name: /^math/ });
    expect(screen.getByRole("checkbox", { name: /HubSpot\s*crm/ })).not.toBeChecked();
    expect(math).toBeChecked();
    expect(gmail).not.toBeChecked();
    expect(screen.getByText("6 tools · Not connected")).toBeInTheDocument();
    expect(screen.getByText("1 tool")).toBeInTheDocument();
    expect(screen.getByText("0 tools · unavailable")).toBeInTheDocument();
    await user.click(gmail);
    expect(screen.getByRole("status")).toHaveTextContent("math,gmail");
    await user.click(math);
    expect(screen.getByRole("status")).toHaveTextContent(/^gmail$/);
  });
});
