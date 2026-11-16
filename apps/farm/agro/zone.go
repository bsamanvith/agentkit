package agro

import (
	"slices"
	"strings"
)

// The climate zones ECOCROP names where a plant grows (Trewartha's):
// tropical wet and wet-and-dry, desert and steppe, the subtropical three,
// temperate, boreal and polar.
const (
	ZoneTropicalWet    = "Ar"
	ZoneTropicalWetDry = "Aw"
	ZoneDesert         = "Bw"
	ZoneSteppe         = "Bs"
	ZoneSubHumid       = "Cf"
	ZoneSubDrySummer   = "Cs"
	ZoneSubDryWinter   = "Cw"
	ZoneTempOceanic    = "Do"
	ZoneTempContinent  = "Dc"
	ZoneBoreal         = "E"
	ZonePolar          = "F"
)

var zoneNames = map[string]string{
	ZoneTropicalWet: "tropical wet", ZoneTropicalWetDry: "tropical wet and dry", ZoneDesert: "desert", ZoneSteppe: "semi-arid",
	ZoneSubHumid: "subtropical humid", ZoneSubDrySummer: "subtropical with dry summers", ZoneSubDryWinter: "subtropical with dry winters",
	ZoneTempOceanic: "temperate oceanic", ZoneTempContinent: "temperate continental", "Df": "temperate with humid winters",
	"Dw": "temperate with dry winters", ZoneBoreal: "boreal", ZonePolar: "polar",
}

// ZoneName returns a zone's name in words: "semi-arid" for "Bs".
func ZoneName(z string) string { return zoneNames[z] }

// Zone returns the climate's zone by Trewartha's rules, and whether it is
// hot all year (no month's mean under 18 °C): a dry zone runs from the
// tropics to the cold steppes, and that tells them apart.
func (c Climate) Zone() (zone string, hot bool) {
	var sum, rain, winterRain float64
	warm, coldest, warmest := 0, 0, 0
	for i, m := range c.Months {
		sum += m.Temp
		rain += m.RainMM
		if m.Temp >= 10 {
			warm++
		}
		if m.Temp < c.Months[coldest].Temp {
			coldest = i
		}
		if m.Temp > c.Months[warmest].Temp {
			warmest = i
		}
	}
	north := c.Point.Lat >= 0
	summer := func(i int) bool { return (i >= 3 && i <= 8) == north } // April to September in the north
	driestSummer, wettestSummer, driestWinter, wettestWinter := -1.0, 0.0, -1.0, 0.0
	dry := 0
	for i, m := range c.Months {
		if m.RainMM < 60 {
			dry++
		}
		if summer(i) {
			wettestSummer = max(wettestSummer, m.RainMM)
			if driestSummer < 0 || m.RainMM < driestSummer {
				driestSummer = m.RainMM
			}
			continue
		}
		winterRain += m.RainMM
		wettestWinter = max(wettestWinter, m.RainMM)
		if driestWinter < 0 || m.RainMM < driestWinter {
			driestWinter = m.RainMM
		}
	}
	hot = c.Months[coldest].Temp >= 18
	share := 0.0
	if rain > 0 {
		share = 100 * winterRain / rain
	}
	// Trewartha's dryness line, in centimetres of rain a year.
	line := 2.3*sum/12 - 0.64*share + 41
	switch cm := rain / 10; {
	case warm == 0:
		return ZonePolar, false
	case cm < line/2:
		return ZoneDesert, hot
	case cm < line:
		return ZoneSteppe, hot
	case hot && dry <= 2:
		return ZoneTropicalWet, true
	case hot:
		return ZoneTropicalWetDry, true
	case warm >= 8 && driestSummer < 30 && driestSummer < wettestWinter/3:
		return ZoneSubDrySummer, false
	case warm >= 8 && wettestSummer >= 10*driestWinter:
		return ZoneSubDryWinter, false
	case warm >= 8:
		return ZoneSubHumid, false
	case warm >= 4 && c.Months[coldest].Temp > 0:
		return ZoneTempOceanic, false
	case warm >= 4:
		return ZoneTempContinent, false
	}
	return ZoneBoreal, false
}

// zoneFit reports whether a plant that grows in zones is known from the
// climate's zone. A plant with no zones fits. In a dry zone that is hot
// all year, a plant of the temperate zones that names no tropical one does
// not fit: the cold steppe it knows has the winter it needs.
func zoneFit(zones []string, zone string, hot bool) bool {
	if len(zones) == 0 {
		return true
	}
	has := func(prefix string) bool {
		return slices.ContainsFunc(zones, func(z string) bool { return strings.HasPrefix(z, prefix) })
	}
	if strings.HasPrefix(zone, "D") {
		return has("D")
	}
	if !slices.Contains(zones, zone) {
		return false
	}
	if hot && strings.HasPrefix(zone, "B") && !has("A") && (has("D") || has("E")) {
		return false
	}
	return true
}
