package job

import (
	"context"
	"fmt"
	"os"
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

func stubFfprobe(t *testing.T, doc string) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncat <<'EOF'\n" + doc + "\nEOF\n"
	if err := os.WriteFile(filepath.Join(dir, "ffprobe"), []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return filepath.Join(t.TempDir(), "media.mkv")
}

func probeDoc(codec string, height int, transfer string, durationSec, bitrateBps int64) string {
	return fmt.Sprintf(`{"format":{"duration":"%d","bit_rate":"%d"},"streams":[{"codec_type":"video","codec_name":%q,"width":1920,"height":%d,"color_transfer":%q}]}`,
		durationSec, bitrateBps, codec, height, transfer)
}

func TestEvaluateFiltersProbeBranches(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		doc     string
		profile store.ProfileRow
		skip    bool
		reason  string
	}{
		{
			name:    "codec in the skip list, case and spacing ignored",
			doc:     probeDoc("hevc", 1080, "bt709", 3600, 4_000_000),
			profile: store.ProfileRow{SkipCodecs: "av1, HEVC "},
			skip:    true,
			reason:  "source codec hevc is in skip list",
		},
		{
			name:    "codec not in the skip list",
			doc:     probeDoc("h264", 1080, "bt709", 3600, 4_000_000),
			profile: store.ProfileRow{SkipCodecs: "hevc,av1"},
		},
		{
			name:    "kbps bitrate at the limit",
			doc:     probeDoc("h264", 1080, "bt709", 3600, 2_000_000),
			profile: store.ProfileRow{SkipBitrateMBPerHour: 2000, SkipBitrateUnit: "kbps"},
			skip:    true,
			reason:  "source bitrate 2000 kbps ≤ 2000 kbps",
		},
		{
			name:    "kbps bitrate above the limit",
			doc:     probeDoc("h264", 1080, "bt709", 3600, 2_001_000),
			profile: store.ProfileRow{SkipBitrateMBPerHour: 2000, SkipBitrateUnit: "kbps"},
		},
		{
			name:    "kbps with an unknown bitrate",
			doc:     probeDoc("h264", 1080, "bt709", 3600, 0),
			profile: store.ProfileRow{SkipBitrateMBPerHour: 2000, SkipBitrateUnit: "kbps"},
		},
		{
			name:    "MB per hour under the limit",
			doc:     probeDoc("h264", 1080, "bt709", 3600, 8*1024*1024),
			profile: store.ProfileRow{SkipBitrateMBPerHour: 4000},
			skip:    true,
			reason:  "source bitrate 3600 MB/hour ≤ 4000 MB/hour",
		},
		{
			name:    "MB per hour over the limit",
			doc:     probeDoc("h264", 1080, "bt709", 3600, 8*1024*1024),
			profile: store.ProfileRow{SkipBitrateMBPerHour: 3000},
		},
		{
			name:    "duration at the limit",
			doc:     probeDoc("h264", 1080, "bt709", 1500, 4_000_000),
			profile: store.ProfileRow{SkipDurationMinutes: 25},
			skip:    true,
			reason:  "source duration 25 min ≤ 25 min",
		},
		{
			name:    "duration over the limit",
			doc:     probeDoc("h264", 1080, "bt709", 2700, 4_000_000),
			profile: store.ProfileRow{SkipDurationMinutes: 25},
		},
		{
			name:    "height at the limit",
			doc:     probeDoc("h264", 720, "bt709", 3600, 4_000_000),
			profile: store.ProfileRow{SkipHeightPx: 720},
			skip:    true,
			reason:  "source height 720px ≤ 720px",
		},
		{
			name:    "height over the limit",
			doc:     probeDoc("h264", 1080, "bt709", 3600, 4_000_000),
			profile: store.ProfileRow{SkipHeightPx: 720},
		},
		{
			name:    "PQ HDR source",
			doc:     probeDoc("hevc", 2160, "smpte2084", 3600, 4_000_000),
			profile: store.ProfileRow{SkipHDR: true},
			skip:    true,
			reason:  "source is HDR (smpte2084)",
		},
		{
			name:    "HLG HDR source",
			doc:     probeDoc("hevc", 2160, "arib-std-b67", 3600, 4_000_000),
			profile: store.ProfileRow{SkipHDR: true},
			skip:    true,
			reason:  "source is HDR (arib-std-b67)",
		},
		{
			name:    "SDR source with the HDR filter on",
			doc:     probeDoc("hevc", 2160, "bt709", 3600, 4_000_000),
			profile: store.ProfileRow{SkipHDR: true},
		},
		{
			name:    "size gate passes so the probe filters decide",
			doc:     probeDoc("hevc", 1080, "bt709", 3600, 4_000_000),
			profile: store.ProfileRow{SkipFileSizeMB: 1, SkipCodecs: "hevc"},
			skip:    true,
			reason:  "source codec hevc is in skip list",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := stubFfprobe(t, tc.doc)
			skip, reason := evaluateFilters(ctx, &tc.profile, store.JobRow{FilePath: path, FileSize: 1 << 30})
			if skip != tc.skip || reason != tc.reason {
				t.Fatalf("got skip=%v reason=%q, want skip=%v reason=%q", skip, reason, tc.skip, tc.reason)
			}
		})
	}
}

func TestEvaluateFiltersSizeGateNeedsNoProbe(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	skip, reason := evaluateFilters(context.Background(), &store.ProfileRow{SkipFileSizeMB: 10}, store.JobRow{FilePath: "/media/a.mkv", FileSize: 20 * 1024 * 1024})
	if skip || reason != "" {
		t.Fatalf("got skip=%v reason=%q, want no skip and no probe needed", skip, reason)
	}
}
