---
id: T155
title: "The idea check: usual, new and unrealistic crop ideas with growing methods, prices and where to sell"
epic: E25
status: review
depends_on: [T134, T140, T154]
---

# T155: The idea check

## Context

The owner's ask of 2026-11-16: a product to try common ideas and
innovative ones, based on the market price and where things can be sold,
with realistic and unrealistic parts both, the unrealistic ones said to
be so, counting crops that can be grown by other techniques too.

Before this the app scored the open field only (drip apart), gave a score
but no verdict, and listed prices per mandi without saying where a crop
is sold.

## Scope

- **Package `apps/farm/ideas`.** `Service.For(ctx, farm, Ask{Search, Limit})`
  returns a `Report` of `Idea`s: the crop, a verdict (`realistic`,
  `stretch`, `unrealistic`), a band (`common`, `new`, `method`,
  `unrealistic`), the reason in a sentence, the open-field fit, the best
  growing method's fit and cost, a price and the ways to sell.
- **Growing methods as data** (`ideas/data/ideas.json`, `Method`): plastic
  mulch with drip, raised beds, low tunnel, shade net house, naturally
  ventilated polyhouse, hydroponics in a polyhouse. A method is a change to
  what the crop sees (`Method.Apply`: day and night temperature, light,
  rain kept off, water need, drainage, soil) and the crop is scored again
  by the recommender's own rules (`agro.Score`, split out of
  `agro.Recommend`). A structure (shade net, polyhouse, hydroponics)
  lists the crops grown under one (`Method.Crops`) and is offered for no
  other; none that needs water is offered on a rainfed plot.
- **Costs with a source.** Each method's cost is the MIDH Operational
  Guidelines 2025 cost norm (Annexure V), with the 50% assistance and the
  largest area assisted; `Method.CostOn` works it for the plot.
- **Verdicts.** Good in the open: realistic (common for a crop of the
  district or the season, new for a climate analog, an out-of-season crop
  or an experimental plant). Not good in the open but good under a method:
  a stretch. For a crop already good in the open, a method is offered as a stretch of
  its own when it opens eight or more weeks of off-season sowing (and the
  crop is not already sown well half the year), or lifts the score by 15,
  as growing without soil does on a soil the crop dislikes.
  Fair with no climate risk: a stretch with care. Otherwise unrealistic,
  with the gravest risk as the reason; a plant invasive in India always.
- **What is checked.** With no search: the region's library, the
  experimental tier's best for the plot, and twenty plants people ask about
  (apple, saffron, strawberry, blueberry, vanilla, coffee, ...), which are
  always answered wherever they land. With a search: every plant of either
  tier that goes by the name.
- **Price and selling.** The minimum support price, the mandi estimate
  and the newest prices of the district's markets where the price history
  has them (`prices.Service`); the ways to sell from the book's routes by
  crop kind, state and support price (support-price purchase, market yard,
  e-NAM, Rythu Bazaar, producer organisation, processor or contract).
- **Surfaces.** `GET /api/farms/{id}/ideas?search=&limit=`; the
  assistant's `check_ideas` tool and prompt lines; an *Ideas* tab on the
  plot page (a lazy chunk).

## Out of scope

- Yield, running cost, profit and payback an acre: the app has no
  reviewed figures, and the report says so (`ideas.money`). A follow-up
  needs cited yields and costs of cultivation per crop.
- Distance to markets and ranking markets by price net of transport.
- Vertical farming, fan-and-pad greenhouses, mushrooms, bees, livestock.

## Acceptance criteria

- [x] A book with an unknown field, nothing said to be assumed, a method
      that changes nothing, a cost without its source, an unknown skill or
      an empty route does not load (`TestBook`).
- [x] Each method changes exactly what it says and never the plot it was
      applied to; a structure fits tomato and not mango; the shade net
      costs Rs 710 a square metre, half assisted, on at most 2,500 square
      metres (`TestMethods`).
- [x] A plot at Kadapa gets ideas in the common, new and unrealistic
      bands, each at most the limit; a crop of the district leads; apple
      and saffron are unrealistic with the climate as the reason; a
      verdict is never realistic on a poor open field, and a method
      stretch always has a method that scores good (`TestIdeasForAPlot`).
- [x] No method that needs water is suggested on a rainfed plot, and the
      report says why (`TestRainfedPlotGetsNoStructure`).
- [x] A plant is found by a common name, a Telugu name and a botanical
      name; an invasive plant by name is unrealistic with no method; an
      unknown name answers that there is none (`TestSearchAnIdea`).
- [x] A crop with a support price and mandi history carries both and the
      district's markets best first; a vegetable in Andhra Pradesh has a
      Rythu Bazaar; an experimental plant has no price and no market yard
      (`TestPricesAndRoutes`).
- [x] The longest run of weeks good only under a method is its gain
      (`TestGain`).
- [x] On a soil that holds tomato back, hydroponics is offered as an idea
      of its own with its cost and a warning, asked by name it is one
      answer, and no structure is offered for a crop outside its list
      (`TestMethodLiftsACropTheSoilHoldsBack`, `TestMethods`).
- [x] A district the map service writes as "Anantapuramu District" takes
      Anantapur's overlay (`region` tests).
- [x] The route answers 200, 400 for a bad limit, 404 for no plot and 503
      when not configured (`TestIdeasRoute`); the tool only reads
      (`TestCheckIdeasTool`); the screen shows each band, the cost, the
      prices and where to sell, and checks one idea by name
      (`ideas.test.tsx`).

## Notes

- What a method does to heat, light and water is a starter value from
  general practice, not a measurement; the book and every report say so.
  Costs are the scheme's norms, not quotations.
- The selling routes' descriptions are general guidance written here, not
  taken from a document: review them with someone who sells in Andhra
  Pradesh.
- An experimental plant's verdict rests on FAO's ranges, which treat some
  crops grown here in one season (strawberry) as standing through the hot
  months: the report says to take it as a first look.
