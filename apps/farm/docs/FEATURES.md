# Farm: what farmers can do, and what comes next

> The product list, kept up to date. Last updated 2026-11-10.
> Every PR that adds, changes or removes something a farmer can see
> updates this file (the `farm-features` skill says how). Why the app
> exists and the farmer's year: [PRODUCT.md](PRODUCT.md). Epic:
> [E25](../../../tasks/E25-farm-app.md).

Status words: **live** (merged on main, a farmer can use it), **building**
(a branch or a thread is on it now), **planned** (decided, not started),
**idea** (not decided).

## The list

| # | Feature | Status | Farmer's year | Since |
|---|---|---|---|---|
| 1 | [My plots](#1-my-plots) | live | land | T133 |
| 2 | [The land and its surroundings](#2-the-land-and-its-surroundings) | live | land | T135, T138 |
| 3 | [Farm layout](#3-farm-layout) | live | land | T135 |
| 4 | [What to plant and when](#4-what-to-plant-and-when) | live | plan | T134, T137 |
| 5 | [Sun and seasons](#5-sun-and-seasons) | live | plan | T137 |
| 6 | [Season plan for a plot](#6-season-plan-for-a-plot) | live | plan | T134 |
| 7 | [Plot groups and how plants are spread](#7-plot-groups-and-how-plants-are-spread) | live | plan | T136 |
| 8 | [The farm assistant](#8-the-farm-assistant) | live | all | T133 onward, T142 |
| 9 | [Web research with the farmer's own key](#9-web-research-with-the-farmers-own-key) | live | plan, sell | T135 |
| 10 | [Farm tools for other agents (MCP)](#10-farm-tools-for-other-agents-mcp) | live | all | T135 |
| 11 | [Weather alerts and weekly crop work](#11-weather-alerts-and-weekly-crop-work) | live | grow | T139 |
| 12 | [Crop prices: estimates and mandi prices](#12-crop-prices-estimates-and-mandi-prices) | live | sell | T140 |
| 13 | [Farmer accounts and phone sign-in](#13-farmer-accounts-and-phone-sign-in) | live | all | T144 |
| 14 | [Alerts on the phone: push, Telegram, email, WhatsApp, SMS](#14-alerts-on-the-phone-push-telegram-email-whatsapp-sms) | live | grow | T143 |
| 15 | [Crop doctor](#15-crop-doctor) | planned | grow | |
| 16 | [Ledger by voice](#16-ledger-by-voice) | planned | money | |
| 17 | [Offline and voice in the field](#17-offline-and-voice-in-the-field) | planned | all | |
| 18 | [Field watch from satellites](#18-field-watch-from-satellites) | idea | grow | |
| 19 | [Soil Health Card and land papers from a photo](#19-soil-health-card-and-land-papers-from-a-photo) | idea | land | |
| 20 | [Schemes, insurance and paperwork](#20-schemes-insurance-and-paperwork) | idea | plan, money | |
| 21 | [For FPOs, dealers and lenders](#21-for-fpos-dealers-and-lenders) | idea | all | |
| 22 | [Season roadmap](#22-season-roadmap) | live | grow | T141 |

## Live

### 1. My plots

**The farmer** adds a plot: a name, how big they think it is (acres,
guntas, cents, hectares or m²), and its boundary, by tapping the corners
on a satellite map, by walking to each corner and tapping "add my
position", or by pasting coordinates. To draw, they type their village
or town and the map flies there; zoomed out, a tap zooms in rather than
dropping a corner far from where they meant.

**The app** checks it before saving: a valid shape, the true area on the
ground in every unit, whether it matches the size the farmer said (a big
miss usually means a wrong corner or the wrong unit), slivers and GPS
jumps, poor GPS accuracy while walking, where it is (village, district,
state) and overlaps with the farmer's other plots. The result is
*verified*, *needs review* (warnings the farmer can accept) or *invalid*.

**Behind it:** `apps/farm/geo` (geodesic area, shape checks),
`apps/farm/farm` (verifier, store), `apps/farm/nominatim` (place). Web:
`editor.tsx`, `map.tsx`, `report.tsx`. Details:
[PRODUCT.md, first feature](PRODUCT.md#first-feature-my-land).

### 2. The land and its surroundings

**The farmer** opens *Land and layout* on a plot and sees its shape, which
way each side faces, how it slopes and which way water runs off, and
what is near: roads, canals, rivers, wells, buildings, the village, power
lines, with distances and directions. An animated picture (the *land
view*) draws the plot and its surroundings, and the crop zones growing
through the season.

**Behind it:** map features from OpenStreetMap, ground heights from
Copernicus (Open-Meteo) or SRTM (Open Topo Data) when the first cannot
answer; `apps/farm/land`, `osm`, `elevation`, `surveyor`. Web:
`land.tsx`, `landpicture.tsx`. Details: [LAND.md](LAND.md).

### 3. Farm layout

**The farmer** asks for a layout and gets a gate on the side nearest the
road, a path, a shed, a farm pond at the low corner and border trees, each
with its reason. They can move things and the app checks the result
(the pond in the low corner, the path reaching the gate, nothing outside
the plot).

**Behind it:** `land.Suggest`, `land.CheckDesign`; tools `suggest_design`,
`check_design`, `save_design`; skill `shape-farm`. Details:
[LAND.md](LAND.md).

### 4. What to plant and when

**The farmer** tells the app the soil (picked from a list of eleven soils
with the signs they can see: colour, cracks, stickiness), the water
sources (rain, canal, borewell, drip, ...) and last season's crop. The app
scores 39 crops for the plot, each with its next good sowing window, a
score from 0 to 100, the reasons and the risks, and how much water rain
will not cover. Crops from similar climates abroad (dragon fruit,
avocado, quinoa) are scored too and marked as *climate analogs*.

**Behind it:** rules, not the model: `apps/farm/agro` (soil and crop
libraries in `agro/data`, the recommender), climate normals from NASA
POWER (`apps/farm/nasapower`). Tools `recommend_crops`, `compare_crops`;
skills `plan-season`, `research-crops`. The rule values are starter values
that need an agronomist's review. Details:
[CROP-PLANNING.md](CROP-PLANNING.md).

### 5. Sun and seasons

**The farmer** sees, month by month for their plot, how much sunlight it
gets, the day length, where the noon sun is, what the slope does to the
light, and a crop calendar: when each crop would be sown and harvested
through the year, kharif, rabi and summer.

**Behind it:** `apps/farm/sun` (FAO-56 astronomy, light on a slope), NASA
POWER sunlight; tool `get_sun`. Web: *Sun and seasons* tab
(`seasons.tsx`). Details: [SUN.md](SUN.md).

### 6. Season plan for a plot

**The farmer** picks one crop or a mix ("60% cotton, 40% green gram",
"4 rows soybean : 1 row maize"), and the app cuts the plot into strips to
scale, with row and plant spacing, plant counts and seed needed. The plan
check warns before it costs a season: sowing outside the window, a
thirsty crop on rain alone, a crop that rots in waterlogged clay, spacing
out of range.

**Behind it:** `agro` (strips, plan checker), `apps/farm/planner`; tools
`suggest_layout`, `check_plan`, `save_plan`; skill `check-plan`. Web:
*Season plan* tab (`planning.tsx`, `plan.ts`).

### 7. Plot groups and how plants are spread

**The farmer** keeps plots in groups (the home farm, the fields by the
river) and, per group, chooses plant groups and where they go: blocks, a
border band, or tree rows with crops between (alley cropping). The app
draws it to scale and says what it does: shade on the shorter crops
through the year, edges, whether trees fit, water, neighbours, wind.

**Behind it:** `apps/farm/planting`; tools `list_groups`, `save_group`,
`distribute_plants`, `save_distribution`; skill `distribute-plants`. Web:
groups sidebar and group page (`groups.tsx`, `grouppicture.tsx`).
Details: [GROUPS.md](GROUPS.md).

### 8. The farm assistant

**The farmer** asks in their own language and script (Telugu, Hindi,
Kannada, Marathi, English) on a phone: "what should I sow on the east
plot after Dasara", "how far is the canal", "convert 2 acres 10 guntas to
cents". The assistant answers short and plain, and every number in the
answer comes from a tool, so it is the same however the question is
asked.

**Behind it:** the agentkit harness (`apps/farm/farmagent`) on the model
lane (`apps/farm/models`, T142): a list of models tried in order, by
default Claude then Gemini, where OpenAI and OpenRouter models can be
listed too. A model joins the lane only after it passes `make farm-eval`;
when one is down, rate limited or its key is refused, the next answers the
same question, so the farmer sees no gap. With the farm, planning and map tools,
seven skills, and a handbook on farming in India it can search
(`apps/farm/handbook`, keyword or meaning search). `make farm-eval` runs
its scenarios on a real model. Web: `assistant.tsx`.

### 9. Web research with the farmer's own key

**The farmer** connects their own Exa or Parallel key once in settings;
the assistant can then search the web and read pages (a product, a seed
variety, a scheme) and cite them. Keys are sealed in a vault, never
shared between farmers.

**Behind it:** `apps/farm/research`, `connectors/exa`,
`connectors/parallel`, `pkg/credentials`; skill `research-products`. Web:
`research.tsx`.

### 10. Farm tools for other agents (MCP)

The farm, planning and map tools are served over MCP at `/mcp/<toolset>`,
so another agent (an FPO's, an agronomist's) can use the same numbers.
Not farmer-facing; it is how partners plug in.

### 11. Weather alerts and weekly crop work

**The farmer** opens *Weather and care* on a plot and sees the week's
forecast, the advice for their crops (or "Nothing to do because of the
weather this week."), the week's work with each crop's stage, and the
season's calendar. The bell in the header is the inbox across plots; an
alert comes **only when there is something to do**: heavy rain (clear
drains, hold irrigation), rain at harvest, a wet spell before sowing,
strong wind, heat or cold for the crop, a dry week on rainfed land,
fertiliser due on a wet day, weather that brings blight and mildew. Each
says what to do, by when and why.

**The app** checks every plot each Monday morning, watches for heavy rain
every six hours, and checks a plot when its plan is saved; the same alert
is not sent twice in a week. The farmer (or an adviser) can turn
automations on and off, run one now, see recent runs, and add their own:
a schedule, an event or a weather threshold, running a fixed rule or an
assistant skill.

**Behind it:** `apps/farm/weather` (Open-Meteo, then MET Norway),
`calendar` (crop stages and care tasks), `advise` (the rules, IMD
thresholds, starter values for an agronomist to review), `automation`
(the engine, runs, the inbox), `watch` (the built-in automations); tools
`get_week`, `crop_calendar`, `list_alerts`, `notify_farmer`; skills
`weather-check`, `draw-crop-plan`, `manage-crop`. From the inbox, alerts go
out by the message lanes of
[14](#14-alerts-on-the-phone-push-telegram-email-whatsapp-sms). Details:
[AUTOMATIONS.md](AUTOMATIONS.md).

### 12. Crop prices: estimates and mandi prices

**The farmer** opens a plot's *Prices* tab and picks a crop (the plan's
crops come first): today's mandi prices in the plot's district and the
rest of the state, the last year month by month, and what the crop may
fetch in the month they plan to sell, with the minimum support price.
With their expected yield in quintals an acre it says what the plot's
harvest may bring. Or they ask the assistant "what is kandulu selling
for, should I hold till March?".

**The app** asks the price providers in order (Agmarknet's all-India
daily prices on data.gov.in, then downloaded price files), saves every
price it gets so the history grows day by day, and answers from the
saved prices when every provider is down, saying how old they are. A
district with no prices widens to the state and says so. The estimate is
arithmetic on that history (recent level, the crop's usual swing by
month, the spread, how far ahead), a low to high range with a confidence
and its reasons, never a model's guess. Prices are rupees a quintal.

**Behind it:** `apps/farm/feeds` (the provider layer: chain, pause on a
rate limit, cache, pick by name), `apps/farm/prices` (providers
`DataGov` and `Files`, history, `pricesqlite`, MSP, estimate); tools
`mandi_prices`, `price_estimate`; skill `sell-or-hold`; routes
`/api/prices*`. Web: `prices.tsx`. Design: [PRICES.md](PRICES.md).

### 22. Season roadmap

**The farmer** opens *Season work* on a plot with a saved plan and sees what
is due in the next two weeks and what is past its date, or the whole
season, by phase (prepare, sowing, growing, harvest). They tap **Done**
(**Sown** for the sowing) with the day it happened, how many workers and
what it cost, **Skip** with why, or **Undo**, and add tasks of their own
("repair the motor on Friday"). It works without a connection: changes
wait on the phone and are sent when it is back. Or they tell the
assistant "weeded the east plot yesterday, 4 women, 1,400 rupees" and it
records that once they agree.

**The app** makes the tasks from the plan's crop calendar every time, and
keeps only what happened. The real sowing day moves every later task;
the app never closes a task by itself, it asks ("Past its date: did you
do it?"); when two people tick the same task the first one counts; a
change sent twice counts once. The weekly work notice leaves out what is
done and includes the farmer's own tasks.

**Behind it:** `apps/farm/work` (roadmap, events, `Store` with
`worktest.RunStore`, `work/sqlite`), calendar rule keys in
`apps/farm/calendar`; routes `/api/farms/{id}/roadmap*`; tools
`get_roadmap`, `record_work`. Web: `roadmap.tsx`, `roadmap-data.ts` (the
offline queue). Design: [ROADMAP.md](ROADMAP.md).

### 14. Alerts on the phone: push, Telegram, email, WhatsApp, SMS

**The farmer** gets the plot's alerts where they already look: a
notification from the installed app, a Telegram message, an email, and,
once the business is registered, WhatsApp with SMS as the fallback. On
the Messages page (from Alerts) they add the people who should hear (themselves, a son, the FPO
field worker), each with the plots they care about, tick the ways they
agree to be reached, link Telegram with one tap, and send a test. STOP on
WhatsApp, SMS or Telegram (or ఆపు) stops that way at once; nothing comes
between 9 pm and 6 am unless it is urgent.

**The app** sends each notice through a tree of lanes the owner sets,
for example urgent alerts to push and Telegram and to WhatsApp, falling
back to SMS; warnings to push and Telegram; the rest by push. Each lane
sends a message at most once, tries again when a provider is down for a
while, records delivered and read, and stops at its daily and monthly
cost cap so a paid lane falls back to a free one. A lane without its keys
is skipped, so adding keys later switches it on.

**Behind it:** `apps/farm/notify` (lanes, guard, fan-out, chain, router,
the outbox), lanes `webpush`, `telegram`, `email`, `whatsapp`, `sms`
(MSG91, Twilio), `notify/sqlite`; `watch.Messages`; routes
`/api/notify/*`, webhooks `/hooks/*`; in the web app the Messages page
(`messages.tsx`, from Alerts, "Alerts on phones") and the service worker
(`public/sw.js`), which shows a push and opens its plot (`/?farm=`).
**Still to come:** a test on a real phone with each lane's keys (push
needs the app on https); WhatsApp and SMS need Meta's business
verification and DLT registration first; escalating along a chain when a
message is sent but not delivered. Design: [MESSAGES.md](MESSAGES.md).

### 13. Farmer accounts and phone sign-in

**The farmer** signs in with Google (free), or with a username and
password, or with a code a partner gave them, or with a one-time code sent
to their phone or email. They can add more ways later (Google and a phone,
say) on the *Account* page, and stay signed in on their phone for months.
Each farmer sees only their own farm: its plots, groups, plans, alerts
and assistant chats. The owner of a farm invites family or a worker with a
join code and picks their role: family and partners can change things, a
worker can only look. A partner (an FPO field worker or a dealer) can sign
up a farmer who has no smartphone or email, by name and phone number, and
hand them a sign-in code on paper; when the farmer loses their way in, the
partner makes them a new code.

**The app** keeps one account per person with as many sign-in ways as
they link, never joining two accounts because their emails match. A
session is a long random key kept only as a hash, in a cookie the page's
scripts cannot read, with a separate token on every change so another
site cannot act for the farmer. Wrong passwords and codes are slowed per
phone and per network; one-time codes last 10 minutes and five tries. The
admin's first farm takes the plots made before sign-in was turned on. Without sign-in turned on (`-signin`), the app works as before, for
one farmer on their own machine.

**Behind it:** `pkg/auth/accounts` (lanes, accounts, sessions, codes;
`oidc` for Google, `sqlite` store), `apps/farm/members` (farms, members,
roles, invites, assisted sign-up), `apps/farm/farmauth` (the routes and
the guard on `/api` and `/mcp`), `farm.WithHolding` (each farm sees only
its plots); flags `-signin`, `-signup`, `-public-url`, `-code-sender`.
Web: `signin.tsx`, `account.tsx`, `session.ts`. Design:
[ACCOUNTS.md](ACCOUNTS.md). The phone and email codes go by the message
lanes of [14](#14-alerts-on-the-phone-push-telegram-email-whatsapp-sms)
that `-code-sender` lists (default SMS, then WhatsApp, then email), at
once and under the paid lanes' caps; WhatsApp needs an authentication
template for codes and MSG91 a DLT OTP template. With no lane keys yet,
`-code-sender log` writes the codes to the server's log, for trying it out.

## Building now

Nothing right now.

## Planned

### 15. Crop doctor

A photo of a sick leaf or plant, the likely problem (pest, disease,
deficiency), what to do now in the farmer's language, and when to ask a
person. Uses the model's image input with the plot's crop and stage and
the weather; answers from the handbook's package of practices, and says
"not sure, show an agronomist" rather than guess.

### 16. Ledger by voice

"Spent 2,000 on urea for the east plot" becomes an entry; yield and sale
price at harvest; profit per plot and per season, with the price
estimates from [12](#12-crop-prices-estimates-and-mandi-prices) before
harvest. The numbers are Go, the model only turns speech into entries
the farmer confirms.

### 17. Offline and voice in the field

The app installs on the phone (PWA), opens without data, lets the farmer
walk a boundary or add a ledger entry offline and syncs later; voice in
and out in the local language for farmers who prefer not to type.

## Ideas

### 18. Field watch from satellites

Weekly greenness (Sentinel-2 NDVI, 10 m, free) per plot; an alert when a
plot browns faster than its neighbours. Also a cross-check that the plot
is cropland (ESA WorldCover).

### 19. Soil Health Card and land papers from a photo

The farmer photographs the Soil Health Card and the app fills the plot's
soil (pH, N, P, K, organic carbon) instead of guessing from colour; the
pattadar passbook or RoR gives the survey number and recorded area to
check against the drawn boundary.

### 20. Schemes, insurance and paperwork

Which schemes a farmer qualifies for (PM-KISAN, PMFBY, Rythu Bharosa,
Raitha Siri and other state schemes), the documents and deadlines; an
insurance claim drafted with geo-tagged photos, submitted only with the
farmer's approval.

### 21. For FPOs, dealers and lenders

Many farmers' verified plots in one view: crop area and expected harvest
across members, input demand for a dealer, a lender's check against a
verified boundary. Needs accounts and consent.

## What is most needed next

Ranked 2026-11-10, after weather alerts (11), prices (12), accounts (13) and the message lanes (14), all live:

1. **Alerts on the phone (14), live; keys next.** Farmers will not open
   an app every day to look for an alert; the lanes and the Messages page
   are in. Next: a Telegram bot and an SMTP account (free), then WhatsApp
   and SMS once registered. The same lanes carry the phone and email
   sign-in codes of 13 (wired; an SMTP account gives free email codes).
2. **Crop doctor (15).** The question farmers ask most in season.
3. **Ledger by voice (16)**, with prices (12) giving profit per plot.
4. **Offline and voice (17)** before field use at scale.
5. **Soil Health Card from a photo (19)**: better soil means better
   recommendations; small once the model reads images for 15.

Also needed, not a feature: an agronomist's review of the crop rule
values, and real farmers trying the app in one district.

## Data providers

Farm data comes from a fragmented, changing set of sources, so every kind
of data is behind an interface with more than one provider, and the
provider is picked in one place (the `farm` binary's flags), never by a
switch inside a feature. A provider can fetch by API, by downloading a
file, or by reading a page; a fallback provider answers when the first
cannot; every number keeps the source it came from; one answer never
mixes two providers' numbers of the same kind.

**Free first** (owner, 2026-11-08). Pick free sources and free tiers; a
paid provider needs a free one beside it, or a line here on why there is
none. Anything not free, or free only for non-commercial use, is marked
in the *Cost* column and named in the PR that adds it.

| Data | Interface | Providers today | Fallback | Cost |
|---|---|---|---|---|
| Place of a point | `farm.Locator` | Nominatim (OpenStreetMap) | none (off) | Free; 1 request a second, attribution |
| Climate normals and sunlight | `agro.ClimateSource` | NASA POWER | none (off: no recommendations) | Free, no key |
| Map features | `osm.Source` | Overpass | OSM API (small boxes) | Free, fair use, attribution (ODbL) |
| Ground heights | `elevation.Source` | Open-Meteo (Copernicus GLO-90) | Open Topo Data (SRTM 30 m) | Open-Meteo: **free for non-commercial use only**, paid plan for commercial; Open Topo Data public API: free, 1,000 a day |
| Web research | `research.Providers` | Exa, Parallel (the farmer's key) | the other one, if the farmer connected it | **Paid per call** after their free credits; the farmer's own key, so it costs us nothing |
| Model | `models.Lane` | the first model of `-models` that answers: Claude (default) | the rest of the list, in order: Gemini (default), OpenAI, OpenRouter; only models that passed `make farm-eval` | Claude and OpenAI: **paid** per token; Gemini: free tier (daily limits, prompts may be used to improve Google's models); OpenRouter: **paid** per token, except `:free` models (daily limits, often slow, providers may log prompts) |
| Handbook search | `pkg/knowledge` | Gemini embeddings plus keywords | keywords only | Gemini free tier, 100 texts a minute; keywords free |
| Weather forecast | `weather.Source` | Open-Meteo, then MET Norway (`-weather`, tried in order) | the next in the list | Open-Meteo: **free for non-commercial use only** (under 10,000 a day), paid plan or self-hosting for commercial; MET Norway: free, CC BY 4.0, commercial use allowed with credit |
| Mandi prices | `prices.Source` (on `feeds.Provider`) | Agmarknet on data.gov.in (`datagov`, all India, daily; needs `FARM_DATAGOV_KEY`), price files (`files`: Agmarknet, data.gov.in or CEDA CSVs in `FARM_PRICE_FILES`) | the next provider, then every price saved before | data.gov.in: **free**, key on registration; files: free; CEDA: free with registration, check its terms before commercial use |
| Message lanes | `notify.Lane` | web push, Telegram, email (SMTP), WhatsApp (Meta Cloud API), SMS (MSG91 or Twilio), each on when its keys are set; `-notify-routes` composes them | the next lane in a chain; at a lane's cost cap, the next | Web push, Telegram: free; email: free tiers of most SMTP providers (flag the one chosen); WhatsApp: **paid** per template message, needs Meta business verification; SMS: **paid** per message, needs DLT registration |
| Sign-in | `accounts.Lane` (redirect, secret or code lanes, `-signin`) | Google (`google`, OpenID Connect; needs `FARM_GOOGLE_CLIENT_ID` and `_SECRET`), password, invite codes, phone and email one-time codes | the other lanes turned on: a farmer can link several | Google, password, invite: **free**; phone and email codes: by the message lanes (14), at their cost: email free tiers, SMS and WhatsApp **paid** per code |
| News and knowledge | to plan, on `feeds.Provider` as prices are (weather can move onto it) | | | |

Paid things in the plans above, and the free way to start:

| Planned | Paid option | Free way first |
|---|---|---|
| Phone sign-in codes (13) | SMS or WhatsApp one-time codes (per message) | Google, a password, or a partner's sign-in code (all live, free) |
| Alerts on the phone (14) | WhatsApp Cloud API (per template message), SMS (per message), both capped by `-notify-caps` | Web push from the installed app, a Telegram bot, email on a free tier |
| Crop doctor (15) | Claude with images | Gemini's free tier reads images |
| Field watch (18) | Commercial imagery | Sentinel-2 (Copernicus, free) |

When a provider is added, changed or dropped, update this table in the
same PR.
