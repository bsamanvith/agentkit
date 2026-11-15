package prices

import "strings"

// Storage is what holding a harvest costs, by commodity class. The values
// are starter values with their sources in [Storage.Source]: the farmer's
// own store (a dry, aired room, bags on pallets) or a warehouse.
type Storage struct {
	// Class names the commodity class: "pulses", "oilseeds", "cereals",
	// "cotton", "spices", "onion" or "perishable".
	Class string `json:"class"`
	// LossPctPerMonth is the weight and quality lost a month in store, in
	// percent of what went in.
	LossPctPerMonth float64 `json:"loss_pct_per_month"`
	// CostPerQMonth is rent, bags, fumigation and insurance, in rupees a
	// quintal a month.
	CostPerQMonth float64 `json:"cost_per_q_month"`
	// InterestPctPerMonth is the money's cost while it is held: the
	// interest on a crop loan still running, or what the money would earn.
	InterestPctPerMonth float64 `json:"interest_pct_per_month"`
	// MaxMonths is how long it keeps; 0 for a crop that cannot wait.
	MaxMonths int    `json:"max_months"`
	Source    string `json:"source"`
}

// Storable reports whether the crop can be held at all.
func (s Storage) Storable() bool { return s.MaxMonths > 0 }

// The sources of the storage starter values (apps/farm/docs/PRICES.md,
// "Evidence").
const (
	storageLossSource = "post-harvest loss over the whole chain (NABCONS 2022, as tabled in Indian Farming 75(4), 2025): pigeonpea 5.65%, groundnut 5.73%, cottonseed 2.87%, maize 3.89%; a month in a dry store is taken as a small share of that"
	storageCostSource = "Central Warehousing Corporation tariff circular of 16 June 2020: bag-basis tariff for grain and pulses, insurance 5 paise per ₹100 a month, a 30% rebate for farmers storing their own crop; ₹15 a quintal a month is a starter, not a quoted rate"
	onionLossSource   = "Mane et al. 2020 (J. Pharmacogn. Phytochem. 9(6):1359), onion in ambient storage at Akola: mean weight loss 14.3% at 60 days and 21.3% at 90, rotting 6.5% and 12.6% more"
	interestSource    = "starter: 1% a month, about a crop loan's cost; set your own"
)

var storageClasses = map[string]Storage{
	"pulses":     {"pulses", 0.5, 15, 1, 8, storageLossSource + "; " + storageCostSource + "; interest " + interestSource},
	"oilseeds":   {"oilseeds", 0.5, 15, 1, 6, storageLossSource + "; " + storageCostSource + "; interest " + interestSource},
	"cereals":    {"cereals", 0.4, 12, 1, 8, storageLossSource + "; " + storageCostSource + "; interest " + interestSource},
	"cotton":     {"cotton", 0.3, 15, 1, 6, storageLossSource + "; " + storageCostSource + "; interest " + interestSource},
	"spices":     {"spices", 0.5, 20, 1, 8, storageLossSource + "; " + storageCostSource + "; interest " + interestSource},
	"onion":      {"onion", 8, 10, 1, 4, onionLossSource + "; interest " + interestSource},
	"perishable": {"perishable", 0, 0, 0, 0, "sold within days of picking: tomato, leafy and most vegetables and fruit"},
}

// commodityClass maps Agmarknet's names (folded prefixes) to a storage
// class.
var commodityClass = []struct{ prefix, class string }{
	{"arhar", "pulses"},
	{"bengalgram", "pulses"},
	{"greengram", "pulses"},
	{"blackgram", "pulses"},
	{"lentil", "pulses"},
	{"groundnut", "oilseeds"},
	{"soyabean", "oilseeds"},
	{"mustard", "oilseeds"},
	{"sunflower", "oilseeds"},
	{"sesamum", "oilseeds"},
	{"safflower", "oilseeds"},
	{"paddy", "cereals"},
	{"wheat", "cereals"},
	{"maize", "cereals"},
	{"jowar", "cereals"},
	{"bajra", "cereals"},
	{"ragi", "cereals"},
	{"foxtail", "cereals"},
	{"cotton", "cotton"},
	{"drychillies", "spices"},
	{"turmeric", "spices"},
	{"onion", "onion"},
}

// StorageFor returns the storage starter values of a commodity (a crop id
// or any name [Commodity] knows); a commodity it does not know is
// perishable, so no hold is ever suggested for it.
func StorageFor(commodity string) Storage {
	f := Fold(Commodity(commodity))
	for _, c := range commodityClass {
		if strings.HasPrefix(f, c.prefix) {
			return storageClasses[c.class]
		}
	}
	return storageClasses["perishable"]
}
