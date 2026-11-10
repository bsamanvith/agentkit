---
id: T144
title: Farmer accounts in the farm app: sign-in lanes (Google, password, invite and one-time codes), farms with members and roles, assisted sign-up
epic: E25
status: review
depends_on: [T133, T061]
---

# T144: farmer accounts and sign-in lanes

## Context

The owner (2026-11-10): "signin also, we can have multiple lanes. keep it
pluggable and switchable. Ensure that it's properly handled", "For now,
use google signin is ok. we use auth0 or clerk? or what else do we do."
The roadmap's "Lanes" section: sign-in is a lane like the model and the
messages; Google first on agentkit's own OAuth core (no Auth0 or Clerk:
paid past a free tier, and the farmers' identities would live outside the
app); one account with several identities; farms with members and roles;
a partner (FPO field worker, dealer) signs up a farmer without a
smartphone; long sessions. Free first: Google, password and invite codes
cost nothing; SMS and WhatsApp codes wait for T143's message lanes.
Design: [ACCOUNTS.md](../apps/farm/docs/ACCOUNTS.md).

## Scope

- `pkg/auth/accounts`: the `Lane` interface in three kinds (redirect,
  secret, code), accounts with linked identities, sessions, one-time and
  sign-in codes, throttling, the HTTP handler (`/auth/*`), the store
  interface with `MemoryStore`, `sqlite` and the `accountstest` suite;
  `oidc` (OpenID Connect with PKCE and a nonce, the Google preset).
  `pkg/auth/jwt` gains `Verify` (an ID token's claims);
  `pkg/auth/login` gains `CheckPassword`.
- `apps/farm/members`: holdings (farms), members and roles, invites, join,
  assisted sign-up and recovery codes; store suite `memberstest`, `sqlite`.
- `apps/farm/farm`: plots and groups carry a holding; `WithHolding` and
  `WithReadOnly` scope the service; `Adopt` gives plots made before
  sign-in to the admin's first farm.
- `apps/farm/farmauth`: the guard on `/api` and `/mcp`, `/api/me`, the
  members routes. The assistant's conversations, alerts and runs are
  scoped to the farm.
- `apps/farm/cmd/farm`: `-signin`, `-signup`, `-public-url`,
  `-code-sender`; `FARM_GOOGLE_CLIENT_ID`/`_SECRET`, `FARM_ADMIN_EMAILS`,
  `FARM_PARTNER_EMAILS`.
- Web: the sign-in screen (lazy), the Account page (ways in, farms,
  people, invites, assisted sign-up), the session headers on every call.

## Out of scope

- Account deletion and export (DPDP), a list of sessions, other OpenID
  providers (configuration), moving the console onto these accounts.
- A live Google sign-in: needs a Google Cloud web client; the flow is
  tested against a fake provider.

## Acceptance criteria

- [x] Google sign-up then sign-in is the same account; a lane not in sign-up signs in only linked identities — test: `accounts_test.TestGoogleSignUpThenSignInIsTheSameAccount`, `TestALaneWithoutSignUpSignsInOnlyExistingAccounts`
- [x] Identities join only by linking while signed in, never by email; the last one stays — test: `TestLinkedIdentitiesSignInToOneAccount`, `TestTheLastIdentityStays`
- [x] The ID token is checked (signature, issuer, audience, azp, nonce, times, PKCE); an unverified email is not marked verified — test: `oidc_test.TestSignInGivesTheVerifiedIdentity`, `TestBadIDTokensAreRefused`, `TestTheVerifierAndCodeMustMatch`, `TestAnUnverifiedEmailIsNotMarkedVerified`, `TestGooglePreset`
- [x] The redirect flow is bound to its browser and used once; return addresses stay on the site — test: `TestRedirectFlowIsBoundToItsBrowserAndUsedOnce`, `TestReturnAddressesStayOnThisSite`
- [x] Wrong passwords for known and unknown users fail alike and are throttled — test: `TestPasswordsFailAlikeAndAreThrottled`
- [x] Sign-in and invite codes work once, expire, and one wins a race; one-time codes last 10 minutes and 5 tries — test: `TestSignInCodesWorkOnceAndExpire`, `TestASignInCodeIsRedeemedOnceUnderARace`, `TestOneTimeCodes`
- [x] Sessions last 90 days unused and 400 in all; sign-out and sign-out everywhere end them; cross-site changes are refused — test: `TestSessionsAreLongButEnd`, `TestSignOutAndSignOutEverywhere`, `TestCrossSiteRequestsAreRefused`
- [x] Stores pass one suite (memory, SQLite) — test: `accountstest.RunStore` in `TestMemoryStore`, `sqlite_test.TestStore`; `memberstest.RunStore` in `TestMemory`, members `sqlite_test.TestStore`
- [x] Each farm sees only its own plots and groups; a worker reads but cannot change, also through the assistant's tools — test: `scope_test.TestAHoldingSeesOnlyItsOwnPlotsAndGroups`, `TestAReadOnlyContextChangesNothing`, `farmauth_test.TestEachFarmSeesOnlyItsOwnPlots`, `TestAWorkerReadsButDoesNotChange`
- [x] The API and MCP are behind sign-in when it is on; automations are an admin's to change — test: `TestTheAPIIsBehindSignIn`, `TestAutomationsAreAnAdminsToChange`
- [x] The admin's first farm takes the plots made before sign-in; a stranger's first farm does not — test: `TestAdoptGivesPlotsWithoutAHolding`, `farmauth_test.TestTheFirstFarmTakesThePlotsMadeBeforeSignIn`
- [x] The assistant's and MCP's tools stay on the request's farm (`list_alerts`); a worker cannot record season work or run an automation; message settings are an admin's — test: `farmagent_test.TestListAlertsStaysOnItsFarm`, `work_test.TestAReadOnlyMemberRecordsNothing`, `TestAWorkerReadsButDoesNotChange`, `TestMessageSettingsAreAnAdminsAndUnsubscribeIsOpen`
- [x] Linking Google goes through the redirect though the session cookie is SameSite=Strict; a burst of guesses stays within the limit — test: `TestAGoogleAccountIsLinkedThroughTheRedirect`, `TestABurstOfGuessesStaysWithinTheLimit`
- [x] A partner makes recovery codes only for farmers they signed up; a new account gets one farm under a race — test: `members_test.TestOnlyAPartnerSignsAFarmerUpAndRecoversThem`, `TestANewAccountGetsOneFarmUnderARace`
- [x] Changes queued offline wait while signed out, and carry the session — test: `roadmap.test.tsx`
- [x] Join codes add with their role once and never as partner; a farm keeps an owner — test: `members_test.TestAJoinCodeAddsWithItsRoleOnce`, `TestAFarmKeepsAnOwner`
- [x] Only a partner signs a farmer up, and only the farm's partner makes a recovery code — test: `TestOnlyAPartnerSignsAFarmerUpAndRecoversThem`, `farmauth_test.TestAPartnerSignsAFarmerUpAndTheFarmerSignsIn`
- [x] Lanes come from settings; a bad list is refused — test: `main.TestSignInLanesComeFromSettings`, `TestNewRefusesBadLanes`
- [x] The web app shows the lanes, signs in, sends session headers, and lists ways in and people — test: `signin.test.tsx`
- [x] Phone and email codes go by the message lanes -code-sender lists, in order, only on lanes that can carry codes (WhatsApp's authentication template, MSG91's DLT OTP template), at once and under the caps, with the code alone for templates — test: `notify_test.TestSendCode`, `main.TestCodesGoByTheMessageLanes`, `accounts_test.TestACodeSenderGetsTheCodeAlone`, `whatsapp_test.TestCodeTemplate`, `sms_test.TestMSG91SendsCodesOnTheOTPTemplate`, `email_test.TestTeluguAndUnsubscribe`
- [ ] A live Google sign-in — needs a Google Cloud web client (free) and its redirect URI
