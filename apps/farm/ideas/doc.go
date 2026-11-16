// Package ideas checks farming ideas against a plot: the usual crops, new
// ones, and ones that need a growing method (a shade net, a polyhouse,
// mulch and drip), each with a plain verdict (realistic, a stretch,
// unrealistic), the reason, a price where the app has one, and the ways to
// sell it.
//
// The rules are the crop recommender's ([agro.Score]): a method is a change
// to the climate, the water and the soil the crop sees ([Method.Apply]), and
// the crop is scored again under it. What a method changes is a starter
// value (see data/ideas.json, "assumed"); what it costs is the horticulture
// mission's cost norm, with its source.
package ideas
