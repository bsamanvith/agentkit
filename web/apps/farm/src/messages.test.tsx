import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MessagesPage } from "./messages";
import { describeRoutes, describeTree, e164, receiptWords } from "./messages-data";
import { keyBytes } from "./push";
import type { Farm, Lane, NotifyStatus, Recipient } from "./types";

const farms = [
  { id: "farm_1", name: "East field" },
  { id: "farm_2", name: "River plot" },
] as Farm[];

function lane(name: string, address: Lane["address"], on: boolean, extra: Partial<Lane> = {}): Lane {
  return {
    name,
    address,
    paid: false,
    on,
    cost: 0,
    cap: {},
    today_count: 0,
    today_cost: 0,
    month_count: 0,
    month_cost: 0,
    ...extra,
  };
}

const status: NotifyStatus = {
  lanes: [
    lane("log", "none", true),
    lane("push", "push", true),
    lane("telegram", "telegram", true),
    lane("email", "email", false, { why: "set FARM_SMTP_URL and FARM_EMAIL_FROM" }),
    lane("whatsapp", "phone", false, { paid: true, cost: 14, why: "needs Meta business verification" }),
    lane("sms", "phone", true, {
      paid: true,
      cost: 25,
      cap: { day: 200, month: 3000, month_cost: 75000 },
      today_count: 3,
      month_count: 40,
      month_cost: 1000,
    }),
  ],
  routes: "urgent=fanout(push,telegram,chain(whatsapp,sms)); default=push",
  push_key: "BAAA",
  telegram_bot: "farm_bot",
};

const ravi: Recipient = {
  id: "person_1",
  name: "Ravi",
  phone: "+919876543210",
  consent: {
    sms: { opted_in: true, at: "2026-11-10T00:00:00Z", source: "settings" },
    whatsapp: { opted_in: false, at: "2026-11-10T00:00:00Z", source: "stop" },
  },
  created_at: "2026-11-10T00:00:00Z",
  updated_at: "2026-11-10T00:00:00Z",
};

function json(v: unknown, code = 200) {
  return new Response(JSON.stringify(v), { status: code, headers: { "Content-Type": "application/json" } });
}

/** A fake farm server's /api/notify: keeps people, records the calls. */
function server(people: Recipient[]) {
  const calls: { method: string; url: string; body?: unknown }[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init?: RequestInit) => {
      const method = init?.method ?? "GET";
      const body = init?.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ method, url, body });
      if (url === "/api/notify") return json(status);
      if (url === "/api/notify/recipients" && method === "GET") return json({ recipients: people });
      if (url === "/api/notify/recipients" && method === "POST") {
        const d = body as { name: string; phone?: string; consent?: string[] };
        const r: Recipient = {
          id: "person_new",
          name: d.name,
          phone: d.phone,
          consent: Object.fromEntries(
            (d.consent ?? []).map((l) => [l, { opted_in: true, at: "2026-11-10T00:00:00Z", source: "settings" }]),
          ),
          created_at: "2026-11-10T00:00:00Z",
          updated_at: "2026-11-10T00:00:00Z",
        };
        people = [...people, r];
        return json(r, 201);
      }
      const m = /^\/api\/notify\/recipients\/([^/]+)\/consent\/([^/]+)$/.exec(url);
      if (m) {
        const r = people.find((p) => p.id === m[1]);
        if (!r) return json({ error: "not found" }, 404);
        const next = {
          ...r,
          consent: {
            ...r.consent,
            [m[2] ?? ""]: {
              opted_in: (body as { opted_in: boolean }).opted_in,
              at: "2026-11-10T00:00:00Z",
              source: "settings",
            },
          },
        };
        people = people.map((p) => (p.id === r.id ? next : p));
        return json(next);
      }
      if (url.endsWith("/link")) return json({ code: "K7Q2P9", telegram_url: "https://t.me/farm_bot?start=K7Q2P9" });
      if (url.endsWith("/test"))
        return json({
          sent: true,
          receipts: [
            { lane: "push", status: "skipped", error: "notify: skipped: no address on this lane" },
            { lane: "sms", status: "sent", cost: 25 },
          ],
        });
      return json({ error: "unexpected " + method + " " + url }, 500);
    }),
  );
  return calls;
}

afterEach(() => vi.unstubAllGlobals());

describe("routes in words", () => {
  it("reads the tree as and, else", () => {
    expect(describeTree("fanout(push,telegram,chain(whatsapp,sms))")).toBe(
      "App alerts and Telegram and (WhatsApp, else SMS)",
    );
    expect(describeTree("push")).toBe("App alerts");
    expect(describeTree("chain(whatsapp,sms)")).toBe("WhatsApp, else SMS");
    expect(describeRoutes(" urgent = fanout(push, sms); digest.advice=email ;default=push")).toEqual([
      { key: "urgent", label: "Urgent alerts", tree: "App alerts and SMS" },
      { key: "digest.advice", label: "digest.advice", tree: "Email" },
      { key: "default", label: "Everything else", tree: "App alerts" },
    ]);
  });
});

describe("phone numbers", () => {
  it("makes Indian mobile numbers E.164 and leaves the rest", () => {
    expect(e164("98765 43210")).toBe("+919876543210");
    expect(e164("098765-43210")).toBe("+919876543210");
    expect(e164("919876543210")).toBe("+919876543210");
    expect(e164("+44 20 7946 0958")).toBe("+442079460958");
    expect(e164("12345")).toBe("12345"); // the server says what is wrong
  });
});

describe("push key", () => {
  it("decodes base64url without padding", () => {
    expect([...keyBytes("AQID_-8")]).toEqual([1, 2, 3, 255, 239]);
  });
});

describe("receipts in words", () => {
  it("says why a lane skipped", () => {
    expect(receiptWords({ lane: "sms", status: "skipped", error: "notify: skipped: no consent on this lane" })).toBe(
      "skipped: they have not agreed to it",
    );
    expect(receiptWords({ lane: "sms", status: "skipped", error: "notify: skipped: the lane is off" })).toBe(
      "skipped: the lane is off",
    );
    expect(
      receiptWords({ lane: "sms", status: "skipped", error: "notify: skipped: the lane is over its cost cap" }),
    ).toBe("skipped: the lane reached its spending cap");
    expect(receiptWords({ lane: "sms", status: "sent", repeat: true })).toBe("sent already");
  });
});

describe("MessagesPage", () => {
  it("shows the lanes: free or paid, on or off and why, and spend against the caps", async () => {
    server([]);
    render(<MessagesPage farms={farms} />);
    const whatsapp = await screen.findByRole("listitem", { name: "WhatsApp" });
    expect(within(whatsapp).getByText("Paid, about ₹0.14 a message")).toBeInTheDocument();
    expect(within(whatsapp).getByText("Off")).toBeInTheDocument();
    expect(within(whatsapp).getByText("needs Meta business verification")).toBeInTheDocument();
    const sms = screen.getByRole("listitem", { name: "SMS" });
    expect(
      within(sms).getByText("3 of 200 today · 40 of 3000 this month · ₹10 of ₹750 this month"),
    ).toBeInTheDocument();
    expect(within(screen.getByRole("listitem", { name: "App alerts" })).getByText("Free")).toBeInTheDocument();
    expect(screen.queryByRole("listitem", { name: "Server log" })).not.toBeInTheDocument();
    const lanes = screen
      .getAllByRole("listitem")
      .filter((li) => li.closest("section")?.textContent?.startsWith("Lanes"));
    expect(lanes.map((li) => li.getAttribute("aria-label"))).toEqual([
      "App alerts",
      "Telegram",
      "Email",
      "WhatsApp",
      "SMS",
    ]);
    expect(screen.getByText("App alerts and Telegram and (WhatsApp, else SMS)")).toBeInTheDocument();
    expect(screen.getByText(/No one gets alerts outside the app yet/)).toBeInTheDocument();
  });

  it("adds a person with an Indian number, their plots and the lanes they agreed to", async () => {
    const calls = server([]);
    const user = userEvent.setup();
    render(<MessagesPage farms={farms} />);
    await user.click(await screen.findByRole("button", { name: "Add a person" }));
    const form = screen.getByRole("form", { name: "Add a person" });
    // No address yet: nothing to agree to.
    expect(within(form).queryByRole("checkbox", { name: /SMS/ })).not.toBeInTheDocument();
    await user.type(within(form).getByLabelText("Name"), "Lakshmi");
    await user.type(within(form).getByLabelText("Mobile number"), "98765 43210");
    await user.click(within(form).getByRole("checkbox", { name: "River plot" }));
    await user.click(within(form).getByRole("checkbox", { name: /SMS/ }));
    expect(within(form).queryByRole("checkbox", { name: /Email/ })).not.toBeInTheDocument();
    await user.click(within(form).getByRole("button", { name: "Add" }));
    expect(await screen.findByRole("article", { name: "Lakshmi" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST" && c.url === "/api/notify/recipients");
    expect(post?.body).toEqual({
      name: "Lakshmi",
      phone: "+919876543210",
      lang: "te",
      farms: ["farm_2"],
      consent: ["sms"],
    });
  });

  it("turns a lane on or off for a person, only where they have an address, and shows a STOP", async () => {
    const calls = server([ravi]);
    const user = userEvent.setup();
    render(<MessagesPage farms={farms} />);
    const card = await screen.findByRole("article", { name: "Ravi" });
    expect(within(card).getByRole("checkbox", { name: "SMS for Ravi" })).toBeChecked();
    expect(within(card).getByRole("checkbox", { name: "Email for Ravi" })).toBeDisabled();
    expect(within(card).getByText("add an email")).toBeInTheDocument();
    expect(within(card).getByRole("checkbox", { name: "Telegram for Ravi" })).toBeDisabled();
    expect(within(card).getByText("stopped by Ravi")).toBeInTheDocument();
    await user.click(within(card).getByRole("checkbox", { name: "SMS for Ravi" }));
    await waitFor(() => expect(within(card).getByRole("checkbox", { name: "SMS for Ravi" })).not.toBeChecked());
    // Turned off here, not a STOP from Ravi: only WhatsApp says stopped.
    expect(within(card).getAllByText("stopped by Ravi")).toHaveLength(1);
    expect(calls.find((c) => c.method === "PUT")).toEqual({
      method: "PUT",
      url: "/api/notify/recipients/person_1/consent/sms",
      body: { opted_in: false },
    });
  });

  it("links Telegram with a start link and sends a test, saying what each lane did", async () => {
    server([ravi]);
    const user = userEvent.setup();
    render(<MessagesPage farms={farms} />);
    const card = await screen.findByRole("article", { name: "Ravi" });
    await user.click(within(card).getByRole("button", { name: "Link Telegram" }));
    expect(await within(card).findByRole("link", { name: "the farm bot on Telegram" })).toHaveAttribute(
      "href",
      "https://t.me/farm_bot?start=K7Q2P9",
    );
    expect(within(card).getByText("K7Q2P9")).toBeInTheDocument();
    await user.click(within(card).getByRole("button", { name: "Send a test" }));
    const result = await within(card).findByRole("list", { name: "Test result" });
    expect(within(result).getByText("App alerts: skipped: no address for it")).toBeInTheDocument();
    expect(within(result).getByText("SMS: sent")).toBeInTheDocument();
  });

  it("says why this browser cannot take app alerts", async () => {
    server([ravi]);
    render(<MessagesPage farms={farms} />);
    const card = await screen.findByRole("article", { name: "Ravi" });
    const push = within(card).getByRole("button", { name: "Get alerts on this phone" });
    expect(push).toBeDisabled(); // jsdom has no service workers
    expect(push).toHaveAttribute("title", expect.stringMatching(/cannot show alerts/));
  });
});
