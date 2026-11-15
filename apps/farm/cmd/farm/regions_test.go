package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/region"
)

// A bad overlay in the -regions folder stops the start, and the error
// names the file and the key.
func TestBadRegionOverlayStopsTheStart(t *testing.T) {
	dir := t.TempDir()
	overlays := filepath.Join(dir, "regions")
	if err := os.Mkdir(overlays, 0o755); err != nil {
		t.Fatal(err)
	}
	bad := `{"region": {"country": "IN", "state": "Bihar"},
		"sources": [{"id": "t", "name": "Test", "publisher": "us", "url": "https://example.org", "read": "2026-11-15"}],
		"crops": {"groundnut": {"dayz": [90, 100], "ref": {"source": "t", "where": "p. 1"}}}}`
	if err := os.WriteFile(filepath.Join(overlays, "bihar.json"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	c := config{db: filepath.Join(dir, "farm.db"), geocoder: "off", climate: "off", regions: overlays}
	err := run(context.Background(), c, io.Discard)
	if !errors.Is(err, region.ErrInvalid) || !strings.Contains(err.Error(), "bihar.json") || !strings.Contains(err.Error(), "crops.groundnut.dayz") {
		t.Fatalf("err %v: want ErrInvalid naming bihar.json and crops.groundnut.dayz", err)
	}
}
