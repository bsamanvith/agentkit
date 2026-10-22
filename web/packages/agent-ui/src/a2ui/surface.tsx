// The A2UI surface: an agent's form, choice or card (A2UI v0.9, the basic
// catalog of pkg/a2ui), drawn with our own components (docs/UI.md "A2UI
// basic catalog"). Its own entry ("@agentkit/agent-ui/a2ui"), so apps load
// it lazily, only for a thread that has a surface.
//
// The agent sends data, never markup or style:
// - every string is a React text child (Text's Markdown is not parsed);
// - the theme (colors, icon) is ignored: surfaces look like the console;
// - image and link URLs, literal or bound or computed, pass safeUrl
//   (a2ui.SafeURL), and images also allowImage (by default this page's own
//   origin, which is all the CSP's img-src allows); others are dropped;
// - a component this renderer doesn't draw is a short "Unsupported
//   component" note, never its data.

import { Button } from "@agentkit/ui/components/button";
import { cn } from "@agentkit/ui/lib/utils";
import {
  ArrowLeft,
  ArrowRight,
  Bell,
  BellOff,
  CalendarCheck,
  CalendarDays,
  Camera,
  Check,
  CircleAlert,
  CircleHelp,
  CircleUser,
  CreditCard,
  Download,
  Ellipsis,
  EllipsisVertical,
  ExternalLink,
  Eye,
  EyeOff,
  FastForward,
  Folder,
  Heart,
  HeartOff,
  House,
  Image as ImageIcon,
  Info,
  Lock,
  LockOpen,
  Mail,
  MapPin,
  Menu,
  Paperclip,
  Pause,
  Pencil,
  Phone,
  Play,
  Plus,
  Printer,
  RefreshCw,
  Rewind,
  Search,
  Send,
  Settings,
  Share2,
  ShoppingCart,
  SkipBack,
  SkipForward,
  Smartphone,
  Square,
  Star,
  StarHalf,
  StarOff,
  Trash2,
  TriangleAlert,
  Upload,
  User,
  Volume1,
  Volume2,
  VolumeOff,
  VolumeX,
  X,
  type LucideIcon,
} from "lucide-react";
import { useId, useState, type ReactNode } from "react";
import { safeUrl, withEdits, type Component, type Surface, type UIAction } from "./model";
import { asText, bool, boundPath, failingChecks, list, resolve, resolveContext, text, type Scope } from "./resolve";

export type SurfaceState = "live" | "busy" | "sent";

export type SurfaceProps = {
  surface: Surface;
  /**
   * live: the person can use it; busy: a run of the conversation is going
   * on, so it waits; sent: the person's action went to the agent, which
   * hasn't changed the surface since (it keeps the values).
   */
  state?: SurfaceState;
  /** The person's edits (path to value, SurfaceEdits); kept inside when absent. */
  edits?: ReadonlyMap<string, unknown>;
  onEdit?: (path: string, value: unknown) => void;
  /** A button's action event, its context resolved. */
  onAction?: (a: UIAction) => void;
  /** Whether an image may load from this (safe) URL: by default only from this page's origin (the CSP's img-src). */
  allowImage?: (url: URL) => boolean;
  /** The surface's accessible name. */
  label?: string;
  className?: string;
};

/** The components this renderer draws: pkg/a2ui's catalog (a2ui.Components). */
export const drawn = [
  "Button",
  "Card",
  "CheckBox",
  "ChoicePicker",
  "Column",
  "DateTimeInput",
  "Divider",
  "Icon",
  "Image",
  "List",
  "Row",
  "Text",
  "TextField",
] as const;

const sameOrigin = (u: URL) => typeof location !== "undefined" && u.origin === location.origin;

const icons: Record<string, LucideIcon> = {
  accountCircle: CircleUser,
  add: Plus,
  arrowBack: ArrowLeft,
  arrowForward: ArrowRight,
  attachFile: Paperclip,
  calendarToday: CalendarDays,
  call: Phone,
  camera: Camera,
  check: Check,
  close: X,
  delete: Trash2,
  download: Download,
  edit: Pencil,
  event: CalendarCheck,
  error: CircleAlert,
  fastForward: FastForward,
  favorite: Heart,
  favoriteOff: HeartOff,
  folder: Folder,
  help: CircleHelp,
  home: House,
  info: Info,
  locationOn: MapPin,
  lock: Lock,
  lockOpen: LockOpen,
  mail: Mail,
  menu: Menu,
  moreVert: EllipsisVertical,
  moreHoriz: Ellipsis,
  notificationsOff: BellOff,
  notifications: Bell,
  pause: Pause,
  payment: CreditCard,
  person: User,
  phone: Smartphone,
  photo: ImageIcon,
  play: Play,
  print: Printer,
  refresh: RefreshCw,
  rewind: Rewind,
  search: Search,
  send: Send,
  settings: Settings,
  share: Share2,
  shoppingCart: ShoppingCart,
  skipNext: SkipForward,
  skipPrevious: SkipBack,
  star: Star,
  starHalf: StarHalf,
  starOff: StarOff,
  stop: Square,
  upload: Upload,
  visibility: Eye,
  visibilityOff: EyeOff,
  volumeDown: Volume1,
  volumeMute: VolumeX,
  volumeOff: VolumeOff,
  volumeUp: Volume2,
  warning: TriangleAlert,
};

// Class names are written out whole, so Tailwind sees them.
const justify: Record<string, string> = {
  start: "justify-start",
  center: "justify-center",
  end: "justify-end",
  spaceBetween: "justify-between",
  spaceAround: "justify-around",
  spaceEvenly: "justify-evenly",
  stretch: "justify-stretch",
};
const align: Record<string, string> = {
  start: "items-start",
  center: "items-center",
  end: "items-end",
  stretch: "items-stretch",
};
const weights: Record<number, string> = {
  1: "min-w-0 flex-[1_1_10rem]",
  2: "min-w-0 flex-[2_1_10rem]",
  3: "min-w-0 flex-[3_1_10rem]",
  4: "min-w-0 flex-[4_1_10rem]",
};
const textStyle: Record<string, string> = {
  h1: "text-(length:--ak-text-lg) font-semibold",
  h2: "text-(length:--ak-text-lg) font-semibold",
  h3: "text-(length:--ak-text-md) font-semibold",
  h4: "text-(length:--ak-text-md) font-semibold",
  h5: "text-(length:--ak-text-md) font-semibold",
  caption: "text-(length:--ak-text-xs) text-ak-muted",
  body: "",
};
const imageSize: Record<string, string> = {
  icon: "size-6",
  avatar: "size-10 rounded-full",
  smallFeature: "h-24 w-auto max-w-full",
  mediumFeature: "h-40 w-auto max-w-full",
  largeFeature: "h-64 w-auto max-w-full",
  header: "h-32 w-full",
};
const imageFit: Record<string, string> = {
  contain: "object-contain",
  cover: "object-cover",
  fill: "object-fill",
  none: "object-none",
  scaleDown: "object-scale-down",
};
const field =
  "w-full min-w-0 rounded-md border border-(--ak-control) bg-ak-surface px-3 py-1.5 text-ak-text outline-none focus-visible:ring-2 focus-visible:ring-(--ak-focus) focus-visible:ring-offset-2 focus-visible:ring-offset-ak-surface disabled:cursor-not-allowed disabled:opacity-60 aria-invalid:border-ak-bad";
const labelStyle = "text-(length:--ak-text-sm) font-medium text-ak-text-2";

/** The longest path from the root drawn (a2ui.MaxDepth); deeper is cut. */
const maxDepth = 32;

type Ctx = {
  surface: Surface;
  data: unknown;
  edit: (path: string, value: unknown) => void;
  act?: ((a: UIAction) => void) | undefined;
  allowImage: (url: URL) => boolean;
  uid: string;
  enabled: boolean;
};

const str = (c: Component, k: string) => (typeof c[k] === "string" ? (c[k] as string) : undefined);

/** An element ID for a component drawn in a scope (a template draws one per item). */
function domId(ctx: Ctx, id: string, scope: Scope) {
  return `${ctx.uid}-${id}${scope.base}`.replace(/[^A-Za-z0-9_:.-]/g, "_");
}

/** The heading markers of Text's Markdown ("# Contact us"): dropped, the variant styles it. */
const headingMarks = /^#{1,6}\s+/;

/** The nodes of a ChildList: its IDs, or its template once per item. */
function childNodes({ ctx, scope, depth, seen }: Draw, value: unknown): ReactNode[] {
  if (Array.isArray(value)) {
    return value
      .filter((id): id is string => typeof id === "string")
      .map((id) => <Node key={id} ctx={ctx} id={id} scope={scope} depth={depth + 1} seen={seen} />);
  }
  if (value && typeof value === "object") {
    // A template: componentId once per item of the list at path.
    const t = value as { componentId?: unknown; path?: unknown };
    if (typeof t.componentId !== "string" || typeof t.path !== "string") return [];
    const at = boundPath({ path: t.path }, scope) ?? t.path;
    const items = resolve({ path: at }, scope);
    if (!Array.isArray(items)) return [];
    const id = t.componentId;
    return items.map((_, i) => (
      <Node
        key={i}
        ctx={ctx}
        id={id}
        scope={{ ...scope, base: `${at === "/" ? "" : at}/${i}` }}
        depth={depth + 1}
        seen={seen}
      />
    ));
  }
  return [];
}

type Draw = { ctx: Ctx; scope: Scope; depth: number; seen: ReadonlySet<string> };

function Node({ ctx, id, scope, depth, seen }: Draw & { id: string }) {
  const c = ctx.surface.components.get(id);
  // Not (yet) defined: a streamed surface fills in as its components come.
  if (!c || depth > maxDepth || seen.has(id)) return null;
  const next = new Set(seen).add(id);
  const d: Draw = { ctx, scope, depth, seen: next };
  const weight = typeof c.weight === "number" ? weights[Math.min(4, Math.max(1, Math.round(c.weight)))] : undefined;
  const a11y = (c.accessibility ?? {}) as { label?: unknown; description?: unknown };
  const ariaLabel = a11y.label !== undefined ? text(a11y.label, scope) || undefined : undefined;
  const el = drawComponent(c, d, ariaLabel);
  return weight ? <div className={weight}>{el}</div> : el;
}

function drawComponent(c: Component, d: Draw, ariaLabel: string | undefined): ReactNode {
  const { ctx, scope } = d;
  switch (c.component) {
    case "Text": {
      const variant = str(c, "variant") ?? "body";
      const t = text(c.text, scope);
      const level = /^h([1-5])$/.exec(variant)?.[1];
      return (
        <div
          className={cn("break-words whitespace-pre-wrap", textStyle[variant] ?? textStyle.body)}
          {...(level ? { role: "heading", "aria-level": Math.min(6, Number(level) + 2) } : {})}
          aria-label={ariaLabel}
        >
          {level ? t.replace(headingMarks, "") : t}
        </div>
      );
    }
    case "Row":
    case "Column":
    case "List": {
      const horizontal = c.component === "Row" || (c.component === "List" && c.direction === "horizontal");
      return (
        <div
          role={c.component === "List" ? "list" : undefined}
          aria-label={ariaLabel}
          className={cn(
            "flex min-w-0 gap-2",
            horizontal ? (c.component === "List" ? "flex-row overflow-x-auto" : "flex-row flex-wrap") : "flex-col",
            justify[str(c, "justify") ?? ""],
            align[str(c, "align") ?? ""] ?? "items-stretch",
          )}
        >
          {c.component === "List"
            ? childNodes(d, c.children).map((it, i) => (
                <div role="listitem" key={i} className="min-w-0">
                  {it}
                </div>
              ))
            : childNodes(d, c.children)}
        </div>
      );
    }
    case "Card":
      return (
        <div aria-label={ariaLabel} className="min-w-0 rounded-xl border border-ak-line bg-ak-surface p-3">
          {typeof c.child === "string" ? <Node {...d} id={c.child} depth={d.depth + 1} /> : null}
        </div>
      );
    case "Divider":
      return c.axis === "vertical" ? (
        <div role="separator" aria-orientation="vertical" className="w-px self-stretch bg-ak-line" />
      ) : (
        <hr className="border-0 border-t border-ak-line" />
      );
    case "Icon": {
      const name = c.name;
      if (name && typeof name === "object" && "svgPath" in name) {
        const path = (name as { svgPath?: unknown }).svgPath;
        if (typeof path !== "string") return null;
        // A path's geometry only: no element or attribute of the agent's.
        return (
          <svg
            viewBox="0 0 24 24"
            role={ariaLabel ? "img" : undefined}
            aria-hidden={ariaLabel ? undefined : true}
            aria-label={ariaLabel}
            className="size-5 fill-current text-ak-text-2"
          >
            <path d={path} />
          </svg>
        );
      }
      // A bound name is the agent's data: only the table's own keys, never
      // Object.prototype's ("constructor" would be drawn as a component).
      const n = asText(resolve(name, scope));
      const Icon = Object.hasOwn(icons, n) ? icons[n] : undefined;
      return Icon ? (
        <Icon
          role={ariaLabel ? "img" : undefined}
          aria-hidden={ariaLabel ? undefined : true}
          aria-label={ariaLabel}
          className="size-5 flex-none text-ak-text-2"
        />
      ) : null;
    }
    case "Image": {
      const url = resolve(c.url, scope);
      const description = text(c.description, scope);
      const u = safeUrl(url) ? new URL(url) : undefined;
      if (!u || !ctx.allowImage(u)) {
        return (
          <p className="text-(length:--ak-text-sm) text-ak-muted" data-dropped="image">
            {description ? `Image not shown: ${description}` : "Image not shown"}
          </p>
        );
      }
      return (
        <img
          src={u.href}
          alt={description}
          referrerPolicy="no-referrer"
          className={cn(
            "max-w-full rounded-md",
            imageSize[str(c, "variant") ?? "mediumFeature"],
            imageFit[str(c, "fit") ?? "fill"],
          )}
        />
      );
    }
    case "Button":
      return <ActionButton {...d} c={c} ariaLabel={ariaLabel} />;
    case "TextField":
      return <TextInput {...d} c={c} ariaLabel={ariaLabel} />;
    case "CheckBox":
      return <CheckInput {...d} c={c} ariaLabel={ariaLabel} />;
    case "ChoicePicker":
      return <Choices {...d} c={c} ariaLabel={ariaLabel} />;
    case "DateTimeInput":
      return <DateInput {...d} c={c} ariaLabel={ariaLabel} />;
  }
  return (
    <p className="text-(length:--ak-text-sm) text-ak-muted" data-unsupported={c.component}>
      Unsupported component: <code className="font-mono">{c.component}</code>
    </p>
  );
}

/** A field's failing checks, linked to it. */
function Problems({ id, messages }: { id: string; messages: string[] }) {
  if (messages.length === 0) return null;
  return (
    <div id={id} className="text-(length:--ak-text-xs) text-ak-bad">
      {messages.map((m, i) => (
        <div key={i}>{m}</div>
      ))}
    </div>
  );
}

type Field = Draw & { c: Component; ariaLabel: string | undefined };

/** A field's value and how to change it: bound to the data model, or held by the field itself. */
function useField(f: Field, initial: unknown) {
  const path = boundPath(f.c.value, f.scope);
  const [own, setOwn] = useState(initial);
  const value = path !== undefined ? resolve(f.c.value, f.scope) : own;
  const set = (v: unknown) => (path !== undefined ? f.ctx.edit(path, v) : setOwn(v));
  return [value, set] as const;
}

function ActionButton({ ctx, scope, depth, seen, c, ariaLabel }: Field) {
  const failing = failingChecks(c.checks, scope);
  const variant = c.variant === "primary" ? "default" : c.variant === "borderless" ? "ghost" : "outline";
  const label =
    typeof c.child === "string" ? <Node ctx={ctx} id={c.child} scope={scope} depth={depth + 1} seen={seen} /> : null;
  const action = (c.action ?? {}) as { event?: { name?: unknown; context?: unknown }; functionCall?: unknown };
  const problems = `${domId(ctx, c.id, scope)}-problems`;
  if (action.functionCall && typeof action.functionCall === "object") {
    const fc = action.functionCall as { call?: unknown; args?: { url?: unknown } };
    if (fc.call === "openUrl") {
      const url = resolve(fc.args?.url, scope);
      if (!safeUrl(url)) {
        return (
          <span className="inline-flex flex-wrap items-center gap-2" data-dropped="link">
            <Button type="button" variant={variant} size="sm" disabled aria-label={ariaLabel}>
              {label}
            </Button>
            <span className="text-(length:--ak-text-xs) text-ak-muted">Link removed: not a web address.</span>
          </span>
        );
      }
      return (
        <Button asChild variant={variant} size="sm" className="self-start">
          <a href={new URL(url).href} target="_blank" rel="noopener noreferrer" aria-label={ariaLabel}>
            {label}
            <ExternalLink aria-hidden="true" className="size-3.5" />
          </a>
        </Button>
      );
    }
  }
  const ev = action.event;
  const name = typeof ev?.name === "string" ? ev.name : undefined;
  return (
    <div className="flex flex-col items-start gap-1">
      <Button
        type="button"
        variant={variant}
        size="sm"
        aria-label={ariaLabel}
        aria-describedby={failing.length > 0 ? problems : undefined}
        disabled={!ctx.enabled || failing.length > 0 || name === undefined || !ctx.act}
        onClick={() => {
          if (!name || !ctx.act) return;
          ctx.act({
            surfaceId: ctx.surface.id,
            componentId: c.id,
            name,
            context: resolveContext(ev?.context, scope),
          });
        }}
      >
        {label}
      </Button>
      <Problems id={problems} messages={failing} />
    </div>
  );
}

function TextInput(f: Field) {
  const { ctx, scope, c } = f;
  const [value, set] = useField(f, text(c.value, scope));
  const id = domId(ctx, c.id, scope);
  const v = asText(value);
  const failing = failingChecks(c.checks, scope);
  const pattern = str(c, "validationRegexp");
  if (pattern !== undefined && v !== "") {
    let ok = true;
    try {
      ok = new RegExp(pattern).test(v);
    } catch {
      // A pattern the browser can't read checks nothing.
    }
    if (!ok) failing.push("This doesn't match the expected format.");
  }
  const variant = str(c, "variant") ?? "shortText";
  const common = {
    id,
    value: v,
    disabled: !ctx.enabled,
    "aria-invalid": failing.length > 0 || undefined,
    "aria-describedby": failing.length > 0 ? `${id}-problems` : undefined,
    "aria-label": f.ariaLabel,
  };
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <label htmlFor={id} className={labelStyle}>
        {text(c.label, scope)}
      </label>
      {variant === "longText" ? (
        <textarea {...common} rows={3} className={cn(field, "resize-y")} onChange={(e) => set(e.target.value)} />
      ) : (
        <input
          {...common}
          type={variant === "number" ? "number" : variant === "obscured" ? "password" : "text"}
          className={cn(field, "h-9")}
          onChange={(e) => set(e.target.value)}
        />
      )}
      <Problems id={`${id}-problems`} messages={failing} />
    </div>
  );
}

function CheckInput(f: Field) {
  const { ctx, scope, c } = f;
  const [value, set] = useField(f, bool(c.value, scope));
  const id = domId(ctx, c.id, scope);
  const failing = failingChecks(c.checks, scope);
  return (
    <div className="flex flex-col gap-1">
      <label htmlFor={id} className="inline-flex min-h-6 items-center gap-2 text-ak-text">
        <input
          id={id}
          type="checkbox"
          checked={value === true}
          disabled={!ctx.enabled}
          aria-label={f.ariaLabel}
          aria-describedby={failing.length > 0 ? `${id}-problems` : undefined}
          className="size-4 flex-none accent-(--ak-primary)"
          onChange={(e) => set(e.target.checked)}
        />
        <span>{text(c.label, scope)}</span>
      </label>
      <Problems id={`${id}-problems`} messages={failing} />
    </div>
  );
}

function Choices(f: Field) {
  const { ctx, scope, c } = f;
  const [value, set] = useField(f, list(c.value, scope));
  const chosen = Array.isArray(value) ? value.filter((x): x is string => typeof x === "string") : [];
  const id = domId(ctx, c.id, scope);
  const many = c.variant === "multipleSelection";
  const options = (Array.isArray(c.options) ? c.options : []).flatMap((o) =>
    o && typeof o === "object" && typeof (o as { value?: unknown }).value === "string"
      ? [{ value: (o as { value: string }).value, label: text((o as { label?: unknown }).label, scope) }]
      : [],
  );
  const toggle = (v: string) =>
    set(many ? (chosen.includes(v) ? chosen.filter((x) => x !== v) : [...chosen, v]) : chosen[0] === v ? [] : [v]);
  const failing = failingChecks(c.checks, scope);
  const label = text(c.label, scope);
  const described = failing.length > 0 ? `${id}-problems` : undefined;

  // One of many options: a select, which takes less room.
  if (!many && c.displayStyle !== "chips" && options.length > 6) {
    return (
      <div className="flex min-w-0 flex-col gap-1">
        <label htmlFor={id} className={labelStyle}>
          {label || "Choose one"}
        </label>
        <select
          id={id}
          value={chosen[0] ?? ""}
          disabled={!ctx.enabled}
          aria-describedby={described}
          className={cn(field, "h-9")}
          onChange={(e) => set(e.target.value === "" ? [] : [e.target.value])}
        >
          <option value="">Choose…</option>
          {options.map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
        <Problems id={`${id}-problems`} messages={failing} />
      </div>
    );
  }
  return (
    <fieldset className="flex min-w-0 flex-col gap-1.5" aria-describedby={described} aria-label={f.ariaLabel}>
      {label ? <legend className={cn(labelStyle, "mb-1.5")}>{label}</legend> : null}
      {c.displayStyle === "chips" ? (
        <div className="flex flex-wrap gap-1.5">
          {options.map((o) => {
            const on = chosen.includes(o.value);
            return (
              <button
                key={o.value}
                type="button"
                aria-pressed={on}
                disabled={!ctx.enabled}
                onClick={() => toggle(o.value)}
                className={cn(
                  "inline-flex h-7 items-center gap-1 rounded-full border px-3 text-(length:--ak-text-sm) outline-none focus-visible:ring-2 focus-visible:ring-(--ak-focus) focus-visible:ring-offset-2 focus-visible:ring-offset-ak-surface disabled:cursor-not-allowed disabled:opacity-60",
                  on
                    ? "border-(--ak-primary) bg-(--ak-primary) text-(--ak-on-primary)"
                    : "border-ak-line-strong bg-ak-surface text-ak-text-2 hover:bg-ak-hover",
                )}
              >
                {on ? <Check aria-hidden="true" className="size-3.5" /> : null}
                {o.label}
              </button>
            );
          })}
        </div>
      ) : (
        options.map((o) => (
          <label key={o.value} className="inline-flex min-h-6 items-center gap-2 text-ak-text">
            <input
              type={many ? "checkbox" : "radio"}
              name={id}
              value={o.value}
              checked={chosen.includes(o.value)}
              disabled={!ctx.enabled}
              className="size-4 flex-none accent-(--ak-primary)"
              onChange={() => toggle(o.value)}
            />
            <span>{o.label}</span>
          </label>
        ))
      )}
      <Problems id={`${id}-problems`} messages={failing} />
    </fieldset>
  );
}

function DateInput(f: Field) {
  const { ctx, scope, c } = f;
  const [value, set] = useField(f, text(c.value, scope));
  const id = domId(ctx, c.id, scope);
  const date = c.enableDate === true;
  const time = c.enableTime === true;
  const type = date && time ? "datetime-local" : time ? "time" : "date";
  const failing = failingChecks(c.checks, scope);
  const label = text(c.label, scope);
  const min = text(c.min, scope);
  const max = text(c.max, scope);
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <label htmlFor={id} className={labelStyle}>
        {label || (type === "time" ? "Time" : "Date")}
      </label>
      <input
        id={id}
        type={type}
        value={asText(value)}
        min={min || undefined}
        max={max || undefined}
        disabled={!ctx.enabled}
        aria-label={f.ariaLabel}
        aria-describedby={failing.length > 0 ? `${id}-problems` : undefined}
        className={cn(field, "h-9")}
        onChange={(e) => set(e.target.value)}
      />
      <Problems id={`${id}-problems`} messages={failing} />
    </div>
  );
}

/**
 * A surface: its components from the root, with the data model and the
 * person's edits. Nothing shows until the root is defined; a deleted
 * surface shows nothing.
 */
export function A2UISurface({
  surface,
  state = "live",
  edits,
  onEdit,
  onAction,
  allowImage = sameOrigin,
  label = "Form",
  className,
}: SurfaceProps) {
  const uid = useId();
  const [own, setOwn] = useState<ReadonlyMap<string, unknown>>(new Map());
  if (surface.deleted || !surface.components.has("root")) return null;
  const values = edits ?? own;
  const ctx: Ctx = {
    surface,
    data: withEdits(surface.data, values),
    edit: onEdit ?? ((path, value) => setOwn((m) => new Map(m).set(path, value))),
    act: onAction,
    allowImage,
    uid,
    enabled: state === "live",
  };
  return (
    <section
      aria-label={label}
      data-surface-id={surface.id}
      data-state={state}
      className={cn("flex min-w-0 flex-col gap-1.5 text-ak-text", className)}
    >
      <Node ctx={ctx} id="root" scope={{ data: ctx.data, base: "" }} depth={0} seen={new Set()} />
      {state === "sent" ? (
        <p className="inline-flex items-center gap-1 text-(length:--ak-text-xs) text-ak-muted">
          <Check aria-hidden="true" className="size-3.5" />
          Sent
        </p>
      ) : null}
    </section>
  );
}

export default A2UISurface;
