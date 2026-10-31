// Package farm is the farm app's domain: a farmer's plots (a [Farm]: a
// name, the size the farmer declares and a boundary), how a boundary is
// verified ([Verifier], [Report]) and where plots are kept ([Store]).
//
// [Service] ties them together: every plot it saves was verified first, and
// one whose boundary is invalid is refused with its report
// ([*InvalidError]). The geometry is in package geo; the store backends,
// such as farm/sqlite, are subpackages, and farm/farmtest has a fake and
// the conformance suite every backend passes.
package farm
