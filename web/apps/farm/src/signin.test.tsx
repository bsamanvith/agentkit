import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SignIn } from "./signin";
import { Gate } from "./gate";
import { AccountPage } from "./account";
import { session, sessionHeaders } from "./session";
import type { Lane, Me } from "./account-api";

const lanes: Lane[] = [
  { name: "google", kind: "redirect", sign_up: true },
  { name: "password", kind: "secret", sign_up: false },
  { name: "invite", kind: "secret", sign_up: false },
  { name: "phone", kind: "code", sign_up: true },
];

function json(body: unknown, status = 200) {
  return Promise.resolve(
    new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }),
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
  session.csrf = session.holding = "";
  window.history.replaceState(null, "", "/");
});

describe("SignIn", () => {
  it("offers each lane and signs in with a partner's code", async () => {
    const calls: { url: string; body: unknown }[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string, init?: RequestInit) => {
        calls.push({ url, body: init?.body ? JSON.parse(String(init.body)) : undefined });
        return json({ account: { id: "acc_1" }, created: false });
      }),
    );
    const done = vi.fn();
    render(<SignIn lanes={lanes} onSignedIn={done} />);
    expect(screen.getByRole("button", { name: "Continue with Google" })).toBeInTheDocument();
    expect(screen.getByRole("form", { name: "Username and password" })).toBeInTheDocument();
    expect(
      screen.getByText(/New here\? Signing in with Google or Phone number makes your account/),
    ).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Sign-in code"), "k7qm 2xdp 9rta");
    await userEvent.click(screen.getByRole("button", { name: /Sign in with the code/ }));
    await waitFor(() => expect(done).toHaveBeenCalled());
    expect(calls).toEqual([{ url: "/auth/invite/check", body: { id: "", secret: "k7qm 2xdp 9rta" } }]);
  });

  it("sends a code to a phone, then signs in with it", async () => {
    const urls: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) => {
        urls.push(url);
        return url.endsWith("/send") ? json({ to: "+919876543210" }, 202) : json({ account: { id: "a" } });
      }),
    );
    const done = vi.fn();
    render(<SignIn lanes={lanes} onSignedIn={done} />);
    const phone = within(screen.getByRole("form", { name: "Phone number" }));
    await userEvent.type(phone.getByRole("textbox", { name: "Phone number" }), "98765 43210");
    await userEvent.click(phone.getByRole("button", { name: "Send me a code" }));
    await userEvent.type(await phone.findByLabelText("The code you got"), "123456");
    await userEvent.click(phone.getByRole("button", { name: "Sign in" }));
    await waitFor(() => expect(done).toHaveBeenCalled());
    expect(urls).toEqual(["/auth/phone/send", "/auth/phone/verify"]);
  });

  it("says why a Google sign-in came back failed, and shows the server's error", async () => {
    window.history.replaceState(null, "", "/?signin=failed&reason=no_account");
    vi.stubGlobal(
      "fetch",
      vi.fn(() => json({ error: "wrong or expired: check it and try again", code: "bad_secret" }, 401)),
    );
    render(<SignIn lanes={lanes} onSignedIn={() => {}} />);
    expect(screen.getByRole("alert")).toHaveTextContent(/No account signs in this way yet/);
    await userEvent.type(screen.getByLabelText("Sign-in code"), "nope");
    await userEvent.click(screen.getByRole("button", { name: /Sign in with the code/ }));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("wrong or expired"));
  });
});

describe("session headers", () => {
  it("carry the farm, and the CSRF token on changes only", () => {
    session.csrf = "tok";
    session.holding = "hold_1";
    expect(sessionHeaders("GET")).toEqual({ "X-Farm-Holding": "hold_1" });
    expect(sessionHeaders("POST")).toEqual({ "X-Farm-Holding": "hold_1", "X-CSRF-Token": "tok" });
    session.csrf = session.holding = "";
    expect(sessionHeaders("DELETE")).toEqual({});
  });
});

describe("AccountPage", () => {
  const me: Me = {
    account: { id: "acc_p", name: "Field Partner", roles: ["partner"] },
    farms: [{ holding: { id: "hold_1", name: "Lakshmi's farm" }, role: "owner" }],
    holding: { id: "hold_1", name: "Lakshmi's farm" },
    role: "owner",
    partner: true,
    admin: false,
    csrf: "tok",
  };

  it("lists ways in and people, and signs a farmer up with a code to give them", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) => {
        if (url === "/auth/me")
          return json({
            identities: [{ lane: "google", subject: "1", email: "p@gmail.com", created_at: "", last_used_at: "" }],
          });
        if (url.endsWith("/members"))
          return json({
            members: [{ holding: "hold_1", account: "acc_p", role: "owner", name: "Field Partner", added_at: "" }],
          });
        if (url === "/api/assist")
          return json({
            account: { id: "acc_f", name: "Venkatamma" },
            holding: { id: "hold_2", name: "Venkatamma's farm" },
            code: "K7QM-2XDP-9RTA",
            expires: "",
          });
        return json({});
      }),
    );
    render(<AccountPage me={me} lanes={lanes} onChanged={() => {}} onSwitch={() => {}} />);
    expect(await screen.findByText("p@gmail.com")).toBeInTheDocument();
    // The only way in cannot be removed.
    expect(screen.queryByRole("button", { name: "Remove Google" })).not.toBeInTheDocument();
    expect(await screen.findByText("(you)")).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Farmer's name"), "Venkatamma");
    await userEvent.click(screen.getByRole("button", { name: /Make the account/ }));
    expect(await screen.findByText("K7QM-2XDP-9RTA")).toBeInTheDocument();
  });
});

describe("Gate", () => {
  it("shows sign-in when nobody is signed in, and drops a farm that is no longer theirs", async () => {
    const asked: (string | undefined)[] = [];
    try {
      localStorage.setItem("farm.holding", "hold_old");
    } catch {
      // no storage: the test still checks the sign-in screen
    }
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string, init?: RequestInit) => {
        if (url === "/auth/lanes") return json({ lanes });
        asked.push((init?.headers as Record<string, string> | undefined)?.["X-Farm-Holding"]);
        return asked.length === 1 ? json({ error: "not found" }, 404) : json({ error: "sign in" }, 401);
      }),
    );
    render(<Gate />);
    expect(await screen.findByRole("button", { name: /Google/ })).toBeInTheDocument();
    expect(asked).toEqual(["hold_old", undefined]);
    expect(session.csrf).toBe("");
    localStorage.removeItem("farm.holding");
  });
});
