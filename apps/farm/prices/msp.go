package prices

import (
	"slices"
	"strings"
)

// SupportPrice is a minimum support price: what the Government of India
// procures a crop at in a marketing season, in rupees per quintal. It is a
// floor only where and when procurement runs (paddy and wheat widely,
// pulses and oilseeds through PM-AASHA in some states), not a price every
// mandi pays.
type SupportPrice struct {
	Commodity string  `json:"commodity"`
	Variety   string  `json:"variety,omitempty"`
	Season    string  `json:"season"` // the marketing season, "Kharif 2026-27"
	Price     float64 `json:"price"`
	Source    string  `json:"source"`
}

const (
	kharif2627 = "Kharif 2026-27"
	rabi2728   = "Rabi 2027-28"
	// Cabinet decisions of 13 May 2026 (kharif) and 30 September 2026
	// (rabi), as reported by the Press Information Bureau.
	mspKharifSource = "Cabinet decision, 13 May 2026 (PIB): MSP for kharif crops, marketing season 2026-27"
	mspRabiSource   = "Cabinet decision, 30 September 2026 (PIB): MSP for rabi crops, marketing season 2027-28"
)

// supportPrices are the MSPs in force for the crops the app plans. Update
// them when the Cabinet announces a season's (kharif in May or June, rabi
// in September or October).
var supportPrices = []SupportPrice{
	{"Paddy(Dhan)(Common)", "Common", kharif2627, 2441, mspKharifSource},
	{"Paddy(Dhan)(Common)", "Grade A", kharif2627, 2461, mspKharifSource},
	{"Jowar(Sorghum)", "Hybrid", kharif2627, 4023, mspKharifSource},
	{"Jowar(Sorghum)", "Maldandi", kharif2627, 4073, mspKharifSource},
	{"Bajra(Pearl Millet/Cumbu)", "", kharif2627, 2900, mspKharifSource},
	{"Ragi (Finger Millet)", "", kharif2627, 5205, mspKharifSource},
	{"Maize", "", kharif2627, 2410, mspKharifSource},
	{"Arhar (Tur/Red Gram)(Whole)", "", kharif2627, 8450, mspKharifSource},
	{"Green Gram (Moong)(Whole)", "", kharif2627, 8780, mspKharifSource},
	{"Black Gram (Urd Beans)(Whole)", "", kharif2627, 8200, mspKharifSource},
	{"Groundnut", "", kharif2627, 7517, mspKharifSource},
	{"Sunflower", "Seed", kharif2627, 8343, mspKharifSource},
	{"Soyabean", "Yellow", kharif2627, 5708, mspKharifSource},
	{"Sesamum(Sesame,Gingelly,Til)", "", kharif2627, 10346, mspKharifSource},
	{"Niger Seed (Ramtil)", "", kharif2627, 10052, mspKharifSource},
	{"Cotton", "Medium staple", kharif2627, 8267, mspKharifSource},
	{"Cotton", "Long staple", kharif2627, 8667, mspKharifSource},
	{"Wheat", "", rabi2728, 2610, mspRabiSource},
	{"Barley (Jau)", "", rabi2728, 2286, mspRabiSource},
	{"Bengal Gram(Gram)(Whole)", "", rabi2728, 5958, mspRabiSource},
	{"Lentil (Masur)(Whole)", "", rabi2728, 7390, mspRabiSource},
	{"Mustard", "Rapeseed and mustard", rabi2728, 6613, mspRabiSource},
	{"Safflower", "", rabi2728, 7215, mspRabiSource},
}

// MSP returns the support prices for a commodity (a crop id or any name
// [Commodity] knows), lowest first; none for a crop without one
// (vegetables, fruit, spices).
func MSP(commodity string) []SupportPrice {
	c := Commodity(commodity)
	var out []SupportPrice
	for _, p := range supportPrices {
		if strings.EqualFold(p.Commodity, c) {
			out = append(out, p)
		}
	}
	return out
}

// SupportPrices returns every support price the app knows.
func SupportPrices() []SupportPrice { return slices.Clone(supportPrices) }
