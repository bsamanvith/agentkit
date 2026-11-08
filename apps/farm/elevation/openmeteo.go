package elevation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/bsamanvith/agentkit/apps/farm/geo"
)

// DefaultURL is Open-Meteo's elevation API.
const DefaultURL = "https://api.open-meteo.com/v1/elevation"

// MaxPoints is the most points one request asks for (both services' limit).
const MaxPoints = 100

// OpenMeteo returns a client of Open-Meteo's elevation API at base (such as
// [DefaultURL]).
func OpenMeteo(base string, opts ...Option) (*Client, error) {
	return newClient(base, &Client{
		name: "Copernicus DEM GLO-90 via Open-Meteo", tag: "om", res: 90,
		batch: MaxPoints, fetch: openMeteoFetch,
	}, opts)
}

func openMeteoFetch(ctx context.Context, c *Client, ps []geo.Point) ([]float64, error) {
	lat, lng := make([]string, len(ps)), make([]string, len(ps))
	for i, p := range ps {
		lat[i] = strconv.FormatFloat(p.Lat, 'f', 6, 64)
		lng[i] = strconv.FormatFloat(p.Lng, 'f', 6, 64)
	}
	q := url.Values{"latitude": {strings.Join(lat, ",")}, "longitude": {strings.Join(lng, ",")}}
	resp, err := c.get(ctx, c.base+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		Elevation []float64 `json:"elevation"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return body.Elevation, nil
}
