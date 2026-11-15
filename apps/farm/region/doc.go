// Package region is the farm app's region packs: the crop library, the
// inputs book, the cycle rules, storage and support prices and schemes
// (such as crop insurance) that hold where a plot is.
//
// A pack is the base set (the embedded crop library of package agro, the
// inputs book of package inputs, [DefaultRules], the starter storage
// values and MSPs of package prices, the national schemes of data/base.json)
// with small overlays laid on it, keyed by country, state and district:
// the most specific wins, field by field. An overlay may add, change or
// switch off a crop, set its seasons, local sowing windows and days, change
// a crop's input doses, add a product or a banned active, change the cycle
// rules, a storage class or a support price, and add or change a scheme.
// A place no overlay names gets the base. Every value an overlay sets keeps
// the reference it gave, and the pack records which layer each value came
// from ([Pack.Origin]): "base", "Andhra Pradesh" or "YSR Kadapa".
//
// Use [Load] (or [Open] with a directory) at startup: it reads the base,
// the embedded overlays and the directory's, builds the pack of every
// overlay and checks it with agro.Load and inputs.Load, so an overlay that
// names an unknown field, an unknown crop or a banned product fails there,
// with the file and the key in the error ([ErrInvalid]). [Set.For] then
// returns the pack for a place, from those built at startup: it never
// fails.
//
// The package is pure: no IO beyond reading the overlay files at Load, no
// package state. Design: apps/farm/docs/REGIONS.md.
package region
