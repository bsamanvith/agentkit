package prices

import "strings"

// commodities maps the app's crop ids and the names farmers use to
// Agmarknet's commodity names, the names every source here reports.
// The first name of each line is Agmarknet's.
var commodities = [][]string{
	{"Paddy(Dhan)(Common)", "rice", "paddy", "dhan", "vari", "nellu", "batta"},
	{"Wheat", "wheat", "gehun", "godhuma"},
	{"Maize", "maize", "corn", "makka", "mokkajonna"},
	{"Jowar(Sorghum)", "sorghum", "jowar", "jonna", "cholam"},
	{"Bajra(Pearl Millet/Cumbu)", "pearl_millet", "bajra", "sajja", "cumbu", "kambu"},
	{"Ragi (Finger Millet)", "finger_millet", "ragi", "nachni"},
	{"Foxtail Millet(Navane)", "foxtail_millet", "navane", "korra", "thinai"},
	{"Bengal Gram(Gram)(Whole)", "chickpea", "gram", "chana", "bengal_gram", "senagalu"},
	{"Arhar (Tur/Red Gram)(Whole)", "pigeon_pea", "tur", "toor", "arhar", "red_gram", "kandulu", "thogari"},
	{"Green Gram (Moong)(Whole)", "green_gram", "moong", "pesalu", "hesaru"},
	{"Black Gram (Urd Beans)(Whole)", "black_gram", "urad", "minumulu", "uddu"},
	{"Groundnut", "groundnut", "peanut", "verusenaga", "kadalekai", "moongphali"},
	{"Soyabean", "soybean", "soyabean", "soya"},
	{"Mustard", "mustard", "sarson", "rapeseed"},
	{"Sunflower", "sunflower", "suryakanthi"},
	{"Sesamum(Sesame,Gingelly,Til)", "sesame", "til", "gingelly", "nuvvulu", "ellu"},
	{"Cotton", "cotton", "kapas", "patti", "pathi"},
	{"Sugarcane", "sugarcane", "cheruku", "kabbu"},
	{"Turmeric", "turmeric", "haldi", "pasupu", "arishina"},
	{"Dry Chillies", "chilli", "chillies", "red_chilli", "mirchi", "mirapakaya"},
	{"Green Chilli", "green_chilli"},
	{"Tomato", "tomato", "tamata", "takkali"},
	{"Onion", "onion", "ullipaya", "eerulli", "pyaz"},
	{"Brinjal", "brinjal", "eggplant", "vankaya", "baingan"},
	{"Bhindi(Ladies Finger)", "okra", "bhindi", "bendakaya", "ladies_finger"},
	{"Potato", "potato", "aloo", "bangaladumpa"},
	{"Sweet Potato", "sweet_potato", "chilagada"},
	{"Water Melon", "watermelon", "water_melon", "puchakaya"},
	{"Banana", "banana", "arati", "kela"},
	{"Mango", "mango", "mamidi", "aam"},
	{"Papaya", "papaya", "boppayi"},
	{"Guava", "guava", "jama", "amrood"},
	{"Coconut", "coconut", "kobbari", "nariyal"},
	{"Drumstick", "moringa", "drumstick", "munagakaya"},
	{"Ginger(Green)", "ginger", "allam"},
}

var commodityIndex = func() map[string]string {
	m := map[string]string{}
	for _, line := range commodities {
		for _, n := range line {
			m[Fold(n)] = line[0]
		}
	}
	return m
}()

// Commodity returns Agmarknet's name for a crop id or a common name
// ("pigeon_pea", "tur", "Arhar"), or the name as given when it knows no
// other.
func Commodity(name string) string {
	name = strings.TrimSpace(name)
	if c, ok := commodityIndex[Fold(name)]; ok {
		return c
	}
	return name
}

// SameCommodity reports whether a and b name the same commodity: the same
// Agmarknet name, or one Agmarknet name that starts with the other
// ("Cotton" and "Cotton(Unginned)").
func SameCommodity(a, b string) bool {
	fa, fb := Fold(Commodity(a)), Fold(Commodity(b))
	if fa == "" || fb == "" {
		return fa == fb
	}
	return fa == fb || strings.HasPrefix(fb, fa) || strings.HasPrefix(fa, fb)
}

// CropCommodity returns Agmarknet's name for one of the app's crop ids,
// and whether it knows one.
func CropCommodity(cropID string) (string, bool) {
	c, ok := commodityIndex[Fold(cropID)]
	return c, ok
}
