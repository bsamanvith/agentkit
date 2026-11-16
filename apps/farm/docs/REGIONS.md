# Region packs: the same rules, local data

> Design for T149 (owner's ask, 2026-11-15: "ensure we are building
> software in a good, open-to-changes way"). Methods, insurance, the
> cycle rules and the crop library can be the same or differ from region
> to region, and a difference must plug in later without a code change.
> The product list is [FEATURES.md](FEATURES.md); cycles are
> [CYCLES.md](CYCLES.md); inputs [INPUTS.md](INPUTS.md).

## The idea

A **region pack** is everything that may hold differently where a plot
is: the crop library (`agro.Library`), the inputs book (`inputs.Book`),
the crop cycle rules (`region.Rules`), the storage values and support
prices of sell timing, and schemes such as crop insurance.

A pack is the **base set** plus small **overlays**, keyed by country,
state and district. The most specific overlay wins, field by field: a
district overlay laid on its state's, laid on its country's, laid on the
base. A place that no overlay names gets the base, so a region that
overrides nothing behaves exactly as the app did before packs.

```
base  ─▶  IN (none yet)  ─▶  Andhra Pradesh  ─▶  YSR Kadapa
                                         ├──▶  Anantapur
                                         └──▶  Sri Sathya Sai
```

## What an overlay may change

One JSON file per region (`data/overlays/*.json` in `apps/farm/region`,
embedded; or a file in the `-regions` folder, `FARM_REGIONS`):

| Section | Keyed by | What it can do |
|---|---|---|
| `crops` | crop id | change any field of a crop (`days`, `seasons`, `windows`, `textures`, ...), add a crop (all its fields), switch one off (`"off": true`) |
| `inputs.crops` | crop id | change a crop's doses, splits, seed, uses, protection (each field whole) |
| `inputs.products` | product id | change or add a product |
| `inputs.banned` | | add an active to the banned list (a state ban; national bans cannot be lifted) |
| `rules` | | `turnaround_days`, `max_wait_days`, `wait_days_per_point`, `rotation` (`same_family`, `legume_after_other`, `cereal_after_legume`, each on its own), `seasons` |
| `storage` | storage class | a class's loss, cost, interest or months |
| `msp` | commodity, variety, season | change or add a support price (a state bonus) |
| `schemes` | scheme id | change or add a scheme: name, kind, the one season and year a notice is for, crops covered, cut-offs (season, enrolment or sowing, `MM-DD`), loss-notice hours, a note |

**Field-level merge.** Each field an overlay names replaces the base's
field whole, except a field that is itself an object of plain fields
(`rules.rotation`), which merges by its own fields. Lists replace: an
overlay's `windows` or `seasons` are the whole list.

**Every value keeps its source.** Each entry carries a `ref`
(`{"source", "where"}`) naming one of the overlay's `sources` (id, name,
publisher, url, the day it was read). The pack records the layer, the
file and the ref of every path an overlay set (`Pack.Origin("crops.groundnut.windows")`
is `{Layer: "YSR Kadapa", File: ..., Ref: ...}`; an unset path is
`{Layer: "base"}`), so a review desk can show "base", "Andhra Pradesh"
or "YSR Kadapa" beside each number.

**Switching a crop off** keeps it in the library (a plot that already
grows it keeps its plan, calendar and inputs) but the recommender and the
cycle suggestions leave it out there; asked for by name, it is still
scored.

**Sowing windows** (`agro.Crop.Windows`, new; the base has none): a
local window for a season and, optionally, rainfed or irrigated plots
only. A sowing inside one is a reason ("inside the local sowing window,
1 June to 31 July (source)"); outside every window that applies to the
plot's water it is a risk, and the sowing's season factor is multiplied
by 0.9, the same kind of nudge as an unusual season (0.8). A crop with no
window for the plot's water is scored as before.

**Schemes are read (T151).** Package `schemes` reads a plot's pack for
its current cycle: which schemes cover its crops (a scheme that names no
crops may cover any: the state notifies them), the season's cut-off and
the days left, and the loss-notice hours, each with its source, layer and
read date. From it come the season roadmap's insurance tasks, the
`weekly-insurance` alert, the cycle's *Insurance* section, `get_schemes`
and `/api/farms/{id}/schemes` ([FEATURES.md](FEATURES.md#20-schemes-insurance-and-paperwork)).
A notice for one season carries `season` and `year` ("kharif", 2026:
the season that starts that year); read for a later season it gives last
year's dates, "not yet announced for this season", never a live
deadline. Rules that hold every season (PMFBY's guidelines) leave both
out.

## Checked at startup

`region.Load` (from `cmd/farm`, with `-regions`) reads the base, the
embedded overlays and the folder's, then builds the pack of every region
named and checks it: the overlay's own shape (unknown keys anywhere are
errors, so a typo cannot pass silently; values of the wrong type; every
entry has a ref to a listed source), then the whole library with
`agro.Load` and the whole book with `inputs.Load` (so an overlay cannot
add an unknown crop to the inputs, a product whose active is banned, a
split that does not add up), the rules (months covered once, positive
waits) and the schemes (crops in the library, `MM-DD` dates, a year
only with a season, cut-offs of a one-season notice in that season). Any error
stops the start with `region.ErrInvalid`, naming the file and the key:
`region: invalid: bihar.json: crops.groundnut.dayz: unknown key`. Two
files for one region are an error too.

Because every pack is built at startup, `Set.For(place)` never fails and
costs a few string compares: packs are built once and kept (the cache).

## Where it plugs in

There is one picker: `cmd/farm` loads the `region.Set` and hands it to
the planner (`planner.WithRegions`). Everything that plans for a plot
asks the planner, never a global:

| Consumer | Takes from the plot's pack |
|---|---|
| `planner.Recommend`, `Layout`, `Check`, `SavePlan` | the library |
| `planner.Sun` | the seasons |
| `cycle.Service` (`cycle.Input.Library`, `.Rules`) | the library and the rules: turnaround, max wait, wait cost, rotation, seasons |
| `cycle/addon` Inputs and Roadmap (`addon.WithPacks`) | the inputs book and the library |
| `cycle/addon` Timing (`addon.WithPacks`) | the library (crop days) and the storage values (`prices.TimingQuery.Storage`) |
| `work` (season roadmap), `watch` (calendar, weekly advice), the assistant's calendar and compare tools | the library (`planner.LibraryFor`) |
| `cycle/addon` Schemes (`addon.WithPacks`): the roadmap's insurance tasks, the cycle's insurance, `get_schemes`, the cut-off alert | the schemes, their sources and origins, the seasons |

The plot's place is its boundary's location (`farm.Report.Location`, from
Nominatim): country code, state, district. Names match whatever their
case and punctuation, and an overlay lists `aliases` for the spellings a
geocoder gives ("Y.S.R. Kadapa", "Cuddapah"; "Anantapuramu"). A plot
whose place was not looked up gets the base.

A new region is a new JSON file in the folder (or, once reviewed, in
`data/overlays`): no code changes, no switch, no `init()`.

## Package shape

`apps/farm/region` sits above `agro`, `inputs` and `prices` and below
`planner`, `cycle` and `cycle/addon`; it imports nothing else of the app
and no new module. It is pure: no IO beyond reading the overlay files at
`Load`, no package state.

- `Load(fs.FS) (*Set, error)`, `Open(dir) (*Set, error)`
- `Set.For(Place) *Pack`, `Set.Base() *Pack`, `Set.Packs() []*Pack`
- `Pack.Library()`, `Book()`, `Rules()`, `Storage(commodity)`,
  `MSP(commodity)`, `Schemes()`, `Sources()`, `Layers()`, `Key()`,
  `Name()`, `Origin(path)`, `Origins()`
- `Rules` (with `DefaultRules()`, `SeasonOf`, `Or()`), `Rotation`,
  `SeasonSpan`, `Scheme`, `Cutoff`, `Place`, `Ref`, `Source`, `Origin`
- `ErrInvalid`

`cycle.Turnaround` and `cycle.MaxWait` stay, as the base rules'
(`region.BaseTurnaroundDays`, `region.BaseMaxWaitDays`). A zero
`cycle.Input.Rules` means the base rules, so code that builds an `Input`
without a pack works as before.

## The first real overlays (embedded)

Only values read on a source, each with its reference:

- **Andhra Pradesh** (`in-andhra-pradesh.json`): a scheme, crop
  insurance for kharif 2026 (PMFBY and RWBCIS; 22 notified crops, of
  which the source names paddy, red gram, dry chilli, maize and turmeric,
  with their sums insured per acre), from Deccan Chronicle, 30 October
  2026.
- **YSR Kadapa** (`in-andhra-pradesh-kadapa.json`): the normal sowing
  periods of the district's agriculture contingency plan (ICAR-CRIDA with
  RARS Tirupati, KVK and DAATTC Kadapa, table 1.12): groundnut rainfed
  kharif 1 June to 31 July and irrigated rabi November to December;
  paddy irrigated June to August and November to January; cotton rainfed
  June to July.
- **Anantapur** (`in-andhra-pradesh-anantapur.json`), the scarce
  rainfall zone: the district plan's table 1.12: groundnut rainfed kharif
  in July and irrigated rabi 15 November to 30 December; paddy irrigated
  16 July to 15 August and 1 to 30 December; bengal gram rainfed rabi
  16 October to 15 November; red gram rainfed kharif 16 June to 15
  August.
- **Sri Sathya Sai** (`in-andhra-pradesh-sri-sathya-sai.json`), carved
  out of Anantapur in April 2022 (headquarters Puttaparthi). No plan has
  been published for the new district, so it carries the same windows
  from the undivided Ananthapuramu plan, which covered this land. It is
  a file of its own so the district's values can part from Anantapur's
  when a local source exists.

The base carries one scheme from the PMFBY operational guidelines
(2020): kharif enrolment cut-off normally not after 15 July, rabi 15
December, each district's date in the state's notification; a localised
or post-harvest loss reported within 72 hours.

## Still in code, to move into packs later

- `agro.SeasonOf` inside the recommender's scoring (the "usual season"
  note) still uses the Indian season months; the packs' seasons reach the
  cycles' season label and the Sun tab.
- The recommender's weights (temperature, water, soil, light, the 0.8
  unusual-season and 0.9 outside-window factors) and `agro`'s fixed
  constants (horizon, step).
- The inputs plan's rules (the jeevamrutham cadence, kind defaults) are
  data in the book already, but `inputs` still names India's units and
  NPOP classes in code.
- Sell timing's `prices.Tuning` and the backtest gate; MSP is in the pack
  but the price tools (not tied to a plot) still read `prices.MSP`.
- The soils list (one for all India); the handbook and the crops API list
  the base library.
- Schemes are not shown or alerted on yet.
