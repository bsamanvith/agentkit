// Package osmtest has recorded OpenStreetMap data for tests: a plot of
// paddy by Kothapeta in the Godavari delta (Andhra Pradesh) and the map
// around it.
package osmtest

import (
	_ "embed"
	"fmt"

	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/osm"
)

// KothapetaMap is the map call's answer (map.json) for the box
// 81.8950,16.7010 to 81.9065,16.7120, recorded 2026-11-01.
// © OpenStreetMap contributors, ODbL.
//
//go:embed kothapeta-map.json
var KothapetaMap []byte

// KothapetaBox is the box KothapetaMap covers.
var KothapetaBox = osm.BBox{South: 16.7010, West: 81.8950, North: 16.7120, East: 81.9065}

// KothapetaPlot is a paddy field of about 1.6 acres (80 m by 88 m) in that
// box, traced from satellite imagery: corners clockwise from the
// north-west. A tertiary road runs about 290 m to its south; a drain
// about 150 m to its east.
var KothapetaPlot = geo.Ring{
	{Lat: 16.70677, Lng: 81.900394},
	{Lat: 16.70677, Lng: 81.901177},
	{Lat: 16.705948, Lng: 81.901177},
	{Lat: 16.705948, Lng: 81.900394},
}

// Kothapeta returns the features of KothapetaMap.
func Kothapeta() []osm.Feature {
	fs, err := osm.Parse(KothapetaMap)
	if err != nil {
		panic(fmt.Sprintf("osmtest: %v", err))
	}
	return fs
}

// Source is a static source of Kothapeta's features.
func Source() osm.Source { return osm.Static(Kothapeta()) }
