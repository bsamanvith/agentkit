# Crop cycles: the farmer's plan, our suggestion, and what follows from them

> Design for T145 to T148 (owner's brief, 2026-11-15). The product list
> is [FEATURES.md](FEATURES.md); the season's tasks are
> [ROADMAP.md](ROADMAP.md); prices are [PRICES.md](PRICES.md).

## The idea

A **cycle** is one crop season on one plot: what is grown in each area,
when it is sown and when it is harvested. Everything a farmer does hangs
off it: the work, what to buy, when to spray, when to sell.

Each plot has two lines of cycles, side by side:

- the **suggested** line, which the app works out and keeps up to date;
- the farmer's **own** line, which the farmer fills in or leaves empty.

The **planned** cycle is the farmer's own where there is one, and the
suggested one otherwise. The farmer may ignore every suggestion; the app
still plans from what they really did, and still shows what it would
have suggested and why.

## Basics first (owner, 2026-11-15)

The basic path is short and has to be worth it alone: **draw the plot,
say the crop (or take ours), get the dates.** After three steps the
farmer sees when to sow, when the crop comes off and what to do this
week. Nothing else is required.

Everything else is optional and additive: areas inside a plot, the
farmer's own dates, future cycles, organic or conventional, the shopping
list, sprays, sell timing, the assistant. Each is one tap from the
cycle, is never asked for up front, and never blocks the basic path
when it is skipped or its data source is down. A screen shows the next
useful thing, not every setting.

## Onboarding (T146)

1. Sign in, type the village, the map flies there.
2. Draw the plot: **pencil** (freehand, smoothed to corners),
   **lines** (tap each corner, as today), **rectangle** (drag, then turn
   and stretch), or walk it with GPS, or type coordinates.
3. Divide it into **areas** the same way (a line across the plot cuts it
   in two; a shape inside makes an area), or keep it as one.
4. Say what they want to grow in each area, or ask for suggestions.
5. The app shows the suggested cycle; the farmer accepts it, changes the
   dates, or says "I already sowed on …".

No step needs the assistant; every step can also be done by asking it.

## Cycles (T145)

- A cycle has: plot, position in the line (0 = current, 1 and 2 = next),
  season (kharif, rabi, summer), areas with crops (the `agro.Plan`
  zones), **method** (`organic` or `conventional`), sowing date
  (`planned` or `actual`), expected harvest (from the crop's days), and
  origin (`suggested` or `farmer`).
- **Linked.** Cycle n+1 starts after cycle n's harvest plus a turnaround
  (field preparation), and is chosen with cycle n as its "last crop"
  (rotation: not the same family twice, a pulse after a cereal, what the
  last crop leaves in the soil). Changing a date or a crop in cycle n
  recomputes the suggested cycles after it. The farmer's own later
  cycles are never changed, only checked, with a warning when they no
  longer fit ("cotton now ends 20 January; your groundnut sowing of
  5 January is before that").
- **Three at most:** the current cycle (or the upcoming one, when the
  land is fallow) and the next two. Later cycles are optional; the
  farmer can turn future planning off.
- **Nothing planted, nothing entered:** the suggested line is the plan.
- **Something useful today.** A crop that suits sowing within six weeks
  is preferred to a better one months away; when nothing does, the page
  says so first, with what to do meanwhile and what comes next, and
  names the assumption that drives it (rain only, when the water was
  never given) with one tap to change it.
- **Actual beats planned.** A recorded sowing (the *Sown* tick of the
  season roadmap, or a date the farmer types) becomes the cycle's date
  and moves everything after it.
- The current cycle's plan stays the plot's saved plan, so the season
  roadmap, weather advice and prices keep working unchanged.

## Inputs and sprays (T148)

From a cycle's crops, areas and method the app computes, as rules:

- a **shopping list**: seed, fertilisers or manures by split, and the
  usual plant protection, with quantities for the area in the units
  sold (kg, bags of 45 or 50 kg, litres), and when each is needed;
- a **spray and feeding schedule**: what, at which crop stage, how much
  product in how much water per acre, the wait before harvest, and
  "not before rain, not in wind" which the weather rules already check.

`organic` swaps every item for its accepted organic counterpart (farmyard
manure, vermicompost, neem cake, jeevamrutham, neem oil, pheromone traps,
Trichoderma) and never lists a synthetic product. Preventive items are
scheduled; curative ones are listed as "only if you see …". Every value
is a starter value with its source, to be reviewed by an agronomist
(ANGRAU, a KVK) before farmers rely on it; the app says so, and tells
the farmer to follow the product label. Design, sources and what needs
review: [INPUTS.md](INPUTS.md).

## When to harvest and sell (T147)

Prices fall when everyone harvests. For each crop and market the app
computes a **seasonal index** (each month's usual price against the
year's average) from several years of mandi prices, and uses it twice:

- **before sowing:** inside the agronomically safe sowing window, and
  among the crop's durations, prefer the date whose harvest month has
  the better index (the gain must beat the yield risk of leaving the
  best sowing date);
- **at harvest:** sell now or hold, for storable crops, net of storage
  loss and cost (the existing sell-or-hold skill gets the index).

It is tested before it is trusted: a **backtest** replays past years
(the index built only from the years before) and compares the suggested
month with selling at harvest and with "same as last year". A crop and
market where the rule does not beat the naive guess shows no timing
advice. Sources, free first: Agmarknet on data.gov.in (daily), CEDA
Ashoka's Agmarknet files 2000 to 2023 (**free for non-commercial use
only**), India Data Portal 2019 to 2025; state university price
forecasts (PJTAU, TNAU) as a cited second opinion. The season's weather
outlook narrows the sowing window; it does not predict the price.

## Products around the cycle

The cycle is a record of what will be grown, where, when, with what, and
what it should fetch. Other products read it; none needs new farmer
input. Planned, in the order they become possible:

| Product | For whom | What the cycle gives it | Needs |
|---|---|---|---|
| **Input list to a dealer** | farmer, input dealer | the shopping list with dates; a dealer or the FPO quotes it, the farmer compares | T148 |
| **Season budget and crop loan sheet** | farmer, bank (KCC) | cost of the list plus labour from the roadmap, expected income from the price estimate | T148, T147, ledger (16) |
| **Crop doctor with guided photos** | farmer | crop, stage and recent sprays, so the app can ask for the right photos (whole plant, leaf top and underside, with a coin for scale) and answer for this crop today | T148, feature 15 |
| **FPO season view** | FPO, extension worker | members' planned cycles: area by crop, expected harvest by week, input demand to buy in bulk, produce to sell together | T145, accounts |
| **Harvest and sale desk** | farmer, FPO, buyer | expected harvest dates and quantities weeks ahead: book a harvester and labour, offer a lot to a buyer before the glut | T145, T147 |
| **Insurance and scheme deadlines** | farmer | sowing date and crop fix the PMFBY cut-off and the 72-hour loss notice; tasks on the roadmap | T145 |
| **Agronomist's review desk** | reviewer (ANGRAU, KVK) | every rule value with its source, where it is used, and a way to correct it for a district | T148 |
| **Plot history** | farmer, lender | finished cycles: what was grown, yield, price got; next year's suggestions learn from it | T145, ledger |

## What is built first

T146 (drawing and onboarding), T145 (cycles), T147 (seasonal index and
backtest) and T148 (inputs and sprays) were built in parallel, then
joined (branch `wt-join`, 2026-11-15):

- **The basic path stands alone.** Village, pencil, name, then a crop or
  *Suggest for me*: the first run ends on the plot's *Cycles* tab with
  the suggestion shown and one *Use this* tap. Cycles is the first tab of
  a plot with no plan (when the planner and cycles are on); the tab order
  stays put once the plan is saved.
- **Addons, picked in `cmd/farm`** (`apps/farm/cycle/addon`): `inputs`
  plans each cycle from its areas (an intercrop split by its rows),
  method, the plot's water (irrigated when its profile names a source)
  and sowing; `timing` asks `prices.AdviseTiming` with the saved price
  history, the crop's days and, before sowing, the recommender's safe
  window around the planned day (kept 30 minutes). An addon with nothing
  to say is left out; one that fails adds a note naming it, never an
  error on the line.
- **Under this cycle, three optional sections, closed** (a lazy chunk):
  *What to buy* (before sowing, later, and "only if you see", in the
  units sold, for the plot's area), *Feeding and sprays* (dated, with the
  dose, water per acre, the wait before harvest and cautions) and *When
  to sell* (only with advice; otherwise one quiet line, "not enough price
  history here yet"). The starter-values and follow-the-label notes sit
  wherever doses show. *Grown: Conventional / Organic* recomputes them.
- **Season work carries the plan.** `work.WithTasks` takes the inputs
  schedule of the current cycle (its method from the cycle store): an
  entry on a calendar task's key enriches that task (a fertilising one
  takes the plan's products and quantities), a feeding within 21 days of
  a calendar feeding takes that task's place at the sourced day (no
  twins) under its own ID, so a tick stays with what was done: the
  jeevamrutham ticked on the organic plan is not the urea feed that takes
  the same place on the conventional one (that one is open), and is
  still done when the farmer switches back. The rest (jeevamrutham,
  preventive sprays) are tasks of their own with calendar-style keys, so
  a moved sowing keeps their ticks.
- **The assistant** answers "what should I buy" from `get_inputs`
  (numbers from the plan, never the model); `get_cycles` carries the
  timing advice and points to `get_inputs`.
- **A sowing far from the plan** (more than `work.MaxSowShift`, 120
  days): *I already sowed on…* saves the plan again with that day rather
  than refusing it.

Still open: the cycle-0 plan (the farm's saved plan) and the cycle record
are two writes, not one transaction, so a crash between them can leave
the record's method or sown day pointing at an older plan (it is then
ignored, as `SownFor` says). Preferring a sowing date by its harvest
month inside the window (`Input.suggest`) is not built. Guided photos
come with the crop doctor.
