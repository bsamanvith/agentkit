// Package geo measures and checks small polygons on the earth's surface:
// farm plots from a few square metres to a few thousand hectares. It has no
// dependencies.
//
// Points are WGS84 latitude and longitude in decimal degrees, as phones and
// maps give them. Areas come from an equal-area projection of the WGS84
// ellipsoid (the ellipsoidal sinusoidal) centred on the polygon, so they
// stay within a fraction of a percent of the true ellipsoidal area for
// anything a farm can be; lengths come from the local radii of curvature.
// Neither is meant for polygons spanning hundreds of kilometres, the poles
// or the antimeridian.
//
// A ring is a polygon's boundary: its corners in order, either way round,
// with or without the first corner repeated at the end.
package geo
