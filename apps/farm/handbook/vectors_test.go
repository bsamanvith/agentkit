package handbook_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/handbook"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/memory/memorytest"
)

func texts(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "passage " + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	return out
}

func TestVectorsPaceAndRemember(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vectors.gob")
	fake := memorytest.NewFakeEmbedder(memorytest.DefaultFakeModel, memorytest.DefaultFakeDim)
	clk := clocktest.NewFake(time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC))
	v := handbook.NewVectors(fake, path, 10, handbook.WithClock(clk))

	// 25 texts at 10 a minute: 10 now, 10 after a minute, 5 after two.
	done := make(chan error, 1)
	var got [][]float32
	go func() {
		var err error
		got, err = v.Embed(ctx, texts(25), memory.PurposeDocument)
		done <- err
	}()
	for range 2 {
		if err := clk.WaitForTimers(ctx, 1); err != nil {
			t.Fatal(err)
		}
		clk.Advance(time.Minute)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	calls := fake.Calls()
	if len(got) != 25 || len(calls) != 3 || len(calls[0].Texts) != 10 || len(calls[2].Texts) != 5 {
		t.Fatalf("%d vectors in %d calls", len(got), len(calls))
	}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}

	// A new start reads the file: no request for what it already has.
	again := memorytest.NewFakeEmbedder(memorytest.DefaultFakeModel, memorytest.DefaultFakeDim)
	v2 := handbook.NewVectors(again, path, 10)
	if v2.Len() != 25 {
		t.Fatalf("read %d vectors back", v2.Len())
	}
	out, err := v2.Embed(ctx, append(texts(25), "a new passage"), memory.PurposeDocument)
	if err != nil || len(out) != 26 || len(again.Calls()) != 1 || len(again.Calls()[0].Texts) != 1 {
		t.Fatalf("second start: %d vectors, calls %+v, %v", len(out), again.Calls(), err)
	}
	for i := range 25 {
		if len(out[i]) != len(got[i]) || out[i][0] != got[i][0] {
			t.Fatal("a remembered vector differs")
		}
	}

	// Another model's file is ignored.
	other := memorytest.NewFakeEmbedder("other-model", memorytest.DefaultFakeDim)
	if n := handbook.NewVectors(other, path, 0).Len(); n != 0 {
		t.Errorf("another model read %d vectors", n)
	}
}

func TestHandbookWithVectorsFile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vectors.gob")
	fake := memorytest.NewFakeEmbedder(memorytest.DefaultFakeModel, memorytest.DefaultFakeDim)
	v := handbook.NewVectors(fake, path, 0)
	book, err := handbook.New(ctx, nil, handbook.WithEmbedder(v))
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	// Restarted, the handbook is indexed from the file alone.
	again := memorytest.NewFakeEmbedder(memorytest.DefaultFakeModel, memorytest.DefaultFakeDim)
	if _, err := handbook.New(ctx, nil, handbook.WithEmbedder(handbook.NewVectors(again, path, 0))); err != nil {
		t.Fatal(err)
	}
	if len(again.Calls()) != 0 {
		t.Errorf("%d embedding calls after a restart, want 0", len(again.Calls()))
	}
	if book.Passages() < 100 {
		t.Errorf("%d passages", book.Passages())
	}
}
