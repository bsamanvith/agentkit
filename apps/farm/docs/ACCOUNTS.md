# Farm accounts: sign-in lanes, farms and members

> Design note for T144 ([task](../../../tasks/T144-farm-accounts.md)). What
> farmers see: [FEATURES.md, 13](FEATURES.md#13-farmer-accounts-and-phone-sign-in).

## Why

Every plot sat in one workspace, so an alert had no one to go to and two
farmers on one server saw each other's land. Accounts say whose farm this
is and how to reach them; WhatsApp alerts, the ledger and the FPO view
build on that.

The owner (2026-11-10): several sign-in lanes, pluggable and switchable,
Google first, no Auth0 or Clerk. Both are paid past a free tier and hold
the farmers' identities outside the app; Google sign-in is a standard
OpenID Connect flow agentkit can do itself with its OAuth core.

## The parts

| Part | Package | What it is |
|---|---|---|
| Lanes | `pkg/auth/accounts` | One interface, `Lane`, in three kinds: **redirect** (`RedirectLane`: the browser goes to a provider and back, as Google), **secret** (`SecretLane`: an id and a secret checked here, as a password or an invite code), **code** (`CodeLane`: a one-time code sent to a phone or an email). Which lanes run is configuration (`-signin`), picked in one place (`apps/farm/cmd/farm/signin.go`), never a switch elsewhere |
| Google | `pkg/auth/accounts/oidc` | OpenID Connect auth code flow with PKCE and a nonce, the ID token checked against Google's keys (`jwt.Authenticator.Verify`). Any OpenID provider is the same code with another `Config` |
| Accounts | `pkg/auth/accounts` | An account has a name, app-wide roles (`admin`, `partner`) and one or more identities, each `(lane, subject)`: a Google `sub`, a username, a phone number. Store: `MemoryStore`, `sqlite`; conformance suite `accountstest.RunStore` |
| Sessions | `pkg/auth/accounts` | A 256-bit random key in a cookie (`HttpOnly`, `SameSite=Strict`, `__Host-` over TLS), stored only as its SHA-256. Ends after 90 days unused or 400 days in all, or on sign-out (here or everywhere) |
| Farms and members | `apps/farm/members` | A **holding** is the farm in the farmer's sense; it has members with a role. Plots and groups carry their holding |
| The guard | `apps/farm/farmauth` | Wraps `/api` and `/mcp`: needs a session, checks the change token, picks the holding (header `X-Farm-Holding`, `?holding=`, or the account's first farm), and puts it in the request's context |
| Scoping | `apps/farm/farm` (`WithHolding`, `WithReadOnly`) | The farm service reads the holding from the context and wraps its stores: another farm's plots are not found, a worker's writes are refused (403). The assistant's tools go through the same service, so they are scoped too |

## Rules

- **Never join by email.** Two identities become one account only when a
  signed-in person links the second. An email match alone could hand a
  farm to whoever controls a look-alike account.
- **A person keeps at least one way in**: the last identity cannot be
  unlinked.
- **Sign-up is per lane** (`-signup`, default `google`). A lane not in it
  signs in only identities already linked: a phone lane without sign-up
  sends codes only to known numbers, or to a signed-in person linking one.
- **Roles on a farm**:

  | Role | Read | Change plots, plans, alerts | Manage people |
  |---|---|---|---|
  | owner | yes | yes | yes |
  | partner | yes | yes | no |
  | family | yes | yes | no |
  | worker | yes | no | no |

  The last owner cannot leave or be demoted. A join code never makes a
  partner; the account role `partner` (granted by an admin, or by email
  with `FARM_PARTNER_EMAILS`) is what lets someone sign farmers up.
- **Assisted sign-up**: a partner makes an account and a farm for a farmer
  by name and phone, joins it as partner, and gets a 12-letter sign-in
  code to hand over (7 days, one use). When the farmer is locked out, the
  farm's partner makes a new code, which also signs the farmer out
  everywhere: only for a farmer they signed up, whose account has no
  other farm and no app-wide role, since a code signs in as the whole
  account.
- **Codes and throttling**: invite and sign-in codes are 12 characters
  from an alphabet without look-alikes (no 0, O, 1, I, L). One-time codes
  are 6 digits, 10 minutes, 5 tries. Failures are limited per network
  (an IPv6 /64 counts as one) and per id; sends per destination and per
  network. An attempt is counted before it is checked, so a burst of
  parallel guesses cannot pass the limit.
- **Change token (CSRF)**: every change sends `X-CSRF-Token` from
  `/api/me` and must come from the same origin.
- **Automations stay app-wide**: an admin changes them; members run them
  only on their own plots, and see only their own alerts.
- **Message settings are an admin's** (`/api/notify/*`, T143): the people
  who get messages, with their phone numbers, belong to the app until they
  become accounts. The email unsubscribe link stays open: it is signed and
  opened from a mail app.
- **Work records follow the role**: a worker sees a plot's season roadmap
  (T141) but cannot tick tasks off.
- **Plots before sign-in**: the first farm of an admin (`FARM_ADMIN_EMAILS`)
  takes every plot and group that has no farm yet, so the farmer who used
  the app alone, and turns sign-in on naming themself admin, keeps their
  land; a stranger who signs up first does not get it.
- **Running an automation by hand** needs a role that can change things
  (a run can call the model and send paid messages).
- **Behind a proxy**: list the proxies in `FARM_TRUSTED_PROXIES` (addresses
  or prefixes), so cookies are secure, the same-origin check sees https,
  and attempts are counted per farmer, not per proxy.

## Codes by the message lanes

The phone and email lanes send their one-time codes by the message lanes
of [MESSAGES.md](MESSAGES.md) (`apps/farm/cmd/farm/signin.go`, `laneCodes`):

- `-code-sender` lists the lanes, tried in order as a chain: the first
  that takes the code ends it. Only lanes that are on, reach the address
  (a phone or an email) and can carry a code are used; a phone or email
  sign-in lane with none fails at start with what to set.
- A code goes at once (`notify.SendCode`): not through the outbox, the
  routes or quiet hours, as a `code` message, urgent, worth sending for 10
  minutes. Asking for it is the consent, so the person needs no record in
  the message settings; the paid lanes' caps and delivery records still
  apply, under a recipient named by a hash of the address.
- The code also travels alone (`accounts.CodeSender`), for the templates
  that carry only it: WhatsApp's authentication template (the code in the
  body and the copy-code button) and a DLT OTP template on MSG91. Without
  those templates the lane is skipped for codes. Twilio and email send the
  message as it is; email adds no unsubscribe link to a code.
- Sending a code is throttled as before (three to one phone or email,
  and ten from one network address, in 15 minutes), so one abuser cannot
  spend the paid lanes' caps.

## Configuration

| Flag | Variable | Meaning |
|---|---|---|
| `-signin` | `FARM_SIGNIN` | Lanes in the order shown: `google`, `password`, `invite`, `phone`, `email`; empty is no sign-in (the app as before) |
| `-signup` | `FARM_SIGNUP` | Lanes that make new accounts (default `google`) |
| `-public-url` | `FARM_PUBLIC_URL` | The address browsers use, for Google's redirect `<url>/auth/google/callback` |
| `-code-sender` | `FARM_CODE_SENDER` | The message lanes codes go by, tried in order (default `sms,whatsapp,email`); `log` writes them to the server log, for trying it out |
| | `FARM_MSG91_CODE_TEMPLATE` | MSG91's template for a DLT OTP template (the code in `var1`); without it MSG91 sends no codes (Twilio needs none) |
| | `FARM_WHATSAPP_CODE_TEMPLATE` | An approved WhatsApp **authentication** template (copy-code button); WhatsApp allows codes in no other kind, so without it WhatsApp sends no codes |
| | `FARM_GOOGLE_CLIENT_ID`, `FARM_GOOGLE_CLIENT_SECRET` | A web client from the Google Cloud console (free) |
| | `FARM_ADMIN_EMAILS`, `FARM_PARTNER_EMAILS` | Verified emails that get the `admin` or `partner` role at sign-in |
| | `FARM_TRUSTED_PROXIES` | The TLS-terminating proxies in front of the app (addresses or prefixes, comma separated) |

## What comes next

- MCP clients signing in (today the API behind sign-in is for browsers).
- Account deletion and export (DPDP Act), and a list of the account's
  sessions.
- More OpenID providers are configuration on `oidc.Config`.
- The console (`pkg/auth/login`) can move onto the same accounts later; the
  password lane already uses its hashing.
