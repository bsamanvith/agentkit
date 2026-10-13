import { Status, type StatusTone } from "@agentkit/ui";
import type { ReactNode } from "react";
import { Link } from "react-router";

/** A row of an admin list: name (mono), a muted line, and a status dot. */
export function ListRow({
  to,
  name,
  sub,
  tone,
  word,
  current,
  chevron,
}: {
  to: string;
  name: string;
  sub: string;
  tone: StatusTone;
  word: string;
  current?: boolean;
  chevron?: ReactNode;
}) {
  return (
    <Link
      to={to}
      aria-current={current ? "page" : undefined}
      className="flex min-h-10 items-center gap-2 rounded-md px-2 py-1.5 text-ak-text-2 hover:bg-ak-hover hover:text-ak-text aria-[current=page]:bg-ak-select aria-[current=page]:text-ak-text max-md:min-h-11"
    >
      <span className="flex min-w-0 flex-1 flex-col">
        <span className="truncate font-mono text-(length:--ak-text-sm)">{name}</span>
        <span className="truncate text-xs text-ak-muted">{sub}</span>
      </span>
      <Status tone={tone} hideLabel>
        {word}
      </Status>
      {chevron}
    </Link>
  );
}
