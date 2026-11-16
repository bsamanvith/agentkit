# Inputs and sprays

`apps/farm/inputs` (T148) turns a crop, an area in hectares, a method
(organic or conventional), a watering (rainfed or irrigated) and a sowing
date into a shopping list, a dated feeding and protection schedule, and a
list of what to buy only if a pest or disease shows up. It is rules and
data, no model. Part of [crop cycles](CYCLES.md#inputs-and-sprays-t148).

## What is data

`data/inputs.json`, embedded, read on 2026-11-15:

- **Products**: nutrient content, the unit and pack they are sold in
  (urea 46 % N in 45 kg bags, DAP 18-46 in 50 kg bags, biofertilisers
  in 200 g packets), whether they are synthetic, and their class in
  India's organic standard (NPOP Annex 3: permitted or restricted).
- **Crops** (34 of the library's 75 have their own values; the rest use their kind's default): seed rate,
  N-P2O5-K2O per hectare rainfed and irrigated (grams per plant for the
  first year of a tree), the splits by stage, the extra inputs (gypsum,
  zinc sulphate), the manures and biofertilisers, the seed treatments, and
  the common pests and diseases of the pilot crops (groundnut, red gram,
  cotton, chilli, tomato, rice, sorghum, pearl millet, finger millet,
  chickpea, sunflower, mango, maize): when they show up, what the farmer
  sees, the threshold to act, prevention, an organic remedy and a
  conventional one as an active ingredient with its dose, spray volume
  and pre-harvest interval (PHI).
- **Kind defaults**: mustard, quinoa, chia, dragon fruit and avocado have
  no values of their own and take the feeding of a crop of their kind
  (sesame, sorghum, sesame, mango, mango), marked default, with no seed
  rate and no pesticide dose.
- **Jeevamrutham**: 200 litres per acre every 15 days (10 kg cow dung, 2 kg
  jaggery, 2 kg pulse flour per batch) until the crop matures, organic
  route only.
- `data/banned.json`: the actives banned, withdrawn, refused, restricted or
  proposed for a ban or restriction in India (PPQS list as on 31.03.2024
  as Wikipedia copies it, the 2023 prohibition order, the 2020 draft of 27,
  the 2026 carbosulfan draft and the paraquat dichloride reports). No
  active on it may be in the data for any crop, even one only restricted
  on another crop: quinalphos, mancozeb, carbendazim and the like are left
  out entirely. Loading data that names one fails.

Every value carries a reference: a source (name, publisher, URL, the day
it was read) and the page or section. `Book.Values` lists them all, with
where each is used, for a review desk.

## Sources

| Source | Used for |
|---|---|
| TNAU Crop Production Guide, Agriculture 2020 ([pdf](https://agritech.tnau.ac.in/pdf/AGRICULTURE.pdf)) | field crops: seed, NPK and splits, manures, thresholds, organic remedies |
| TNAU Crop Production Guide, Horticulture 2020 ([pdf](https://agritech.tnau.ac.in/pdf/HORTICULTURE.pdf)) | vegetables, fruit, spices |
| CIB&RC Major Uses of Pesticides, up to 31.10.2019 ([Odisha copy](https://www.epestodisha.nic.in/public/documents/Major%20use%20of%20Pesticides%20by%20CIBRC.pdf)) | every conventional dose, spray volume and PHI |
| TNAU Agritech crop protection pages | what the farmer sees, per pest |
| NPOP 8th edition, Annex 3 | which inputs organic farming permits |
| SPIC product pages; a reply in Parliament (Governance Now) | nutrient contents; 45 kg urea and 50 kg DAP bags |
| Organic Mandya jeevamrutham guide | jeevamrutham rate and recipe (a practitioners' guide) |
| Wikipedia's copy of the PPQS list, Rural Voice, TeamLease, Drishti IAS | the banned list |

ANGRAU's packages of practices for Andhra Pradesh, ICAR, NIPHM and PPQS
could not be reached from where this was built; TNAU's guides are for the
same peninsular conditions and are the base until they can be.

## What is rule

- Fertiliser from N-P2O5-K2O: phosphate as DAP (SSP for sugarcane and
  coconut), counting DAP's nitrogen across the season: a split where
  DAP brings more N than is due then (the basal dose) takes the surplus
  off the urea of the later splits; where DAP for all the phosphate
  would bring more N than the whole season gets (soybean), DAP goes as
  far as the N allows and the rest of the phosphate is SSP. The rest of
  the nitrogen as urea; potash as MOP; sulphur as gypsum (18 % S) with
  the first split. What the listed fertilisers deliver equals the
  recommended N, P2O5 and K2O for every crop and watering
  (`TestNutrientsAddUp`).
- Times: a split is on a calendar task key (`sow`, `nitrogen-1`,
  `treat-seed`, `plough`, `dig-pits`, `y1-fertilise-2`), on a day after
  sowing (`feed-das-45`), on the first day of a calendar stage
  (`feed-flowering`), or in a month of a tree's first year
  (`y2-feed-m04`). Keys stay the same when the sowing date moves.
- Rainfed falls back to irrigated and back, marked default. Splits are the
  irrigated ones unless the source gives rainfed ones.
- A crop with no bulk manure of its own gets vermicompost 5 t/ha on the
  organic route (marked default).
- Trees are planned for the first year from planting, per plant, plants
  counted from the library's spacing.
- Quantities of all fields add up per product, then round up to what is
  sold: whole packs, else whole kg, 100 g or 100 ml steps, whole litres,
  half tonnes, whole pieces. Before sowing is what is used on or before
  the sowing day.
- Curative products are never on the shopping list: they are under "only
  if you see …" with the signs, the threshold, the quantity one spray of
  the field takes, the water per acre, the PHI and the last day to spray
  (first harvest day less the PHI).
- A spray is scheduled only if it is preventive in the source; a synthetic
  one only with a known PHI, and never after the first harvest day less
  its PHI (else it is left out, with a note).
- The organic route never holds a synthetic product (checked on load and
  in tests); organic remedies are offered on both routes.

## What needs review

- Every number, by an agronomist (ANGRAU, a KVK) against Andhra Pradesh
  packages of practices and current CIB&RC labels: the CIB&RC list here is
  from 2019, and registrations change.
- The five kind defaults, and the rainfed fallbacks.
- Guava and coconut have fertiliser values only for grown trees: their
  first-year plan is manure only. Cocoa's first-year dose is "half" of the
  grown one, as the source says.
- MOP's bag size could not be confirmed on a source and is sold by the kg
  here; jeevamrutham comes from a practitioners' guide, not a university.
- Banana's biofertilisers at planting (arbuscular mycorrhiza 250 g,
  phosphate solubilising bacteria, Azospirillum and Trichoderma 50 g a
  plant, about 770 and 150 kg a hectare at 3086 plants) look high; they
  are what the TNAU Horticulture guide says (p. 8), re-checked on
  2026-11-15 and noted on each value. An agronomist should confirm them.
- The library has no citrus, so citrus has no values.
- Chilli thrips, mites and die-back have no organic remedy in the sources
  read: the plan says so instead of inventing one.

## Safety wording

Every plan starts with two notes: the values are starter values for an
agronomist to review, and the product label always wins (dose, mixing,
protective gear, waiting period). Every spray carries: not before rain, not
in wind, protective gear, follow the label. Fertiliser goes into moist soil,
not just before heavy rain. Curative options say to buy only when the
threshold is crossed.
