package osm

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"

	"github.com/bsamanvith/agentkit/apps/farm/geo"
)

// Classify returns the kind and class of an element with these tags, and
// whether it is one this package reads. It looks at the tags in a fixed
// order, so a canal that is also tagged as an area is a canal.
func Classify(tags map[string]string) (Kind, string, bool) {
	if v := tags["highway"]; v != "" {
		switch v {
		case "motorway", "trunk", "primary", "secondary", "tertiary", "unclassified", "residential",
			"service", "living_street", "road", "motorway_link", "trunk_link", "primary_link", "secondary_link", "tertiary_link":
			return Road, v, true
		case "track":
			return Track, v, true
		case "path", "footway", "cycleway", "bridleway", "steps", "pedestrian":
			return Path, v, true
		}
	}
	if v := tags["railway"]; v == "rail" || v == "narrow_gauge" || v == "light_rail" {
		return Rail, v, true
	}
	switch v := tags["waterway"]; v {
	case "river", "riverbank":
		return River, v, true
	case "stream", "brook":
		return Stream, v, true
	case "canal":
		return Canal, v, true
	case "drain", "ditch":
		return Drain, v, true
	}
	if tags["natural"] == "water" || tags["landuse"] == "reservoir" || tags["landuse"] == "basin" || tags["water"] != "" {
		class := tags["water"]
		if class == "" {
			class = "water"
			if tags["landuse"] == "reservoir" || tags["landuse"] == "basin" {
				class = tags["landuse"]
			}
		}
		if class == "river" || class == "canal" {
			return map[string]Kind{"river": River, "canal": Canal}[class], class, true
		}
		return Water, class, true
	}
	if tags["natural"] == "wetland" {
		return Wetland, "wetland", true
	}
	if v := tags["building"]; v != "" && v != "no" {
		return Building, v, true
	}
	if v := tags["power"]; v == "line" || v == "minor_line" {
		return Power, v, true
	}
	if v := tags["man_made"]; v == "water_well" || v == "water_tower" {
		return Well, v, true
	}
	if v := tags["amenity"]; slices.Contains([]string{"school", "college", "hospital", "clinic", "place_of_worship", "marketplace", "bus_station", "post_office", "bank", "fuel", "townhall"}, v) {
		return Facility, v, true
	}
	if v := tags["place"]; slices.Contains([]string{"village", "hamlet", "town", "city", "suburb", "isolated_dwelling"}, v) {
		return Settlement, v, true
	}
	switch v := tags["landuse"]; v {
	case "residential", "village_green":
		return Settlement, v, true
	case "farmland", "orchard", "plantation", "meadow", "farmyard", "greenhouse_horticulture":
		return Farmland, v, true
	case "forest":
		return Trees, v, true
	case "industrial", "commercial", "retail", "quarry":
		return Industry, v, true
	}
	if v := tags["natural"]; v == "wood" || v == "scrub" {
		return Trees, v, true
	}
	return "", "", false
}

// element is an OpenStreetMap element as both Overpass ("out geom") and
// the map call (map.json) write it.
type element struct {
	Type     string            `json:"type"`
	ID       int64             `json:"id"`
	Lat      *float64          `json:"lat"`
	Lon      *float64          `json:"lon"`
	Tags     map[string]string `json:"tags"`
	Nodes    []int64           `json:"nodes"`
	Geometry []latLon          `json:"geometry"`
	Members  []member          `json:"members"`
}

type latLon struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

type member struct {
	Type     string   `json:"type"`
	Ref      int64    `json:"ref"`
	Role     string   `json:"role"`
	Geometry []latLon `json:"geometry"`
}

type document struct {
	Elements []element `json:"elements"`
	Remark   string    `json:"remark"`
}

// overpassFailure is how Overpass reports a query it gave up on: in the
// remark of a 200.
var overpassFailure = regexp.MustCompile(`(?i)runtime error|timed out|out of memory|too many requests`)

// Parse reads an Overpass "out geom" answer or a map.json answer into
// features: the tagged ones Classify reads, with their geometry; ways and
// relations missing geometry (cut at the box's edge) are left out.
func Parse(data []byte) ([]Feature, error) {
	var doc document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if doc.Remark != "" && overpassFailure.MatchString(doc.Remark) {
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, doc.Remark)
	}
	nodes := map[int64]geo.Point{}
	ways := map[int64][]geo.Point{}
	for _, e := range doc.Elements {
		if e.Type == "node" && e.Lat != nil && e.Lon != nil {
			nodes[e.ID] = geo.Point{Lat: *e.Lat, Lng: *e.Lon}
		}
	}
	for _, e := range doc.Elements {
		if e.Type == "way" {
			if ps := wayPoints(e.Geometry, e.Nodes, nodes); ps != nil {
				ways[e.ID] = ps
			}
		}
	}
	var out []Feature
	for _, e := range doc.Elements {
		kind, class, ok := Classify(e.Tags)
		if !ok {
			continue
		}
		f := Feature{Kind: kind, Class: class, Name: e.Tags["name"], Ref: e.Tags["ref"]}
		if f.Name == "" {
			f.Name = e.Tags["name:en"]
		}
		switch e.Type {
		case "node":
			p, ok := nodes[e.ID]
			if !ok {
				continue
			}
			f.ID, f.Points = "node/"+strconv.FormatInt(e.ID, 10), []geo.Point{p}
			out = append(out, f)
		case "way":
			ps, ok := ways[e.ID]
			if !ok {
				continue
			}
			f.ID, f.Points = "way/"+strconv.FormatInt(e.ID, 10), ps
			f.Closed = closedArea(kind, ps)
			if f.Closed {
				f.Points = ps[:len(ps)-1]
			}
			out = append(out, f)
		case "relation":
			for _, m := range e.Members {
				if m.Type != "way" || (m.Role != "outer" && m.Role != "") {
					continue
				}
				ps := wayPoints(m.Geometry, nil, nil)
				if ps == nil {
					ps = ways[m.Ref]
				}
				if len(ps) < 2 {
					continue
				}
				g := f
				g.ID, g.Points = "relation/"+strconv.FormatInt(e.ID, 10), ps
				g.Closed = closedArea(kind, ps)
				if g.Closed {
					g.Points = ps[:len(ps)-1]
				}
				out = append(out, g)
			}
		}
	}
	return out, nil
}

func wayPoints(geom []latLon, refs []int64, nodes map[int64]geo.Point) []geo.Point {
	if len(geom) > 0 {
		ps := make([]geo.Point, len(geom))
		for i, g := range geom {
			ps[i] = geo.Point{Lat: g.Lat, Lng: g.Lon}
		}
		return ps
	}
	if len(refs) == 0 {
		return nil
	}
	ps := make([]geo.Point, 0, len(refs))
	for _, r := range refs {
		p, ok := nodes[r]
		if !ok {
			return nil // cut at the box's edge
		}
		ps = append(ps, p)
	}
	return ps
}

// closedArea reports whether a way's points bound an area: it closes on
// itself and its kind is an area kind (a closed road is a loop, not an
// area).
func closedArea(k Kind, ps []geo.Point) bool {
	if len(ps) < 4 || ps[0] != ps[len(ps)-1] {
		return false
	}
	switch k {
	case Road, Track, Path, Rail, Stream, Canal, Drain, Power:
		return false
	}
	return true
}
