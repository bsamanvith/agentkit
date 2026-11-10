# Farm

An app for farmers, built on agentkit: plots with verified boundaries
first, then crops, money and agents that watch the fields. What farmers
can do today, how each feature works and what is next:
[docs/FEATURES.md](docs/FEATURES.md) (kept current by the `farm-features`
skill). What and why: [docs/PRODUCT.md](docs/PRODUCT.md); crop planning: [docs/CROP-PLANNING.md](docs/CROP-PLANNING.md); the land
(survey, layout, map tools, web research): [docs/LAND.md](docs/LAND.md);
plot groups and how plants are spread over them: [docs/GROUPS.md](docs/GROUPS.md);
the sun, the slope and the seasons: [docs/SUN.md](docs/SUN.md);
weather, care advice and automations: [docs/AUTOMATIONS.md](docs/AUTOMATIONS.md);
crop prices (mandi prices, estimates, the provider layer): [docs/PRICES.md](docs/PRICES.md);
the plan: [E25](../../tasks/E25-farm-app.md).

```sh
make farm                     # http://127.0.0.1:8090, data in ./farm.db
make farm-web                 # rebuild the web client after changing web/apps/farm
pnpm --dir web --filter @agentkit/farm dev   # the client with hot reload, against make farm
```

The assistant runs on the first model of `-models` (`$FARM_MODELS`) that
answers, by default `anthropic,gemini`: Claude with `ANTHROPIC_API_KEY` (or
`ANTHROPIC_KEY`), then Gemini with `GEMINI_API_KEY` (or `GEMINI_KEY`). OpenAI
(`openai/<id>`, `OPENAI_API_KEY`) and OpenRouter
(`openrouter/<vendor>/<model>`, `OPENROUTER_API_KEY`) can be listed too. A
model is used only after it passes `make farm-eval` (recorded in
[models/evaluated.json](models/evaluated.json)); when one fails (overloaded,
rate limited, a bad key) the next takes the request. `-models off` turns
the assistant off. Plots are located with OpenStreetMap's Nominatim
(`-geocoder off` to skip it); `-country IN` is where plots are expected.
The climate at a plot comes from NASA POWER (`-climate off` to skip it:
then there are no recommendations, and plan checks skip sowing and water).
The land around a plot comes from OpenStreetMap (`-overpass`, `-osm-api`)
and its slope from Open-Meteo (`-elevation`), or Open Topo Data when
Open-Meteo cannot answer (`-elevation-backup`), heights kept in
`<db>-heights.json`; each can be `off`. The
handbook searches by meaning with Gemini embeddings when `GEMINI_API_KEY`
(or `GEMINI_KEY`) is set (`-embedder off` for keywords only; `-embed-rate`
paces a free-tier key; vectors are kept in `<db>-vectors.gob`). Farmers'
Exa and Parallel keys are sealed in `<db>-vault.db` with the key ring in
`FARM_CREDENTIALS_KEY` or a key file made on first start. The assistant's
tools are served over MCP at `/mcp/<toolset>` (`-mcp=false` to turn it
off). `make farm-eval` runs the assistant's scenarios on a real model.
Notices also go out by the message lanes ([docs/MESSAGES.md](docs/MESSAGES.md)):
web push from the start, and Telegram, email, WhatsApp and SMS once their
keys are set (`FARM_TELEGRAM_TOKEN`, `FARM_SMTP_URL`, `FARM_WHATSAPP_TOKEN`,
`FARM_MSG91_KEY` or `FARM_TWILIO_SID`); `-notify-routes` composes them and
`-notify-caps` bounds the paid ones.

Sign-in is off by default: the app is one farmer's, as on a laptop.
`-signin google,password,invite` (`$FARM_SIGNIN`) turns it on with those
lanes, in that order on the sign-in screen; `-signup` says which lanes make
new accounts (default `google`). Google needs a web client from the Google
Cloud console (`FARM_GOOGLE_CLIENT_ID`, `FARM_GOOGLE_CLIENT_SECRET`) whose
redirect URI is `<-public-url>/auth/google/callback`. Then each farmer sees
only their own farm, can add family and workers, and a partner
(`FARM_PARTNER_EMAILS`) can sign a farmer up; `FARM_ADMIN_EMAILS` may
change the automations and the message settings, and the first admin's
farm takes the plots made before sign-in. Behind a TLS-terminating proxy,
set `FARM_TRUSTED_PROXIES`. Design: [docs/ACCOUNTS.md](docs/ACCOUNTS.md).

| Package | What |
|---|---|
| [geo](geo) | Area, perimeter, shape checks of small polygons on WGS84 |
| [farm](farm) | Plots and plot groups, units, the verifier, the service, the store interfaces; `sqlite`, `farmtest` |
| [planting](planting) | Plant groups spread over a group's plots, and what that does: shade, edges, spacing, water, neighbours, wind |
| [nominatim](nominatim) | Where a point is (reverse geocoding), a `farm.Locator` |
| [agro](agro) | Crop planning rules: soil and crop libraries, the recommender, layouts, plan checks; `agrotest` |
| [nasapower](nasapower) | Climate normals at a point (NASA POWER), an `agro.ClimateSource` |
| [osm](osm) | Map features near a plot from OpenStreetMap (Overpass, then the OSM API), classified; `osmtest` |
| [elevation](elevation) | Ground heights (Open-Meteo's Copernicus GLO-90, then Open Topo Data's SRTM 30 m), cached in a file |
| [land](land) | The land's shape, its survey (nearest roads, water, buildings, slope) and the farm layout with its checks |
| [surveyor](surveyor) | Surveys and layouts of the farms, point location, place search |
| [research](research) | Web search and extract on Exa or Parallel with the farmer's key in a vault; `researchtest` |
| [planner](planner) | Plans seasons for the farms: profile, recommend, lay out, check, save; distributions for plot groups |
| [handbook](handbook) | The assistant's knowledge base on farming and plants in India: chapters, a crop library chapter, keyword or hybrid search |
| [notify](notify) | Message lanes (web push, Telegram, email, WhatsApp, SMS) behind one interface, fan-out, chain and router, the outbox with quiet hours, retries, consent and cost caps; `sqlite`, `notifytest` |
| [farmagent](farmagent) | The farm, planning and map tools for agents (also over MCP), the skills (`plan-season`, `check-plan`, `survey-land`, `shape-farm`, `distribute-plants`, `research-products`), and the assistant (agentkit harness) |
| [farmapi](farmapi) | The HTTP API under `/api/` |
| [members](members) | Farms in the farmer's sense (holdings), their members and roles, invites, assisted sign-up; `memberstest`, `sqlite` |
| [farmauth](farmauth) | Sign-in for the farm app: the guard on `/api` and `/mcp`, `/api/me`, the members routes (on `pkg/auth/accounts`) |
| [webdist](webdist) | The web client's build, embedded |
| [cmd/farm](cmd/farm) | The binary |
| [web/apps/farm](../../web/apps/farm) | The web client (React, Vite, Leaflet) |
