package region

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/inputs"
	"github.com/bsamanvith/agentkit/apps/farm/prices"
)

//go:embed data/base.json data/overlays/*.json
var data embed.FS

// Set is the packs of every region the overlays name, built and checked
// at [Load]. It is read-only and safe for concurrent use.
type Set struct {
	base     *Pack
	overlays []*overlay
	packs    map[string]*Pack // by overlay key
}

// overlay is one overlay file.
type overlay struct {
	file string
	key  string
	name string
	f    overlayFile
}

func (o *overlay) level() int {
	switch {
	case o.f.Region.District != "":
		return 3
	case o.f.Region.State != "":
		return 2
	}
	return 1
}

type overlayFile struct {
	Region struct {
		Country  string   `json:"country"`
		State    string   `json:"state,omitempty"`
		District string   `json:"district,omitempty"`
		Name     string   `json:"name,omitempty"`
		Aliases  []string `json:"aliases,omitempty"`
	} `json:"region"`
	Note    string                     `json:"note,omitempty"`
	Sources []Source                   `json:"sources,omitempty"`
	Crops   map[string]json.RawMessage `json:"crops,omitempty"`
	Inputs  *struct {
		Crops    map[string]json.RawMessage `json:"crops,omitempty"`
		Products map[string]json.RawMessage `json:"products,omitempty"`
		Banned   []json.RawMessage          `json:"banned,omitempty"`
	} `json:"inputs,omitempty"`
	Rules   json.RawMessage            `json:"rules,omitempty"`
	Storage map[string]json.RawMessage `json:"storage,omitempty"`
	MSP     []struct {
		Commodity string  `json:"commodity"`
		Variety   string  `json:"variety,omitempty"`
		Season    string  `json:"season"`
		Price     float64 `json:"price"`
		Ref       Ref     `json:"ref"`
	} `json:"msp,omitempty"`
	Schemes map[string]json.RawMessage `json:"schemes,omitempty"`
}

type baseFile struct {
	Note    string   `json:"note"`
	Sources []Source `json:"sources"`
	Schemes []Scheme `json:"schemes"`
}

// Open is [Load] with the overlays of a directory; an empty dir is none.
func Open(dir string) (*Set, error) {
	if dir == "" {
		return Load(nil)
	}
	if st, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("%w: overlays: %w", ErrInvalid, err)
	} else if !st.IsDir() {
		return nil, fmt.Errorf("%w: overlays: %s is not a directory", ErrInvalid, dir)
	}
	return Load(os.DirFS(dir))
}

// Load reads the base, the embedded overlays and the *.json overlay files
// of fsys (nil for none), and builds and checks the pack of every region
// they name. Any error wraps [ErrInvalid] and names the file and the key.
func Load(fsys fs.FS) (*Set, error) {
	var bf baseFile
	raw, err := data.ReadFile("data/base.json")
	if err != nil {
		return nil, err
	}
	if err := strict(raw, &bf); err != nil {
		return nil, fmt.Errorf("%w: data/base.json: %w", ErrInvalid, err)
	}
	s := &Set{packs: map[string]*Pack{}}
	if s.base, err = basePack(bf); err != nil {
		return nil, err
	}
	var files []struct {
		name string
		fs   fs.FS
		at   string
	}
	emb, _ := fs.Sub(data, "data/overlays")
	add := func(fsys fs.FS, label string) error {
		return fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && path.Ext(p) == ".json" {
				files = append(files, struct {
					name string
					fs   fs.FS
					at   string
				}{label + p, fsys, p})
			}
			return nil
		})
	}
	if err := add(emb, "embedded:"); err != nil {
		return nil, err
	}
	if fsys != nil {
		if err := add(fsys, ""); err != nil {
			return nil, fmt.Errorf("%w: overlays: %w", ErrInvalid, err)
		}
	}
	byKey := map[string]*overlay{}
	for _, f := range files {
		b, err := fs.ReadFile(f.fs, f.at)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrInvalid, f.name, err)
		}
		o := &overlay{file: f.name}
		if err := strict(b, &o.f); err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrInvalid, f.name, err)
		}
		r := &o.f.Region
		if len(r.Country) != 2 || (r.District != "" && r.State == "") {
			return nil, fmt.Errorf("%w: %s: region: want a two-letter country, and a state with a district", ErrInvalid, f.name)
		}
		r.Country = strings.ToUpper(r.Country)
		o.key = regionKey(r.Country, r.State, r.District)
		o.name = cmpOr(r.Name, r.District, r.State, r.Country)
		if other, dup := byKey[o.key]; dup {
			return nil, fmt.Errorf("%w: %s: region %s is also in %s", ErrInvalid, f.name, o.key, other.file)
		}
		byKey[o.key] = o
		s.overlays = append(s.overlays, o)
	}
	slices.SortFunc(s.overlays, func(a, b *overlay) int { return strings.Compare(a.key, b.key) })
	for _, o := range s.overlays {
		chain := s.chain(o)
		p, err := build(bf, chain)
		if err != nil {
			return nil, err
		}
		s.packs[o.key] = p
	}
	return s, nil
}

// chain is the overlays laid for o's region, least specific first.
func (s *Set) chain(o *overlay) []*overlay {
	r := o.f.Region
	var out []*overlay
	for _, k := range []string{regionKey(r.Country, "", ""), regionKey(r.Country, r.State, ""), o.key} {
		for _, x := range s.overlays {
			if x.key == k && !slices.Contains(out, x) {
				out = append(out, x)
			}
		}
	}
	return out
}

// Base returns the base pack.
func (s *Set) Base() *Pack { return s.base }

// Packs returns the pack of every region an overlay names, by key.
func (s *Set) Packs() []*Pack {
	out := []*Pack{s.base}
	for _, o := range s.overlays {
		out = append(out, s.packs[o.key])
	}
	return out
}

// For returns the pack for a place: the most specific overlay whose
// country, state and district (or one of its aliases) match, else the
// base. Names match whatever their case and punctuation.
func (s *Set) For(p Place) *Pack {
	if s == nil {
		return nil
	}
	best := -1
	var pick *overlay
	for _, o := range s.overlays {
		if o.matches(s, p) && o.level() > best {
			best, pick = o.level(), o
		}
	}
	if pick == nil {
		return s.base
	}
	return s.packs[pick.key]
}

func (o *overlay) matches(s *Set, p Place) bool {
	r := o.f.Region
	if !strings.EqualFold(r.Country, strings.TrimSpace(p.Country)) {
		return false
	}
	if r.State == "" {
		return true
	}
	if !s.sameState(r.Country, r.State, p.State) {
		return false
	}
	if r.District == "" {
		return true
	}
	return nameIs(p.District, r.District, r.Aliases)
}

// sameState: the place's state is the state, or an alias its overlay gives.
func (s *Set) sameState(country, state, placeState string) bool {
	var aliases []string
	for _, x := range s.overlays {
		if x.key == regionKey(country, state, "") {
			aliases = x.f.Region.Aliases
		}
	}
	return nameIs(placeState, state, aliases)
}

func nameIs(got, name string, aliases []string) bool {
	// A map service may say "Anantapuramu District": the word is not part
	// of the name.
	g := strings.TrimSuffix(fold(got), "district")
	if g == "" {
		return false
	}
	for _, n := range append([]string{name}, aliases...) {
		if fold(n) == g {
			return true
		}
	}
	return false
}

// fold keeps a name's letters and digits, lower-cased: "Y.S.R. Kadapa"
// and "YSR kadapa" fold alike.
func fold(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r > 127 {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// regionKey names a region by its names folded as places are matched
// (fold), so two overlays whose names differ only in case or punctuation
// are the same region, and a district finds its state's overlay.
func regionKey(country, state, district string) string {
	k := strings.ToUpper(country)
	if state != "" {
		k += "/" + fold(state)
	}
	if district != "" {
		k += "/" + fold(district)
	}
	return k
}

func cmpOr(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func basePack(bf baseFile) (*Pack, error) {
	lib, err := agro.Default()
	if err != nil {
		return nil, err
	}
	book, err := inputs.Default(lib)
	if err != nil {
		return nil, err
	}
	p := &Pack{
		key: Base, name: Base, layers: []string{Base}, lib: lib, book: book, rules: DefaultRules(),
		storage: prices.StorageClasses(), msp: prices.SupportPrices(), schemes: bf.Schemes,
		sources: bf.Sources, origins: map[string]Origin{},
	}
	if err := checkSchemes(p.schemes, lib, p.sources); err != nil {
		return nil, fmt.Errorf("%w: data/base.json: %w", ErrInvalid, err)
	}
	return p, nil
}

// doc is a JSON object, field by field.
type doc = map[string]json.RawMessage

// state is a pack being built: the base's JSON with the overlays laid on.
type state struct {
	soils      []byte
	cropsDoc   doc
	crops      []doc
	inputsDoc  doc
	inputCrops []doc
	products   []doc
	inputSrc   []json.RawMessage
	bannedDoc  doc
	banned     []json.RawMessage
	rules      Rules
	storage    map[string]prices.Storage
	msp        []prices.SupportPrice
	schemes    []Scheme
	sources    []Source
	origins    map[string]Origin
	cur        *overlay
	errs       []error
	layers     []string
}

func (st *state) bad(key, format string, a ...any) {
	st.errs = append(st.errs, fmt.Errorf("%w: %s: %s: %s", ErrInvalid, st.cur.file, key, fmt.Sprintf(format, a...)))
}

func build(bf baseFile, chain []*overlay) (*Pack, error) {
	soils, crops, err := agro.DefaultJSON()
	if err != nil {
		return nil, err
	}
	inData, banned, err := inputs.DefaultJSON()
	if err != nil {
		return nil, err
	}
	st := &state{soils: soils, rules: DefaultRules(), storage: prices.StorageClasses(), msp: prices.SupportPrices(), origins: map[string]Origin{}, layers: []string{Base}}
	for _, s := range bf.Schemes {
		st.schemes = append(st.schemes, s.clone())
	}
	st.sources = slices.Clone(bf.Sources)
	if st.cropsDoc, st.crops, err = split(crops, "crops"); err != nil {
		return nil, err
	}
	if st.inputsDoc, st.inputCrops, err = split(inData, "crops"); err != nil {
		return nil, err
	}
	if _, st.products, err = split(inData, "products"); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(st.inputsDoc["sources"], &st.inputSrc); err != nil {
		return nil, err
	}
	if st.bannedDoc, err = object(banned); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(st.bannedDoc["actives"], &st.banned); err != nil {
		return nil, err
	}
	var last *overlay
	for _, o := range chain {
		st.cur, last = o, o
		st.layers = append(st.layers, o.name)
		st.lay(o)
		if len(st.errs) > 0 {
			return nil, errors.Join(st.errs...)
		}
	}
	p, err := st.pack()
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrInvalid, last.file, err)
	}
	p.key, p.name = last.key, last.name
	return p, nil
}

// lay lays one overlay on the state.
func (st *state) lay(o *overlay) {
	f := o.f
	seen := map[string]bool{}
	for _, s := range st.sources {
		seen[s.ID] = true
	}
	for _, s := range f.Sources {
		if s.ID == "" || s.Name == "" || s.URL == "" || s.Read == "" {
			st.bad("sources."+s.ID, "a source needs id, name, url and the day it was read")
			continue
		}
		if seen[s.ID] {
			st.bad("sources."+s.ID, "repeated: a source id is used once across the layers")
			continue
		}
		seen[s.ID] = true
		st.sources = append(st.sources, s)
		raw, _ := json.Marshal(inputs.Source{ID: s.ID, Name: s.Name, Publisher: s.Publisher, URL: s.URL, Read: s.Read})
		st.inputSrc = append(st.inputSrc, raw)
	}
	for _, id := range sortedKeys(f.Crops) {
		st.patchList(&st.crops, "id", id, "crops."+id, f.Crops[id], reflect.TypeFor[agro.Crop](), false)
	}
	if f.Inputs != nil {
		for _, id := range sortedKeys(f.Inputs.Crops) {
			st.patchList(&st.inputCrops, "crop", id, "inputs.crops."+id, f.Inputs.Crops[id], reflect.TypeFor[inputs.Crop](), false)
		}
		for _, id := range sortedKeys(f.Inputs.Products) {
			st.patchList(&st.products, "id", id, "inputs.products."+id, f.Inputs.Products[id], reflect.TypeFor[inputs.Product](), true)
		}
		for i, b := range f.Inputs.Banned {
			var x inputs.Banned
			if err := strict(b, &x); err != nil {
				st.bad(fmt.Sprintf("inputs.banned[%d]", i), "%v", err)
				continue
			}
			if x.Name == "" || x.Ref.Source == "" || !seen[x.Ref.Source] {
				st.bad(fmt.Sprintf("inputs.banned[%d]", i), "needs a name and a ref to one of the sources")
				continue
			}
			st.banned = append(st.banned, b)
			r := Ref(x.Ref)
			st.origins["inputs.banned."+x.Name] = Origin{Layer: o.name, File: o.file, Ref: &r}
		}
	}
	if len(f.Rules) > 0 {
		st.patchValue(&st.rules, "rules", f.Rules, []string{})
	}
	for _, class := range sortedKeys(f.Storage) {
		cur, ok := st.storage[class]
		if !ok {
			st.bad("storage."+class, "unknown storage class")
			continue
		}
		if ref := st.patchValue(&cur, "storage."+class, f.Storage[class], []string{"class", "source"}); ref != nil {
			cur.Source = st.cite(*ref)
			st.storage[class] = cur
		}
	}
	for i, m := range f.MSP {
		key := fmt.Sprintf("msp[%d]", i)
		if m.Commodity == "" || m.Season == "" || m.Price <= 0 {
			st.bad(key, "needs a commodity, a season and a price above 0")
			continue
		}
		if !st.known(m.Ref) {
			st.bad(key, "needs a ref to one of the sources")
			continue
		}
		sp := prices.SupportPrice{Commodity: prices.Commodity(m.Commodity), Variety: m.Variety, Season: m.Season, Price: m.Price, Source: st.cite(m.Ref)}
		if sp.Commodity == "" {
			sp.Commodity = m.Commodity
		}
		k := slices.IndexFunc(st.msp, func(x prices.SupportPrice) bool {
			return strings.EqualFold(x.Commodity, sp.Commodity) && x.Variety == sp.Variety && x.Season == sp.Season
		})
		if k >= 0 {
			st.msp[k] = sp
		} else {
			st.msp = append(st.msp, sp)
		}
		r := m.Ref
		st.origins["msp."+sp.Commodity] = Origin{Layer: o.name, File: o.file, Ref: &r}
	}
	for _, id := range sortedKeys(f.Schemes) {
		k := slices.IndexFunc(st.schemes, func(s Scheme) bool { return s.ID == id })
		cur := Scheme{ID: id}
		if k >= 0 {
			cur = st.schemes[k]
		}
		ref := st.patchValue(&cur, "schemes."+id, f.Schemes[id], []string{"id", "ref"})
		if ref == nil {
			continue
		}
		cur.ID, cur.Ref = id, *ref
		if k < 0 {
			st.origins["schemes."+id] = Origin{Layer: o.name, File: o.file, Ref: ref}
		}
		if k >= 0 {
			st.schemes[k] = cur
		} else {
			st.schemes = append(st.schemes, cur)
		}
	}
}

// cite writes a reference as a source string: "name, where".
func (st *state) cite(r Ref) string {
	for _, s := range st.sources {
		if s.ID == r.Source {
			return s.Name + ", " + r.Where
		}
	}
	return r.Source + ", " + r.Where
}

func (st *state) known(r Ref) bool {
	if r.Where == "" {
		return false
	}
	return slices.ContainsFunc(st.sources, func(s Source) bool { return s.ID == r.Source })
}

// takeRef splits an entry's "ref" from its fields and checks it.
func (st *state) takeRef(key string, m doc) (*Ref, bool) {
	raw, ok := m["ref"]
	if !ok {
		st.bad(key, "every overridden value needs a \"ref\": {\"source\", \"where\"}")
		return nil, false
	}
	delete(m, "ref")
	var r Ref
	if err := strict(raw, &r); err != nil {
		st.bad(key+".ref", "%v", err)
		return nil, false
	}
	if !st.known(r) {
		st.bad(key+".ref", "source %q is not among the sources, or no place in it is given", r.Source)
		return nil, false
	}
	return &r, true
}

// patchList patches (or adds) the entry of list whose idKey is id, field
// by field, and checks the result decodes into t. With ownRef the entry's
// own "ref" field becomes the overlay's ref (an inputs product's).
func (st *state) patchList(list *[]doc, idKey, id, key string, raw json.RawMessage, t reflect.Type, ownRef bool) {
	patch, err := object(raw)
	if err != nil {
		st.bad(key, "%v", err)
		return
	}
	ref, ok := st.takeRef(key, patch)
	if !ok {
		return
	}
	k := slices.IndexFunc(*list, func(d doc) bool {
		var v string
		_ = json.Unmarshal(d[idKey], &v)
		return v == id
	})
	var cur doc
	added := k < 0
	if added {
		cur = doc{}
		idRaw, _ := json.Marshal(id)
		cur[idKey] = idRaw
	} else {
		cur = clone((*list)[k])
	}
	o := Origin{Layer: st.cur.name, File: st.cur.file, Ref: ref}
	if !st.merge(cur, patch, t, key, []string{idKey, "ref"}, o, !added) {
		return
	}
	if ownRef {
		cur["ref"], _ = json.Marshal(ref)
	}
	if added {
		st.origins[key] = o
	}
	if err := strictDoc(cur, reflect.New(t).Interface()); err != nil {
		st.bad(key, "%v", err)
		return
	}
	if added {
		*list = append(*list, cur)
	} else {
		(*list)[k] = cur
	}
}

// patchValue patches *v field by field from raw, which must carry a ref,
// and returns the ref; nil when it fails.
func (st *state) patchValue(v any, key string, raw json.RawMessage, deny []string) *Ref {
	patch, err := object(raw)
	if err != nil {
		st.bad(key, "%v", err)
		return nil
	}
	ref, ok := st.takeRef(key, patch)
	if !ok {
		return nil
	}
	b, _ := json.Marshal(v)
	cur, _ := object(b)
	o := Origin{Layer: st.cur.name, File: st.cur.file, Ref: ref}
	if !st.merge(cur, patch, reflect.TypeOf(v).Elem(), key, deny, o, true) {
		return nil
	}
	fresh := reflect.New(reflect.TypeOf(v).Elem())
	if err := strictDoc(cur, fresh.Interface()); err != nil {
		st.bad(key, "%v", err)
		return nil
	}
	reflect.ValueOf(v).Elem().Set(fresh.Elem())
	return ref
}

// merge sets each field of patch on cur: a field whose type is a struct
// (not a pointer to one) merges by its own fields, every other field is
// replaced whole. A key that is not a field of t, or is denied, is an
// error.
func (st *state) merge(cur, patch doc, t reflect.Type, key string, deny []string, o Origin, record bool) bool {
	fields := jsonFields(t)
	ok := true
	for _, k := range sortedKeys(patch) {
		ft, known := fields[k]
		if !known || slices.Contains(deny, k) {
			st.bad(key+"."+k, "unknown key")
			ok = false
			continue
		}
		if ft.Kind() == reflect.Struct && ft != reflect.TypeFor[time.Time]() {
			sub, err := object(patch[k])
			if err != nil {
				st.bad(key+"."+k, "%v", err)
				ok = false
				continue
			}
			inner, _ := object(cur[k])
			if inner == nil {
				inner = doc{}
			}
			if !st.merge(inner, sub, ft, key+"."+k, nil, o, record) {
				ok = false
				continue
			}
			cur[k], _ = json.Marshal(inner)
			continue
		}
		cur[k] = patch[k]
		if record {
			st.origins[key+"."+k] = o
		}
	}
	return ok
}

// pack builds and checks the library, the book and the rest.
func (st *state) pack() (*Pack, error) {
	crops := st.cropsDoc
	crops["crops"], _ = json.Marshal(st.crops)
	cropsJSON, _ := json.Marshal(crops)
	lib, err := agro.Load(st.soils, cropsJSON)
	if err != nil {
		return nil, err
	}
	in := clone(st.inputsDoc)
	in["crops"], _ = json.Marshal(st.inputCrops)
	in["products"], _ = json.Marshal(st.products)
	in["sources"], _ = json.Marshal(st.inputSrc)
	inJSON, _ := json.Marshal(in)
	bd := clone(st.bannedDoc)
	bd["actives"], _ = json.Marshal(st.banned)
	bannedJSON, _ := json.Marshal(bd)
	book, err := inputs.Load(lib, inJSON, bannedJSON)
	if err != nil {
		return nil, err
	}
	if err := checkRules(st.rules); err != nil {
		return nil, err
	}
	if err := checkSchemes(st.schemes, lib, st.sources); err != nil {
		return nil, err
	}
	return &Pack{
		layers: st.layers, lib: lib, book: book, rules: st.rules, storage: st.storage,
		msp: st.msp, schemes: st.schemes, sources: st.sources, origins: st.origins,
	}, nil
}

func checkRules(r Rules) error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf("rules: "+format, a...)) }
	if r.TurnaroundDays < 0 || r.TurnaroundDays > 120 {
		bad("turnaround_days %d: want 0 to 120", r.TurnaroundDays)
	}
	if r.MaxWaitDays <= 0 || r.MaxWaitDays > 365 {
		bad("max_wait_days %d: want 1 to 365", r.MaxWaitDays)
	}
	if r.WaitDaysPerPoint <= 0 {
		bad("wait_days_per_point %d: want 1 or more", r.WaitDaysPerPoint)
	}
	var months [13]int
	for _, s := range r.Seasons {
		if !slices.Contains([]string{agro.Kharif, agro.Rabi, agro.Zaid}, s.Season) || s.From < 1 || s.From > 12 || s.To < 1 || s.To > 12 {
			bad("season %+v: want kharif, rabi or zaid, months 1 to 12", s)
			continue
		}
		for m := time.January; m <= time.December; m++ {
			if s.Contains(m) {
				months[m]++
			}
		}
	}
	for m := 1; m <= 12; m++ {
		if months[m] != 1 {
			bad("seasons: month %d is in %d seasons, want exactly one", m, months[m])
		}
	}
	return errors.Join(errs...)
}

func checkSchemes(ss []Scheme, lib *agro.Library, sources []Source) error {
	var errs []error
	for _, s := range ss {
		bad := func(format string, a ...any) {
			errs = append(errs, fmt.Errorf("schemes.%s: "+format, append([]any{s.ID}, a...)...))
		}
		if s.ID == "" || s.Name == "" || s.Kind == "" {
			bad("needs an id, a name and a kind")
		}
		if !slices.ContainsFunc(sources, func(x Source) bool { return x.ID == s.Ref.Source }) || s.Ref.Where == "" {
			bad("ref %+v names no known source or no place in it", s.Ref)
		}
		for _, c := range s.Crops {
			if _, err := lib.Crop(c); err != nil {
				bad("crop %q is not in the crop library", c)
			}
		}
		seasons := []string{agro.Kharif, agro.Rabi, agro.Zaid}
		if s.Season != "" && !slices.Contains(seasons, s.Season) {
			bad("season %q: want kharif, rabi or zaid", s.Season)
		}
		if s.Year != 0 && (s.Season == "" || s.Year < 2000 || s.Year > 2100) {
			bad("year %d: want a season with it, and a year from 2000 to 2100", s.Year)
		}
		for _, c := range s.Cutoffs {
			if _, err := time.Parse("01-02", c.By); err != nil || len(c.By) != 5 {
				bad("cutoff %q: want MM-DD", c.By)
			}
			if !slices.Contains(seasons, c.Season) || (c.What != "enrolment" && c.What != "sowing") {
				bad("cutoff %+v: want a season and enrolment or sowing", c)
			}
			if s.Season != "" && c.Season != s.Season {
				bad("cutoff %+v: the scheme is for %s only", c, s.Season)
			}
		}
		if s.LossNoticeHours < 0 {
			bad("loss_notice_hours %d", s.LossNoticeHours)
		}
	}
	return errors.Join(errs...)
}

// jsonFields maps a struct type's JSON keys to their types.
func jsonFields(t reflect.Type) map[string]reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	out := map[string]reflect.Type{}
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if f.Anonymous && name == "" {
			for k, v := range jsonFields(f.Type) {
				out[k] = v
			}
			continue
		}
		if name == "" {
			name = f.Name
		}
		out[name] = f.Type
	}
	return out
}

// split reads a JSON object and its list under key, entry by entry.
func split(raw []byte, key string) (doc, []doc, error) {
	d, err := object(raw)
	if err != nil {
		return nil, nil, err
	}
	var list []doc
	if err := json.Unmarshal(d[key], &list); err != nil {
		return nil, nil, err
	}
	return d, list, nil
}

func object(raw []byte) (doc, error) {
	var d doc
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	if d == nil {
		return nil, errors.New("want a JSON object")
	}
	return d, nil
}

func clone(d doc) doc {
	out := make(doc, len(d))
	for k, v := range d {
		out[k] = v
	}
	return out
}

// strict decodes raw into v, refusing unknown keys.
func strict(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("more than one JSON value")
	}
	return nil
}

func strictDoc(d doc, v any) error {
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return strict(b, v)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
