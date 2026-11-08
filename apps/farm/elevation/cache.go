package elevation

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/bsamanvith/agentkit/apps/farm/geo"
)

// MaxCacheEntries bounds a cache: past it, an arbitrary tenth of the heights
// is forgotten. A plot's survey asks for about 25 points.
const MaxCacheEntries = 200_000

// Cache remembers heights per service and point, to about a metre. Kept in
// a file, it saves the free services' daily limits across restarts. It is
// safe for concurrent use.
type Cache struct {
	mu   sync.Mutex
	path string
	m    map[string]float64
}

// NewCache returns a cache in memory.
func NewCache() *Cache { return &Cache{m: map[string]float64{}} }

// OpenCache returns a cache kept in the file at path, reading what it
// holds; a missing or unreadable file starts empty (it is only a cache).
func OpenCache(path string) (*Cache, error) {
	if path == "" {
		return nil, errors.New("elevation: the cache needs a file")
	}
	c := &Cache{path: path, m: map[string]float64{}}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, fmt.Errorf("elevation: reading the cache: %w", err)
	default:
		var f cacheFile
		if json.Unmarshal(b, &f) == nil && f.Version == cacheVersion && f.Heights != nil {
			c.m = f.Heights
		}
	}
	return c, nil
}

const cacheVersion = 1

type cacheFile struct {
	Version int                `json:"version"`
	Heights map[string]float64 `json:"heights"`
}

// Len is how many heights the cache holds.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}

func cacheKey(tag string, p geo.Point) string {
	return tag + "/" + strconv.FormatInt(int64(math.Round(p.Lat*1e5)), 10) + "/" + strconv.FormatInt(int64(math.Round(p.Lng*1e5)), 10)
}

func (c *Cache) get(tag string, p geo.Point) (float64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h, ok := c.m[cacheKey(tag, p)]
	return h, ok
}

// put remembers heights and, for a file cache, writes the file. A failed
// write loses nothing but the saving.
func (c *Cache) put(tag string, ps []geo.Point, hs []float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m)+len(ps) > MaxCacheEntries {
		drop := len(c.m) / 10
		for k := range c.m {
			if drop == 0 {
				break
			}
			delete(c.m, k)
			drop--
		}
	}
	for i, p := range ps {
		c.m[cacheKey(tag, p)] = hs[i]
	}
	if c.path != "" {
		_ = c.save()
	}
}

func (c *Cache) save() error {
	b, err := json.Marshal(cacheFile{Version: cacheVersion, Heights: c.m})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(c.path), filepath.Base(c.path)+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), c.path)
}
