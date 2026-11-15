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

## When to sow and sell (T147)

Plain arithmetic in `apps/farm/prices`, tested before it is trusted. Not
wired into the API, the assistant or the web client yet: the crop cycle
page (T145) calls one function, `prices.AdviseTiming(ctx, history,
TimingQuery{Crop, State, District, Market, DaysMin, DaysMax, Window, Sown,
Today})`, and gets advice or nothing, with reasons.

1. **Monthly price**: the median modal price of every quote of a month in
   the area (market, else district, else state, the nearest whose history
   gives an index).
2. **Seasonal index** (`NewSeasonalIndex`): each month's price over the
   centred 2×12-month moving average around it (the classical ratio to
   moving average, as in the Indian mandi studies below), the **median**
   over the years per month, scaled so the year averages 1; gaps of up to
   two months are filled on a log scale for the average but never become
   ratios. Each month carries its quartiles (the spread) and its years. It
   refuses (`ErrTooLittleHistory`) when fewer than 9 months of the year
   have 2 years each, and says "low" with 2 years, "fair" with 3 to 4,
   "good" with 5 or more and all twelve months.
3. **Harvest-month advice** (`AdviseSowing`): every week of the safe
   sowing window and the crop's shortest, usual and longest duration are
   ranked by their harvest month's index times the yield they keep (2% a
   week from the best agronomic date, 3% for an unusual variety: starter
   values). A shift is advised only when its expected gain passes 3% plus
   half the combined spread of the two months; otherwise "sow at the best
   agronomic date".
4. **Sell or hold** (`AdviseSell`): from the harvest month (or today, once
   harvested) each later month's price is today's moved by the index, less
   the loss in store, the storage cost and the interest, per commodity class
   (`StorageFor`): pulses and oilseeds 0.5% a month, cereals 0.4%, cotton
   0.3%, onion 8%, ₹15 a quintal a month (onion ₹10), 1% a month interest,
   kept at most 6 to 8 months (onion 4); tomato and other perishables are
   never held. The same margin rule decides between "sell now" and a month.
5. **Backtest** (`BacktestSell`, `BacktestSowing`): every past year is
   replayed with an index from the months before the decision only (the
   harvest month for selling, the best sowing date for sowing), and scored
   on the year's real prices against (a) selling in the harvest month or
   sowing at the best date and (b) "same as last year", the choice that paid
   best the year before. It reports the mean gain over each, the hit rate
   (years strictly better), and the MAPE of the later months' price forecast
   from the harvest month's, the index's and last year's shape's.
6. **Gate** (`Gate`): advice is shown for a crop and area only if its rule
   was tested on at least 3 years, gained on average over both baselines,
   and lost no more years than it won against selling at harvest.

`make farm-price-backtest FARM_PRICE_FILES=<folder>` prints the table for
every crop and area in the files (`prices.ScanFiles` streams files of any
size; the columns of India Data Portal's and CEDA's Agmarknet files are
mapped in `files.go`); without the folder it runs on synthetic series.

### What the backtest showed (2026-11-15)

- **Synthetic** (`prices/synthetic`, 15 years, a post-harvest dip of 0.84 to
  1.15, trend 4% a year), sell-or-hold for groundnut from October, 40
  seeds each: with a 5% monthly shock the gate showed the advice for 20 of
  40 seasonal histories and for 1 of 40 with no season; at 10%, 17 and 4;
  at 15%, 15 and 5. Where it holds, the rule beat selling at harvest by 7%
  to 26% a year, but "same as last year" is a strong baseline for a steady
  season (both pick nearly the same month), so about half of the good
  advice is hidden. A season that turns round (a regime break) passed 1 of
  40 at 5% noise. The sowing rule rarely passes (3 to 7 of 40): the yield
  risk of moving the date eats most of a month's price difference.
- **Real**: India Data Portal's Agmarknet files (ODC-By), January 2021 to
  May 2026, Andhra Pradesh, Telangana and Karnataka, nine crops: 326,513
  quotes. Only 41 of 385 crop, area and rule combinations had 3 or more
  years to test (five years of data less two to train); 4 passed: groundnut
  sell-or-hold in Telangana (hold from October, +28.6% a year over selling
  at harvest, +1.3% over last year's choice, 3 years), and the tomato
  sowing rule in three Karnataka districts (3 years each, MAPE 22% to 35%:
  thin, and tomato's swings are larger than any index). In Andhra Pradesh
  (Kurnool is most of the state's data; Kadapa has 33 months, Anantapur
  too few) nothing passed: groundnut, tur, cotton and onion advice never
  differed from selling at harvest in the tested years, and tomato's lost
  to "same as last year". **Validation on real data is still open**: it
  needs CEDA's 2000-2023 files (non-commercial terms) or years more of the
  daily feed, and the gate should then rise to 5 years.

### Evidence

Read for the method and the starter values (2026-11-15):

- H.K. Suma, S. Rajeswari, K. Umadevi, Sk. Nafeez Umar (ANGRAU),
  [Time series analysis of onion prices from Solapur market](https://sasapjas.org/time-series-analysis-of-onion-prices-from-solapur-market-of-maharashtra-india),
  Andhra Pradesh J. Agril. Sciences: monthly NHRDF prices 2005 to 2019,
  multiplicative model, seasonal indices from a 12-month centred moving
  average adjusted to sum to 1200; November highest (130), May lowest
  (57.7). The index method here is theirs, with medians for robustness.
- Ajmal S., S. Rohith, Unniravisankar P., O. Nabay (ICAR-IARI),
  [Price dynamics of tomato, onion and potato in India](https://ageconsearch.umn.edu/record/367914/files/Ajmal4232024AJAEES112355.pdf),
  AJAEES 42(3), 2024: monthly wholesale prices 2005 to 2021, twelve-month
  ratio to moving average seasonal indices, Cuddy-Della Valle instability;
  tomato and onion the most unstable, which is why perishables get no hold
  advice and a margin for spread.
- PJTAU Agricultural Market Intelligence Centre,
  [market intelligence page](https://pjtau.edu.in/market-intelligence/):
  pre-sowing and pre-harvest price forecasts and monthly outlooks for
  Telangana crops, from "15-20 years of historical market data" with ARIMA,
  SARIMA, ARCH, GARCH and ANN models plus trade surveys; the
  [Redgram](https://drive.google.com/file/d/1RjuFkemYLC9d-zKQNaFDg0Pp0BcUs_ZF/view)
  and [Groundnut](https://drive.google.com/file/d/1jx0I6xsX0j7aY8Fjw4gv7BR6DfZIhAgg/view)
  outlooks of November 2026 give a month's range (₹9,700 to 10,000 and
  ₹6,750 to 7,250 a quintal). A cited second opinion for the app; their
  length of history is what our gate is waiting for.
- M. Burke, L. F. Bergquist, E. Miguel,
  [Sell low and buy high: arbitrage and local price effects in Kenyan markets](https://www.nber.org/papers/w24476)
  (NBER w24476, QJE 2019): seasonal grain price swings give storage a
  return (about 28% on a timely harvest loan) that credit-constrained
  farmers miss, and storing by many farmers flattens the local swing. Why
  hold advice is worth giving, and why it must be re-tested as it is used.
- NABCONS (2022) post-harvest losses, as tabled in Priyadarsani, Bisen and
  Tiwari, [Food losses, food security and climate change](https://epubs.icar.org.in/index.php/IndFarm/article/download/155375/62630/489767),
  Indian Farming 75(4), 2025: whole-chain losses of pigeonpea 5.65%,
  groundnut 5.73%, cottonseed 2.87%, maize 3.89%, onion 7.26%, tomato
  11.61%. The monthly storage losses here are a small share of those
  (starter values).
- N.V. Mane et al. (PDKV Akola),
  [Storage studies in onion](https://phytojournal.com/archives/2020/vol9issue6/PartT/9-6-339-608.pdf),
  J. Pharmacogn. Phytochem. 9(6), 2020: in ambient storage, mean weight
  loss 14.3% at 60 days and 21.3% at 90, rotting 6.5% and 12.6%: the
  onion's 8% a month.
- Central Warehousing Corporation,
  [storage tariff circular w.e.f. 1 July 2020](https://pswc.in/PDF/tariff/CWC%20Merged%20Pages%20-%20PSWC%20Tariff%20For%20Storage%20of%20Notified%20Commodities.pdf)
  (via Punjab SWC): grain and pulses stored on a bag basis, insurance 5
  paise per ₹100 a month, a 30% rebate for farmers storing their own
  produce. Its rate annexure did not read as text, so ₹15 a quintal a month
  is a starter, not a quoted rate.

Data: India Data Portal's
[Agriculture Marketing](https://ckandev.indiadataportal.com/dataset/agriculture-marketing)
dataset (Open Data Commons Attribution: cite "India Data Portal,
Agmarknet, Directorate of Marketing and Inspection"), resources "APMC
Arrivals And Prices (Old Source)" 2021-2025 (4.2 GB) and "(New Source)"
2025-2026; read in this session, not committed. CEDA's Agri Market data
(agmarknet.ceda.ashoka.edu.in) is free for non-commercial use with its
citation; its README did not load from here, so its column mapping follows
the README's description in search results and a synthetic fixture
(`testdata/synthetic-bulk`), not a real file. Not found or not read: a
SARIMA study of groundnut or tur prices in AP mandis, and TNAU's forecasts.
