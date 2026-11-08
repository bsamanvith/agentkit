package elevation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/geo"
)

// OpenTopoDataURL is Open Topo Data's public API with the SRTM 30 m
// dataset. Its free use is at most one request a second and 1,000 a day.
const OpenTopoDataURL = "https://api.opentopodata.org/v1/srtm30m"

// OpenTopoDataInterval is the pause between requests the public API asks
// for.
const OpenTopoDataInterval = time.Second

// datasets names Open Topo Data's datasets and their grids in metres.
var datasets = map[string]struct {
	name string
	res  float64
}{
	"srtm30m":  {"SRTM 30 m", 30},
	"srtm90m":  {"SRTM 90 m", 90},
	"aster30m": {"ASTER GDEM 30 m", 30},
	"mapzen":   {"Mapzen terrain", 30},
	"etopo1":   {"ETOPO1", 1800},
}

// OpenTopoData returns a client of an Open Topo Data dataset at u, the API
// and the dataset's name (such as [OpenTopoDataURL]; a self-hosted server
// works too). It waits [OpenTopoDataInterval] between requests.
func OpenTopoData(u string, opts ...Option) (*Client, error) {
	pu, err := url.Parse(u)
	if err != nil {
		return nil, fmt.Errorf("elevation: %q is not a URL", u)
	}
	ds := path.Base(strings.TrimSuffix(pu.Path, "/"))
	if ds == "" || ds == "/" || ds == "." {
		return nil, fmt.Errorf("elevation: %q names no dataset (such as %s)", u, OpenTopoDataURL)
	}
	d, ok := datasets[ds]
	if !ok {
		d.name, d.res = ds, 90
	}
	return newClient(strings.TrimSuffix(u, "/"), &Client{
		name: d.name + " via Open Topo Data", tag: "otd/" + ds, res: d.res,
		batch: MaxPoints, interval: OpenTopoDataInterval, fetch: openTopoDataFetch,
	}, opts)
}

func openTopoDataFetch(ctx context.Context, c *Client, ps []geo.Point) ([]float64, error) {
	locs := make([]string, len(ps))
	for i, p := range ps {
		locs[i] = strconv.FormatFloat(p.Lat, 'f', 6, 64) + "," + strconv.FormatFloat(p.Lng, 'f', 6, 64)
	}
	resp, err := c.get(ctx, c.base+"?"+url.Values{"locations": {strings.Join(locs, "|")}}.Encode())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		Status  string `json:"status"`
		Error   string `json:"error"`
		Results []struct {
			Elevation *float64 `json:"elevation"`
		} `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if body.Status != "OK" {
		return nil, fmt.Errorf("%w: status %q: %s", ErrUnavailable, body.Status, body.Error)
	}
	out := make([]float64, len(body.Results))
	for i, r := range body.Results {
		if r.Elevation == nil {
			// Outside the dataset (SRTM stops at 60° N and 56° S) or a void.
			return nil, fmt.Errorf("%w: no height at point %d", ErrUnavailable, i)
		}
		out[i] = *r.Elevation
	}
	return out, nil
}
