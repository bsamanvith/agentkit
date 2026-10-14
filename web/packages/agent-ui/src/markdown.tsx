// Answers are Markdown, rendered to React elements by this small renderer
// (E11 "Untrusted content in the web client", docs/UI.md "Rules"):
//
// - a fixed set of elements: paragraphs, headings, lists, block quotes, code
//   (fenced and inline), tables, rules, emphasis, strikethrough and links;
// - raw HTML is never parsed: `<script>` or `<img onerror=…>` is text, which
//   React escapes;
// - links only to absolute http(s) URLs, opened in a new tab without the
//   opener or a referrer; any other link (javascript:, data:, relative) is
//   its text alone;
// - images are their alt text: the CSP allows no outside images.
//
// The console loads it lazily (React.lazy), so the first screen doesn't wait
// for it.

import { cn } from "@agentkit/ui";
import type { ReactNode } from "react";

/** The URL if it is an absolute http(s) link, else undefined. */
export function safeHref(raw: string): string | undefined {
  let u: URL;
  try {
    u = new URL(raw.trim());
  } catch {
    return undefined;
  }
  return u.protocol === "https:" || u.protocol === "http:" ? u.href : undefined;
}

// ---- Inline ----

// The earliest match wins; at the same place, the earlier alternative.
const inlinePattern =
  /(`+)([\s\S]*?[^`])\1(?!`)|!\[([^\]]*)\]\(([^)\s]*)(?:\s+"[^"]*")?\)|\[((?:[^[\]]|\[[^\]]*\])*)\]\(((?:[^()\s]|\([^()\s]*\))*)(?:\s+"[^"]*")?\)|<(https?:\/\/[^\s<>]+)>|(https?:\/\/[^\s<>()[\]"']*[^\s<>()[\]"'.,;:!?*_~])|\*\*(?=\S)([\s\S]*?\S)\*\*|__(?=\S)([\s\S]*?\S)__(?!\w)|~~(?=\S)([\s\S]*?\S)~~|\*(?=[^\s*])([\s\S]*?[^\s*])\*|(?<!\w)_(?=[^\s_])([\s\S]*?[^\s_])_(?!\w)|\\([\\`*_{}[\]()#+\-.!~|<>])/g;

function inline(text: string, key: string): ReactNode[] {
  const out: ReactNode[] = [];
  let last = 0;
  let i = 0;
  for (const m of text.matchAll(inlinePattern)) {
    const at = m.index;
    if (at > last) out.push(text.slice(last, at));
    last = at + m[0].length;
    const k = `${key}.${i++}`;
    const [, , code, alt, , label, url, angle, bare, strong, strong2, del, em, em2, escaped] = m;
    if (code !== undefined) {
      out.push(
        <code key={k} className="rounded-sm bg-ak-surface-2 px-1 py-0.5 font-mono text-[0.9em]">
          {code.length > 2 && code.startsWith(" ") && code.endsWith(" ") ? code.slice(1, -1) : code}
        </code>,
      );
    } else if (alt !== undefined) {
      out.push(alt);
    } else if (label !== undefined && url !== undefined) {
      out.push(link(url, inline(label, k), k));
    } else if (angle !== undefined || bare !== undefined) {
      const u = angle ?? bare ?? "";
      out.push(link(u, [u], k));
    } else if (strong !== undefined || strong2 !== undefined) {
      out.push(<strong key={k}>{inline(strong ?? strong2 ?? "", k)}</strong>);
    } else if (del !== undefined) {
      out.push(<del key={k}>{inline(del, k)}</del>);
    } else if (em !== undefined || em2 !== undefined) {
      out.push(<em key={k}>{inline(em ?? em2 ?? "", k)}</em>);
    } else if (escaped !== undefined) {
      out.push(escaped);
    } else {
      out.push(m[0]);
    }
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

function link(url: string, children: ReactNode[], key: string): ReactNode {
  const href = safeHref(url);
  if (!href) return <span key={key}>{children}</span>;
  return (
    <a
      key={key}
      href={href}
      target="_blank"
      rel="noopener noreferrer"
      className="text-ak-text underline decoration-ak-line-strong underline-offset-2 hover:decoration-ak-text"
    >
      {children}
    </a>
  );
}

// ---- Blocks ----

const fence = /^ {0,3}(`{3,}|~{3,})\s*([\w+-]*)/;
const heading = /^ {0,3}(#{1,6})\s+(.*?)\s*#*\s*$/;
const rule = /^ {0,3}([-*_])(\s*\1){2,}\s*$/;
const quote = /^ {0,3}>\s?/;
const bullet = /^( {0,3})([-*+])\s+/;
const ordered = /^( {0,3})(\d{1,9})[.)]\s+/;
const tableSep = /^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$/;

function cells(row: string): string[] {
  let r = row.trim();
  if (r.startsWith("|")) r = r.slice(1);
  if (r.endsWith("|") && !r.endsWith("\\|")) r = r.slice(0, -1);
  return r.split(/(?<!\\)\|/).map((c) => c.trim().replace(/\\\|/g, "|"));
}

const startsBlock = (l: string) =>
  fence.test(l) || heading.test(l) || rule.test(l) || quote.test(l) || bullet.test(l) || ordered.test(l);

function blocks(lines: string[], key: string): ReactNode[] {
  const out: ReactNode[] = [];
  let i = 0;
  let n = 0;
  // The line at j ("" past the end), as noUncheckedIndexedAccess wants.
  const at = (j: number) => lines[j] ?? "";
  while (i < lines.length) {
    const line = at(i);
    const k = `${key}.${n++}`;
    if (line.trim() === "") {
      i++;
      continue;
    }
    const f = fence.exec(line);
    if (f) {
      const mark = f[1] ?? "```";
      const close = new RegExp(`^ {0,3}${mark.startsWith("`") ? "`" : "~"}{${mark.length},}\\s*$`);
      const body: string[] = [];
      i++;
      while (i < lines.length && !close.test(at(i))) body.push(at(i++));
      i++; // the closing fence, or the end
      out.push(
        <pre
          key={k}
          tabIndex={0}
          aria-label={f[2] ? `Code (${f[2]})` : "Code"}
          className="overflow-x-auto rounded-md border bg-ak-surface-2 p-3 font-mono text-(length:--ak-text-sm) leading-relaxed"
        >
          <code>{body.join("\n")}</code>
        </pre>,
      );
      continue;
    }
    const h = heading.exec(line);
    if (h) {
      const level = (h[1] ?? "").length;
      const Tag = (level <= 2 ? "h3" : "h4") as "h3" | "h4";
      out.push(
        <Tag key={k} className={cn("font-semibold text-ak-text", level <= 2 ? "text-(length:--ak-text-md)" : "")}>
          {inline(h[2] ?? "", k)}
        </Tag>,
      );
      i++;
      continue;
    }
    if (rule.test(line)) {
      out.push(<hr key={k} className="border-ak-line" />);
      i++;
      continue;
    }
    if (quote.test(line)) {
      const body: string[] = [];
      while (i < lines.length && at(i).trim() !== "" && (quote.test(at(i)) || body.length > 0)) {
        body.push(at(i).replace(quote, ""));
        i++;
      }
      out.push(
        <blockquote key={k} className="border-l-2 border-ak-line-strong pl-3 text-ak-text-2">
          {blocks(body, k)}
        </blockquote>,
      );
      continue;
    }
    const b = bullet.exec(line);
    const o = b ? null : ordered.exec(line);
    if (b || o) {
      const marker = b ? bullet : ordered;
      const items: string[][] = [];
      let indent = 0;
      while (i < lines.length) {
        const l = at(i);
        const m = marker.exec(l);
        if (m) {
          indent = m[0].length;
          items.push([l.slice(indent)]);
          i++;
          continue;
        }
        if (l.trim() === "") {
          // A blank line ends the list unless the next line continues it.
          const next = lines[i + 1];
          if (next !== undefined && (marker.test(next) || /^\s{2,}\S/.test(next))) {
            items.at(-1)?.push("");
            i++;
            continue;
          }
          break;
        }
        const leading = l.length - l.trimStart().length;
        if (leading >= 2 || !startsBlock(l)) {
          items.at(-1)?.push(l.slice(Math.min(leading, indent)));
          i++;
          continue;
        }
        break;
      }
      const children = items.map((it, j) => (
        <li key={`${k}.${j}`} className="pl-1">
          {it.length === 1 ? inline(it[0] ?? "", `${k}.${j}`) : blocks(it, `${k}.${j}`)}
        </li>
      ));
      const start = o ? Number(o[2]) : 1;
      out.push(
        b ? (
          <ul key={k} className="flex list-disc flex-col gap-1 pl-5">
            {children}
          </ul>
        ) : (
          <ol key={k} start={start === 1 ? undefined : start} className="flex list-decimal flex-col gap-1 pl-5">
            {children}
          </ol>
        ),
      );
      continue;
    }
    if (line.includes("|") && i + 1 < lines.length && tableSep.test(at(i + 1))) {
      const head = cells(line);
      const rows: string[][] = [];
      i += 2;
      while (i < lines.length && at(i).includes("|") && at(i).trim() !== "") rows.push(cells(at(i++)));
      out.push(
        <div key={k} tabIndex={0} aria-label="Table" className="overflow-x-auto">
          <table className="w-full border-collapse text-(length:--ak-text-sm)">
            <thead>
              <tr>
                {head.map((c, j) => (
                  <th key={j} className="border-b border-ak-line-strong px-2 py-1 text-left font-semibold">
                    {inline(c, `${k}.h${j}`)}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((r, ri) => (
                <tr key={ri}>
                  {head.map((_, j) => (
                    <td key={j} className="border-b border-ak-line px-2 py-1 align-top">
                      {inline(r[j] ?? "", `${k}.${ri}.${j}`)}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>,
      );
      continue;
    }
    // A paragraph: lines up to a blank line or the start of another block.
    const para: string[] = [line.trim()];
    i++;
    while (i < lines.length && at(i).trim() !== "" && !startsBlock(at(i))) para.push(at(i++).trim());
    out.push(<p key={k}>{inline(para.join("\n"), k)}</p>);
  }
  return out;
}

/** An answer's Markdown as React elements (see the file's comment for what is allowed). */
export function Markdown({ text, className }: { text: string; className?: string }) {
  return (
    <div className={cn("flex flex-col gap-3 leading-relaxed text-ak-text [&_p]:whitespace-pre-line", className)}>
      {blocks(text.replace(/\r\n?/g, "\n").split("\n"), "m")}
    </div>
  );
}

export default Markdown;
