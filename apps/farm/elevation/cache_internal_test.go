package elevation

import (
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/geo"
)

func TestCacheIsBounded(t *testing.T) {
	t.Parallel()
	c := NewCache()
	ps := make([]geo.Point, MaxPoints)
	hs := make([]float64, MaxPoints)
	for i := 0; c.Len() < MaxCacheEntries; i++ {
		for j := range ps {
			ps[j] = geo.Point{Lat: 10 + float64(i)*1e-3, Lng: 70 + float64(j)*1e-3}
		}
		c.put("om", ps, hs)
	}
	c.put("om", []geo.Point{{Lat: 1, Lng: 1}}, []float64{3})
	if n := c.Len(); n > MaxCacheEntries || n < MaxCacheEntries*8/10 {
		t.Errorf("%d heights cached, cap %d", n, MaxCacheEntries)
	}
	if h, ok := c.get("om", geo.Point{Lat: 1, Lng: 1}); !ok || h != 3 {
		t.Error("the newest height was dropped")
	}
}
