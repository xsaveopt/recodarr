package job

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/xsaveopt/recodarr/internal/store"
)

func TestFiltersConfigured(t *testing.T) {
	if filtersConfigured(&store.ProfileRow{}) {
		t.Fatal("an empty profile configures no filters")
	}
	cases := []store.ProfileRow{
		{SkipCodecs: "hevc"},
		{SkipBitrateMBPerHour: 1},
		{SkipFileSizeMB: 1},
		{SkipDurationMinutes: 1},
		{SkipHeightPx: 1},
		{SkipHDR: true},
	}
	for i := range cases {
		if !filtersConfigured(&cases[i]) {
			t.Fatalf("profile %+v must count as configured", cases[i])
		}
	}
}

func TestEvaluateFiltersFileSizeGate(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "media.mkv")

	skip, reason := evaluateFilters(ctx, &store.ProfileRow{}, store.JobRow{FilePath: path, FileSize: 1})
	if skip || reason != "" {
		t.Fatalf("got skip=%v reason=%q, want no filtering at all", skip, reason)
	}

	profile := &store.ProfileRow{SkipFileSizeMB: 100}
	small := store.JobRow{FilePath: path, FileSize: 50 * 1024 * 1024}
	skip, reason = evaluateFilters(ctx, profile, small)
	if !skip {
		t.Fatalf("50 MB is under the 100 MB floor, want a skip (reason %q)", reason)
	}
	if reason == "" {
		t.Fatal("a skip must carry a reason")
	}

	large := store.JobRow{FilePath: path, FileSize: 500 * 1024 * 1024}
	if skip, _ := evaluateFilters(ctx, profile, large); skip {
		t.Fatal("500 MB is over the floor, want no skip")
	}

	exact := store.JobRow{FilePath: path, FileSize: 100 * 1024 * 1024}
	if skip, _ := evaluateFilters(ctx, profile, exact); !skip {
		t.Fatal("the floor is inclusive, want a skip at exactly 100 MB")
	}

	unknown := store.JobRow{FilePath: path, FileSize: 0}
	if skip, _ := evaluateFilters(ctx, profile, unknown); skip {
		t.Fatal("an unknown file size must not trigger the size gate")
	}
}

func TestEvaluateFiltersKeepsTheJobWhenProbingFails(t *testing.T) {
	ctx := context.Background()
	missing := filepath.Join(t.TempDir(), "gone.mkv")
	profile := &store.ProfileRow{SkipCodecs: "hevc", SkipHeightPx: 720, SkipHDR: true}

	skip, reason := evaluateFilters(ctx, profile, store.JobRow{FilePath: missing, FileSize: 1 << 30})
	if skip {
		t.Fatalf("an unprobeable file must not be skipped (reason %q)", reason)
	}
}
