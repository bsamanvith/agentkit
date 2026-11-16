// Package schemes says which government schemes (crop insurance first)
// apply to a plot's crop cycle, from the schemes of its region pack
// (region.Pack.Schemes): which cover the cycle's crops, the last day to
// enrol (or sow) this season and the days left, and how soon a loss must
// be reported.
//
// It is rules over the pack's data and today's date, no model, and it
// never adds a fact the data does not hold: a scheme whose source names
// no crops "may cover" every crop (the state notifies them each season);
// a notice for an earlier season gives last year's dates, never a live
// deadline. Every item carries its source, the layer it came from and the
// day the source was read, and every answer tells the farmer to confirm
// at the bank, the Rythu Seva Kendra or the insurer.
//
// The season roadmap's insurance tasks, the cut-off alert and the
// assistant's get_schemes all read [For]. See apps/farm/docs/REGIONS.md.
package schemes
