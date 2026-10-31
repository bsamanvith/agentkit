// Package agro is the farm app's crop planning rules: a library of Indian
// soils and of crops (with climate analogs from abroad), a recommender that
// scores every crop for every sowing date of the coming year at a plot, and
// a checker for a season's plan of zones on the plot.
//
// Everything here is deterministic: the same inputs give the same answer,
// and every score carries its reasons and risks as notes with stable ids.
// Models explain the results; they never make them.
//
// The recommender simulates a crop's season day by day over the plot's
// climate normals (twelve months, from a [ClimateSource] such as NASA
// POWER): temperature against the crop's best and survivable ranges, frost,
// rain (four fifths of it counts) plus the water a soil held at sowing plus
// what the plot's sources can irrigate, against the crop's need; then the
// soil's pH, texture, salt and drainage. A rainfed sowing with little rain
// in its first three weeks is marked down: farmers sow after the rains set
// in. Scores run from 0 to 100; [Good] and above make a sowing window.
//
// [Layout] cuts a plot into strips, one per crop, by area share, and
// [CheckPlan] checks a plan: zones inside the plot and apart, spacing,
// sowing window, water, soil, rotation, intercrops and shade.
package agro
