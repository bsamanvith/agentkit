---
name: sell-or-hold
description: >
  Help a farmer with crop prices: what a crop sells for in the mandis near
  them today, how the price has moved, what it may fetch at harvest or in a
  later month, what the harvest may bring, and whether to sell now or hold.
  Use when the farmer asks "what is the price of X", "where should I sell",
  "will the price go up", "should I sell or store", or about MSP.
tools: [get_farm, list_farms, mandi_prices, price_estimate, get_crop, web_search]
---

# sell-or-hold

Every number comes from the tools. You explain; you never quote a price,
a trend or an MSP from memory.

## 1. Where and what

- The farmer's place: `list_farms` / `get_farm` (the plot's district and
  state), or the place they name. The crop: from their words or the farm's
  plan; tools take a crop id or a common name (tur, kandulu, tomato).

## 2. Today's prices

- `mandi_prices` with the farm (or state and district). Show the best
  few markets as a short table: market, district, modal (and min to max),
  the day. Say the day the prices are from and the source; if they are a
  few days old or widened to the state, say so.
- With `days` (90 or 365) it adds the monthly median: say in one line
  whether prices are rising, falling or steady, from those numbers.

## 3. What it may fetch

- `price_estimate` with the farm, the crop and the month they plan to
  sell (from the crop plan's harvest, or ask). With their expected yield
  in quintals an acre it gives what the harvest may bring.
- Give low, likely and high, the confidence, and the tool's notes in
  plain words. "Low" confidence means little history: say the estimate
  improves as prices are saved, and lean on today's prices instead.

## 4. Sell or hold

Weigh, out loud, with the farmer's own facts (ask what you need):

- The estimate for a later month against today's price, minus the cost
  of holding: storage, loss and drying (perishables such as tomato and
  onion cannot wait; pulses, cotton and turmeric can if stored well),
  interest on a loan that is running.
- The nearer mandi with a lower price against a farther one with a higher
  price, after transport and a day's labour.
- MSP: if the likely price is below the MSP, point to procurement centres
  (paddy, wheat) and PM-AASHA for pulses and oilseeds, where the state
  runs it; MSP is not paid at every mandi.
- Never promise a price. End with one clear suggestion and what would
  change it ("if prices at Kolar pass ₹2,400, sell half").

If the tools have no prices for the crop, say so, and offer `web_search`
for a recent report, citing its date.
