package prices

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/feeds"
)

// MaxFileBytes bounds one price file [Files] reads: 64 MiB.
const MaxFileBytes = 64 << 20

// Files returns a Source on the price files (.csv) in dir and its folders:
// files someone downloaded from Agmarknet (its report export), from
// data.gov.in (a resource's CSV) or from CEDA's Agri Market portal, or
// wrote by hand. Columns are found by their header, in any order and any
// of the sources' spellings: state, district, market, commodity, variety,
// grade, date (arrival_date, price date), min, max and modal price
// (Rs./Quintal). A file without a state column takes its state from its
// folder's or its own name ("karnataka/tomato-2025.csv"). Rows that do not
// read are skipped and counted ([FileStats]).
//
// Files are read when first asked and again when one changes, so a file
// dropped in the folder is seen without a restart.
func Files(dir string) (*FileSource, error) {
	if dir == "" {
		return nil, fmt.Errorf("%w: no price folder (FARM_PRICE_FILES)", feeds.ErrNotConfigured)
	}
	st, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("prices: price folder: %w", err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("prices: price folder %s is not a folder", dir)
	}
	return &FileSource{dir: dir, files: map[string]*priceFile{}}, nil
}

// FileSource is the Source [Files] returns.
type FileSource struct {
	dir   string
	mu    sync.Mutex
	files map[string]*priceFile
}

type priceFile struct {
	mod     time.Time
	size    int64
	quotes  []Quote
	skipped int
	err     error
}

// FileStats says what a folder held when last read.
type FileStats struct {
	Files   int `json:"files"`
	Quotes  int `json:"quotes"`
	Skipped int `json:"skipped"`
	// Errors names the files that could not be read at all.
	Errors []string `json:"errors,omitempty"`
}

// Name implements Source.
func (f *FileSource) Name() string { return "price files" }

// Fetch implements Source.
func (f *FileSource) Fetch(ctx context.Context, q Query) ([]Quote, error) {
	q, err := q.Normalized()
	if err != nil {
		return nil, err
	}
	all, _, err := f.All(ctx)
	if err != nil {
		return nil, err
	}
	var out []Quote
	for _, x := range all {
		if q.Matches(x) {
			out = append(out, x)
		}
	}
	if len(out) == 0 {
		return nil, feeds.ErrNoData
	}
	slices.SortFunc(out, newestFirst)
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// All returns every quote in the folder's files, reading again the files
// that changed.
func (f *FileSource) All(ctx context.Context) ([]Quote, FileStats, error) {
	var paths []string
	err := filepath.WalkDir(f.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.EqualFold(filepath.Ext(p), ".csv") {
			paths = append(paths, p)
		}
		return ctx.Err()
	})
	if err != nil {
		return nil, FileStats{}, fmt.Errorf("prices: reading %s: %w", f.dir, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Quote
	var stats FileStats
	seen := map[string]bool{}
	for _, p := range paths {
		seen[p] = true
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		pf := f.files[p]
		if pf == nil || !pf.mod.Equal(st.ModTime()) || pf.size != st.Size() {
			pf = &priceFile{mod: st.ModTime(), size: st.Size()}
			pf.quotes, pf.skipped, pf.err = readPriceFile(p, f.dir)
			f.files[p] = pf
		}
		stats.Files++
		if pf.err != nil {
			rel, _ := filepath.Rel(f.dir, p)
			stats.Errors = append(stats.Errors, rel+": "+pf.err.Error())
			continue
		}
		stats.Quotes += len(pf.quotes)
		stats.Skipped += pf.skipped
		out = append(out, pf.quotes...)
	}
	for p := range f.files {
		if !seen[p] {
			delete(f.files, p)
		}
	}
	return out, stats, nil
}

// columns are the header names each field goes by, folded. Besides
// Agmarknet's report export ("Market Name", "Reported Date", "Modal Price
// (Rs./Quintal)") and data.gov.in's CSV (arrival_date, modal_price), they
// read India Data Portal's Agmarknet files (report_date, state_name,
// district_name, market_center, commodity_name, variety, min_price,
// max_price, modal_price; the 2025-26 files say date and
// market_center_name; ids, codes, coordinates and arrivals are not used)
// and CEDA's Agri Market downloads (state_name, district_name, market_name,
// commodity_name, date, min_price, max_price, modal_price, as its README
// describes them; its Census 2011 names are not used, so the district is
// Agmarknet's).
var columns = map[string][]string{
	"state":     {"state", "statename"},
	"district":  {"district", "districtname"},
	"market":    {"market", "marketname", "marketcenter", "marketcentername", "apmc", "mandi"},
	"commodity": {"commodity", "commodityname", "crop"},
	"variety":   {"variety"},
	"grade":     {"grade"},
	"date":      {"date", "arrivaldate", "pricedate", "reporteddate", "reportdate", "tradedate"},
	"min":       {"min", "minprice", "minpricersquintal", "minimumprice"},
	"max":       {"max", "maxprice", "maxpricersquintal", "maximumprice"},
	"modal":     {"modal", "modalprice", "modalpricersquintal"},
}

func readPriceFile(path, root string) ([]Quote, int, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer fh.Close()
	var out []Quote
	skipped, err := scanPrices(io.LimitReader(fh, MaxFileBytes), stateFromPath(path, root), func(x Quote) error {
		out = append(out, x)
		return nil
	})
	return out, skipped, err
}

// scanPrices reads a price file's rows from r, calling each with every
// valid quote, in file order. state is the state of a file without a state
// column. It returns how many rows did not read; an error from each stops
// it.
func scanPrices(r io.Reader, state string, each func(Quote) error) (int, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	cr.TrimLeadingSpace = true
	cr.ReuseRecord = true
	head, err := cr.Read()
	if err != nil {
		return 0, fmt.Errorf("no header: %w", err)
	}
	col := map[string]int{}
	for i, h := range head {
		h = Fold(strings.ReplaceAll(strings.TrimPrefix(h, "\uFEFF"), "_x0020_", ""))
		for field, names := range columns {
			if _, done := col[field]; !done && slices.Contains(names, h) {
				col[field] = i
			}
		}
	}
	for _, need := range []string{"market", "commodity", "date", "modal"} {
		if _, ok := col[need]; !ok {
			return 0, fmt.Errorf("no %s column in the header", need)
		}
	}
	if _, ok := col["state"]; ok {
		state = ""
	}
	get := func(rec []string, field string) string {
		i, ok := col[field]
		if !ok || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}
	num := func(rec []string, field string) float64 {
		v, _ := strconv.ParseFloat(strings.ReplaceAll(get(rec, field), ",", ""), 64)
		return v
	}
	skipped := 0
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			skipped++
			if _, ok := errors.AsType[*csv.ParseError](err); ok {
				continue
			}
			return skipped, err
		}
		day, err := ParseDay(get(rec, "date"))
		if err != nil {
			skipped++
			continue
		}
		x := Quote{
			Date: day, State: get(rec, "state"), District: get(rec, "district"), Market: get(rec, "market"),
			Commodity: get(rec, "commodity"), Variety: get(rec, "variety"), Grade: get(rec, "grade"),
			Min: num(rec, "min"), Max: num(rec, "max"), Modal: num(rec, "modal"), Source: "price files",
		}
		if x.State == "" {
			x.State = state
		}
		if x.Valid() != nil {
			skipped++
			continue
		}
		if err := each(x); err != nil {
			return skipped, err
		}
	}
	return skipped, nil
}

// ScanFiles reads every price file (.csv) in dir and its folders, of any
// size, and calls each with every valid quote, file by file. It is for bulk
// history (years of India Data Portal or CEDA exports, gigabytes) that a
// caller summarises as it reads instead of keeping, as the backtest does;
// [Files] keeps what it reads and caps each file at [MaxFileBytes]. An
// error from each, or ctx ending, stops it.
func ScanFiles(ctx context.Context, dir string, each func(Quote) error) (FileStats, error) {
	var stats FileStats
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".csv") {
			return nil
		}
		fh, err := os.Open(p)
		if err != nil {
			return err
		}
		defer fh.Close()
		stats.Files++
		n := 0
		var stop error
		skipped, err := scanPrices(fh, stateFromPath(p, dir), func(x Quote) error {
			n++
			if n%4096 == 0 {
				stop = ctx.Err()
			}
			if stop == nil {
				stop = each(x)
			}
			return stop
		})
		stats.Quotes += n
		stats.Skipped += skipped
		if stop != nil {
			return stop
		}
		if err != nil {
			rel, _ := filepath.Rel(dir, p)
			stats.Errors = append(stats.Errors, rel+": "+err.Error())
		}
		return nil
	})
	if err != nil {
		return stats, fmt.Errorf("prices: reading %s: %w", dir, err)
	}
	return stats, nil
}

// stateFromPath finds a state's name in a file's folders or name, below
// root.
func stateFromPath(path, root string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = path
	}
	f := Fold(rel)
	for _, s := range []string{"Andhra Pradesh", "Karnataka", "Telangana", "Tamil Nadu", "Kerala", "Maharashtra", "Odisha", "Gujarat", "Madhya Pradesh", "Uttar Pradesh", "Rajasthan", "Punjab", "Haryana", "West Bengal", "Bihar"} {
		if strings.Contains(f, Fold(s)) {
			return s
		}
	}
	return ""
}
