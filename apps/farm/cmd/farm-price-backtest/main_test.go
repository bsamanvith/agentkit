package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/prices"
)

func TestBacktestTableOnSyntheticSeriesAndOnFiles(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := run(context.Background(), &out, "", true, "", 30, prices.Tuning{}); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{"synthetic series", "groundnut", "noisy season", "no season", "sell", "sow", "SHOWN", "no advice", "passed the gate"} {
		if !strings.Contains(s, want) {
			t.Errorf("synthetic table lacks %q:\n%s", want, s)
		}
	}
	out.Reset()
	if err := run(context.Background(), &out, "../../prices/testdata/synthetic-bulk", false, "andhra pradesh", 1, prices.Tuning{}); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.Contains(s, "3 file(s), 4 quotes read, 1 rows skipped") || !strings.Contains(s, "Kurnool, Andhra Pradesh") {
		t.Errorf("files table:\n%s", s)
	}
	if err := run(context.Background(), &out, "", false, "", 30, prices.Tuning{}); err == nil {
		t.Error("no folder and no -synthetic: no error")
	}
}
