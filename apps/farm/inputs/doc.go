// Package inputs is the farm app's inputs and sprays: what to buy and when
// to apply it for a crop, an area, a method (organic or conventional) and a
// sowing date.
//
// It is rules and data, no model. The data (data/inputs.json) is per crop:
// seed rate, nutrition as N-P2O5-K2O per hectare (per plant for trees),
// split by crop stage, rainfed and irrigated; the manures, biofertilisers
// and seed treatments of the organic route; and the few most common pests
// and diseases with their signs, threshold, prevention, an organic remedy
// and a conventional one as an active ingredient with dose, spray volume
// and pre-harvest interval (PHI). Every value names the source it was read
// on. A crop without values of its own takes those of a crop of its kind,
// marked default; a default never carries a pesticide dose.
//
// [Book.Plan] turns the data into a shopping list in sold units (45 kg
// urea bags, 50 kg DAP bags, whole kilograms, 100 g packets...), split
// into what to buy before sowing and what later, a dated schedule keyed to
// the crop calendar's stages and task keys (package calendar), and a list
// of curative sprays to buy only if the pest or disease shows up. The
// organic route never lists a synthetic product, no pesticide on the
// banned or restricted list (data/banned.json) can be in the data, and no
// spray is scheduled closer to harvest than its PHI.
//
// Everything here is a starter value for an agronomist to review, and the
// product label always wins: every plan says so.
package inputs
