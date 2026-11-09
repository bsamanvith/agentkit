# Farm: crop prices

> T140, 2026-11-08. Owner's ask: price estimates, and real mandi prices
> across the state, Karnataka, Andhra Pradesh, Telangana and Tamil Nadu
> first, from a single all-India source if there is a good one; many
> providers and many ways to fetch, because this space is fragmented and
> providers keep changing; free sources and free tiers first. Feature 12
> in [FEATURES.md](FEATURES.md).

## What the farmer gets

- **Today's mandi prices** near the plot: each market's newest min, max
  and modal price (rupees a quintal) in the plot's district, then the
  rest of the state's markets that traded that week, with the day and
  the source. A district with none widens to the state, and says so.
- **The last year, month by month**: the median modal price and the
  month's low to high.
- **What it may fetch** in the month the farmer plans to sell: low,
  likely and high, a confidence, the reasons, and with an expected yield
  (quintals an acre) what the plot's harvest may bring, before costs.
- **The minimum support price** beside it (kharif 2026-27, rabi
  2027-28), with the warning that it is paid only where the government
  procures.
- The **assistant** answers "what is the price of tur", "where should I
  sell", "should I hold till March" with the tools `mandi_prices` and
  `price_estimate` and the skill `sell-or-hold`, in the farmer's
  language. Tested on Claude in Telugu.

## Sources (surveyed 2026-11-08)

| Source | What | Access | Cost | Used |
|---|---|---|---|---|
| **Agmarknet on data.gov.in** (resource `9ef84268-…`, "Current Daily Price of Various Commodities from Various Markets (Mandi)") | The latest day's min, max and modal price of every reporting mandi in India (about 4,300 markets), from the Directorate of Marketing and Inspection | JSON API with a key (`FARM_DATAGOV_KEY`) | **Free** (register at data.gov.in for a key); Government Open Data License | yes, provider `datagov`: the one all-India source |
| Agmarknet portal (agmarknet.gov.in, 2.0 since Nov 2025) | The same data with history; report export as CSV/Excel | website | Free | yes, as files (`files`): download a report, drop it in the folder |
| data.gov.in variety-wise history resources | Daily prices by variety, back years | the same API, another resource id | Free with the key | ready: `WithDataGovResource`, not wired by default (resource id to confirm) |
| CEDA Agri Market (Ashoka University, api.ceda.ashoka.edu.in) | Agmarknet prices and arrivals since 2000, cleaned, monthly updates | API with a key, CSV downloads | **Free** with registration, non-commercial terms to check before commercial use | as files today; an API provider later |
| e-NAM (enam.gov.in) | Trade prices in e-NAM mandis | dashboards, no public API found | Free to view | no: scraping a dashboard is a last resort |
| State boards (Karnataka KRAMA, Tamil Nadu agmark, AP and TS marketing departments) | State mandis, sometimes fresher | websites | Free | not yet: they all feed Agmarknet; add one as a provider if it proves fresher |
| Paid aggregators (Farmonaut, commodity data sellers) | Agmarknet resold, with SLAs | API | **Paid** | no; nothing here needs them |

Reachability: from the cloud sessions both data.gov.in (connection reset)
and agmarknet.gov.in (403) refuse the connection, and so does the
sandbox shell on the owner's computer (its proxy). The app run from an
ordinary Indian connection is expected to reach them; until a key is set
and that is tried, the `datagov` provider is tested against a fake with
the platform's answer shape, and the app was run on price files.

## How it is built

```
farm binary flags ──► feeds.Pick("datagov,files") ──► []prices.Source
                                                         │
prices.Service ── feeds.Cached( feeds.Chain(            ▼
                    Recorded(Paused(datagov)),   ← answers saved in the history
                    Recorded(Paused(files)),
                    Saved(history) ))            ← last known prices when all are down
               ── Import(files) → history         (at start, in the background)
               ── EstimatePrice(history, …)       (arithmetic, below)
```

- **`apps/farm/feeds`** is the provider layer, with no farm in it, for
  every kind of outside data (prices now; weather, news and knowledge
  can move onto it): `Provider[Q, R]`; `Chain` (first answer wins; "no
  data" and "unavailable" pass to the next; the error names each);
  `Paused` (a 429 stops asking until Retry-After, else an hour);
  `Cached` (TTL, bounded); `Pick` (a comma list of names to providers,
  skipping one whose key is not set); `GetBody`/`GetJSON` sort HTTP
  failures for a chain (5xx and 403 unavailable, 404 no data, 429 rate
  limit).
- **`apps/farm/prices`**: `Quote` (one day, one market, one commodity,
  variety and grade); `Query` (state, district, market, commodity,
  dates; names matched loosely: case, punctuation, Rangareddy and
  Rangareddi); `Commodity` maps the app's crop ids and farmers' words
  (kandulu, tur, arhar) to Agmarknet's names; providers `DataGov` and
  `Files`; `History` (in memory, or SQLite in `prices/pricesqlite`, the
  file `<db>-prices.db`); `MSP`; `EstimatePrice`; `Service`.
- **Fetch methods** are providers: an API (`DataGov`), files (`Files`:
  Agmarknet's report export, data.gov.in's CSV, CEDA's CSV, or a hand-made
  sheet; columns found by header; a file without a state column takes it
  from its folder, `karnataka/tomato.csv`), and the saved history. A page
  scraper is one more `Source` when one is needed.
- **The history grows by itself**: every answer from any provider is
  saved, so estimates get better day by day even with only the daily API.

## The estimate

Plain arithmetic, so it can be checked and explained:

1. **Area**: the nearest of market, district, state, India with at least
   20 prices over 3 months; else the one with the most.
2. **Now**: the median modal price of the newest 30 days; the trend
   against the 30 days before.
3. **Season**: for each year with six or more months, each month's median
   over the year's average; the median over years per month. Needs nine
   months covered.
4. **Likely** = now × season[selling month] / season[now's month].
5. **Range**: ±(8% + 4% a month ahead), at least the selling month's
   spread over the years and today's spread across markets, at most 50%.
6. **Confidence**: good (a season from two or more years, and recent
   prices), fair, low (prices older than 45 days).
7. **Revenue** = price × yield × acres, before costs.

## Configuration

| Flag | Env | Default | What |
|---|---|---|---|
| `-prices` | `FARM_PRICE_PROVIDERS` | `datagov,files` | providers in order; `off` for saved prices only |
| | `FARM_DATAGOV_KEY` | | data.gov.in key; without it `datagov` is skipped |
| `-price-files` | `FARM_PRICE_FILES` | | folder of price files, read at start and when one changes |
| `-datagov-url` | `FARM_DATAGOV_URL` | api.data.gov.in | for tests |

## Next

- Try `datagov` with a real key from an Indian connection, record a
  cassette, and confirm the variety-wise history resource for backfill.
- A daily refresh of the plan's crops (an automation rule on T139's
  engine) so the history grows without anyone asking, and a price alert
  ("tur passed ₹8,500 at Tandur").
- Distance and transport cost to each mandi (mandi coordinates from
  OpenStreetMap or Nominatim), so "farther but dearer" is a number.
- CEDA's API as a provider for history; a state board as a provider if
  it proves fresher than Agmarknet.
- Price estimates in the crop recommender and the plan (expected income
  per plot).
