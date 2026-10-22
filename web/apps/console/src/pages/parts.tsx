// Pieces the admin pages share (the mockup's .notice, .kv, .copy-line and
// dialogs), and the 403 page.

import { Button } from "@agentkit/ui/components/button";
import { cn } from "@agentkit/ui/lib/utils";
import { Check, Copy, ShieldAlert, TriangleAlert, type LucideIcon } from "lucide-react";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { Link } from "react-router";
import { Empty, Page } from "./page";

/** A notice in a page: an icon, a bold line, a muted line, and actions. */
export function Callout({
  icon: Icon = TriangleAlert,
  title,
  children,
  actions,
  tone = "warn",
}: {
  icon?: LucideIcon;
  title: ReactNode;
  children?: ReactNode;
  actions?: ReactNode;
  tone?: "warn" | "bad" | "info";
}) {
  return (
    <div
      data-tone={tone}
      className={cn(
        "flex flex-wrap items-start gap-x-3 gap-y-2 rounded-md border bg-ak-surface-2 px-3 py-2.5 text-(length:--ak-text-sm)",
        tone === "bad" && "border-ak-bad/40",
      )}
    >
      <Icon
        aria-hidden="true"
        className={cn(
          "mt-0.5 size-4 flex-none",
          tone === "warn" ? "text-ak-warn" : tone === "bad" ? "text-ak-bad" : "text-ak-muted",
        )}
      />
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="font-medium break-words text-ak-text">{title}</span>
        {children ? <span className="break-words text-ak-text-2">{children}</span> : null}
      </div>
      {actions ? <div className="flex gap-2">{actions}</div> : null}
    </div>
  );
}

/** A definition list of a page's facts (the mockup's .kv). */
export function Facts({ rows }: { rows: [string, ReactNode][] }) {
  return (
    <dl className="grid grid-cols-[minmax(0,8rem)_minmax(0,1fr)] gap-x-4 gap-y-2.5 text-(length:--ak-text-sm) max-md:grid-cols-1 max-md:gap-y-0.5">
      {rows.map(([term, value]) => (
        <div key={term} className="contents">
          <dt className="text-ak-muted max-md:mt-2">{term}</dt>
          <dd className="min-w-0 break-words text-ak-text">{value}</dd>
        </div>
      ))}
    </dl>
  );
}

/** A section of a page, under its h2. */
export function Section({ title, children }: { title: ReactNode; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-3">
      <h2 className="text-(length:--ak-text-md) font-semibold text-ak-text">{title}</h2>
      {children}
    </section>
  );
}

/**
 * Copies text to the clipboard, saying so: "Copied" for two seconds, and
 * the same in a live region for screen readers.
 */
export function CopyButton({ text, label }: { text: string; label: string }) {
  const [state, setState] = useState<"idle" | "copied" | "failed">("idle");
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);
  const copy = () => {
    const done = (s: "copied" | "failed") => {
      setState(s);
      clearTimeout(timer.current);
      timer.current = setTimeout(() => setState("idle"), 2000);
    };
    // navigator.clipboard is there only in a secure context (https, or localhost).
    if (!navigator.clipboard) {
      done("failed");
      return;
    }
    navigator.clipboard.writeText(text).then(
      () => done("copied"),
      () => done("failed"),
    );
  };
  return (
    <>
      <Button type="button" variant="outline" size="sm" onClick={copy} className="flex-none">
        {state === "copied" ? <Check aria-hidden="true" /> : <Copy aria-hidden="true" />}
        {state === "copied" ? "Copied" : label}
      </Button>
      <span role="status" className="sr-only">
        {state === "copied" ? "Copied" : state === "failed" ? "Couldn't copy: select the text and copy it" : ""}
      </span>
    </>
  );
}

/** A line of code to copy, with its button; it wraps on phones (docs/UI.md "Toolsets"). */
export function CopyLine({ text, label, name }: { text: string; label: string; name: string }) {
  return (
    <div className="flex items-start gap-2 max-md:flex-col">
      <code
        aria-label={name}
        className="min-w-0 flex-1 rounded-md border bg-ak-surface-2 px-2.5 py-1.5 font-mono text-(length:--ak-text-sm) break-all text-ak-text"
      >
        {text}
      </code>
      <CopyButton text={text} label={label} />
    </div>
  );
}

/**
 * A modal dialog: the native <dialog>, which keeps focus in itself, closes
 * on Esc and needs no injected style (the CSP blocks Radix's scroll lock).
 * Mounted open; onClose runs on Esc as well.
 */
export function Modal({ title, children, onClose }: { title: string; children: ReactNode; onClose: () => void }) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const d = ref.current;
    if (!d) return;
    // The opener gets focus back when the dialog goes.
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    try {
      d.showModal();
    } catch {
      d.setAttribute("open", ""); // jsdom has no showModal
    }
    d.querySelector<HTMLElement>("[data-autofocus]")?.focus();
    return () => {
      if (d.open && typeof d.close === "function") d.close();
      if (opener?.isConnected) opener.focus();
    };
  }, []);
  return (
    <dialog
      ref={ref}
      aria-labelledby="modal-title"
      onCancel={(e) => {
        e.preventDefault();
        onClose();
      }}
      className="m-auto w-[min(440px,calc(100vw-32px))] rounded-lg border bg-ak-surface p-5 text-ak-text shadow-ak backdrop:bg-black/30"
    >
      <h2 id="modal-title" className="mb-3 text-(length:--ak-text-lg) font-semibold">
        {title}
      </h2>
      {children}
    </dialog>
  );
}

/** The 403 page (docs/UI.md "Screens"): admins only, and the way back. */
export function Forbidden() {
  return (
    <Page title="Admins only" heading="body" body="bare">
      <Empty icon={ShieldAlert} title="Admins only">
        <p className="text-ak-muted">This page shows server configuration. Ask an admin if you need access.</p>
        <Button asChild variant="outline" size="sm">
          <Link to="/">Back to chats</Link>
        </Button>
      </Empty>
    </Page>
  );
}
