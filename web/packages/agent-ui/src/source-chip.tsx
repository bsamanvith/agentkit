import { cn } from "@agentkit/ui";

/**
 * A source under an answer: site initial, host and path, linking to the
 * page. Only http(s) URLs make a chip; anything else renders nothing, so a
 * javascript: link from a model can never become clickable.
 */
export function SourceChip({ url, className }: { url: string; className?: string }) {
  let u: URL;
  try {
    u = new URL(url);
  } catch {
    return null;
  }
  if (u.protocol !== "https:" && u.protocol !== "http:") return null;
  const host = u.hostname.replace(/^www\./, "");
  const path = u.pathname === "/" ? "" : u.pathname;
  return (
    <a
      href={u.href}
      target="_blank"
      rel="noopener noreferrer"
      className={cn(
        "inline-flex h-6 max-w-64 items-center gap-1.5 rounded-full border border-ak-line-strong px-2 text-xs text-ak-text-2 hover:bg-accent",
        className,
      )}
    >
      <span
        aria-hidden="true"
        className="grid size-4 place-items-center rounded-full bg-ak-select text-[10px] font-semibold uppercase"
      >
        {host.charAt(0)}
      </span>
      <span className="truncate">
        {host}
        <span className="text-ak-muted">{path}</span>
      </span>
    </a>
  );
}
