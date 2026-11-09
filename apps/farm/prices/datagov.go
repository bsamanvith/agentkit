package prices

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/bsamanvith/agentkit/apps/farm/feeds"
)

// DataGovURL is the Open Government Data platform's API.
const DataGovURL = "https://api.data.gov.in"

// DataGovDaily is the platform's resource "Current Daily Price of Various
// Commodities from Various Markets (Mandi)", published from Agmarknet
// (https://data.gov.in/resource/current-daily-price-various-commodities-various-markets-mandi):
// the latest day's prices of every reporting mandi in India, under the
// Government Open Data License - India.
const DataGovDaily = "9ef84268-d588-465a-a308-a864a43d0070"

// dataGovPage is the records asked for in one request, and dataGovPages
// the most requests one query makes.
const (
	dataGovPage  = 1000
	dataGovPages = 5
)

// DataGovOption configures [DataGov].
type DataGovOption func(*dataGov)

// WithDataGovURL sets the API's base URL (default [DataGovURL]), for tests.
func WithDataGovURL(u string) DataGovOption {
	return func(d *dataGov) { d.base = strings.TrimRight(u, "/") }
}

// WithDataGovResource sets the resource id (default [DataGovDaily]); another
// resource with the same fields, such as a variety-wise history, works too.
func WithDataGovResource(id string) DataGovOption { return func(d *dataGov) { d.resource = id } }

// WithDataGovHTTPClient sets the HTTP client (default feeds.NewHTTPClient).
func WithDataGovHTTPClient(hc *http.Client) DataGovOption { return func(d *dataGov) { d.http = hc } }

// DataGov returns a Source on api.data.gov.in with key, the operator's own
// (free, from https://data.gov.in after signing in: My Account, API key).
// Without a key it returns [feeds.ErrNotConfigured].
//
// The platform filters on exact values, so it is asked by state and
// commodity in Agmarknet's names, and district, market and dates are
// filtered here. Its servers refuse some cloud addresses (a reset
// connection or a 403), which [feeds.GetBody] reports as
// [feeds.ErrUnavailable] so the next source answers.
func DataGov(key string, opts ...DataGovOption) (Source, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("%w: data.gov.in needs an API key (FARM_DATAGOV_KEY)", feeds.ErrNotConfigured)
	}
	d := &dataGov{key: key, base: DataGovURL, resource: DataGovDaily}
	for _, o := range opts {
		o(d)
	}
	if d.http == nil {
		d.http = feeds.NewHTTPClient()
	}
	if _, err := url.Parse(d.base); err != nil {
		return nil, err
	}
	return d, nil
}

type dataGov struct {
	key, base, resource string
	http                *http.Client
}

func (d *dataGov) Name() string { return "Agmarknet via data.gov.in" }

func (d *dataGov) Fetch(ctx context.Context, q Query) ([]Quote, error) {
	q, err := q.Normalized()
	if err != nil {
		return nil, err
	}
	var out []Quote
	for page := range dataGovPages {
		v := url.Values{}
		v.Set("api-key", d.key)
		v.Set("format", "json")
		v.Set("limit", strconv.Itoa(dataGovPage))
		v.Set("offset", strconv.Itoa(page*dataGovPage))
		if q.State != "" {
			v.Set("filters[state.keyword]", stateName(q.State))
		}
		if q.Commodity != "" {
			v.Set("filters[commodity]", q.Commodity)
		}
		var body dataGovAnswer
		if err := feeds.GetJSON(ctx, d.http, d.base+"/resource/"+url.PathEscape(d.resource)+"?"+v.Encode(), nil, &body); err != nil {
			// The key travels in the URL, as the platform wants it: keep
			// it out of the error.
			return nil, redact(err, d.key)
		}
		if body.Status != "" && !strings.EqualFold(body.Status, "ok") {
			return nil, fmt.Errorf("%w: data.gov.in said %q: %s", feeds.ErrUnavailable, body.Status, body.Message)
		}
		for _, r := range body.Records {
			x, ok := r.quote()
			if !ok || x.Valid() != nil || !q.Matches(x) {
				continue
			}
			x.Source = d.Name()
			out = append(out, x)
		}
		if len(body.Records) < dataGovPage {
			break
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

type dataGovAnswer struct {
	Status  string      `json:"status"`
	Message string      `json:"message"`
	Records []dataGovRe `json:"records"`
}

// dataGovRe is one record. The platform's resources spell their fields
// differently (state or State, min_price or Min_x0020_Price) and send
// numbers as numbers or strings, so a record is read loosely.
type dataGovRe map[string]any

func (r dataGovRe) field(names ...string) string {
	for k, v := range r {
		f := Fold(strings.ReplaceAll(k, "_x0020_", ""))
		if !slices.Contains(names, f) {
			continue
		}
		switch v := v.(type) {
		case string:
			return strings.TrimSpace(v)
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64)
		case json.Number:
			return v.String()
		}
	}
	return ""
}

func (r dataGovRe) quote() (Quote, bool) {
	day, err := ParseDay(r.field("arrivaldate", "date", "pricedate"))
	if err != nil {
		return Quote{}, false
	}
	num := func(names ...string) float64 {
		f, _ := strconv.ParseFloat(strings.ReplaceAll(r.field(names...), ",", ""), 64)
		return f
	}
	return Quote{
		Date: day, State: r.field("state"), District: r.field("district"), Market: r.field("market"),
		Commodity: r.field("commodity"), Variety: r.field("variety"), Grade: r.field("grade"),
		Min: num("minprice", "min"), Max: num("maxprice", "max"), Modal: num("modalprice", "modal"),
	}, true
}

// stateName spells a state as Agmarknet does, from the usual spellings.
func stateName(s string) string {
	switch Fold(s) {
	case "karnataka":
		return "Karnataka"
	case "andhrapradesh", "ap":
		return "Andhra Pradesh"
	case "telangana", "ts", "tg":
		return "Telangana"
	case "tamilnadu", "tn":
		return "Tamil Nadu"
	case "maharashtra":
		return "Maharashtra"
	case "kerala":
		return "Kerala"
	}
	return s
}

// redact replaces secret in err's text.
func redact(err error, secret string) error {
	if secret == "" || !strings.Contains(err.Error(), secret) {
		return err
	}
	return &redacted{msg: strings.ReplaceAll(err.Error(), secret, "REDACTED"), err: err}
}

type redacted struct {
	msg string
	err error
}

func (r *redacted) Error() string { return r.msg }

// Unwrap keeps errors.Is working (feeds.ErrUnavailable) without the text.
func (r *redacted) Unwrap() error { return r.err }
