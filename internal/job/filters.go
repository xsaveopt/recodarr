package job

import (
	"context"
	"fmt"
	"strings"

	"github.com/sratabix/recodarr/internal/probe"
	"github.com/sratabix/recodarr/internal/store"
)

// evaluateFilters runs the profile's pre-encode skip rules against the source
// file. Returns (skip, reason) — when skip is true, the worker should mark the
// job as `skipped` with reason instead of encoding.
//
// Behavior when ffprobe is missing or fails:
//   - File-size rule still works (we have the size from *arr's webhook payload).
//   - Codec / bitrate / duration / height / HDR rules are silently dropped,
//     because we can't evaluate them without media info. We err on the side of
//     "encode anyway" rather than "skip everything" — wrong skips are worse
//     than wrong encodes here.
//
// Each rule is independent and short-circuits on first match so the reason is
// the first triggered filter.
func evaluateFilters(ctx context.Context, p *store.ProfileRow, j store.JobRow) (bool, string) {
	if !filtersConfigured(p) {
		return false, ""
	}

	// File size is cheap — we already have it. Check first so we can skip the
	// ffprobe entirely when the file's so small it's not worth probing.
	if p.SkipFileSizeMB > 0 && j.FileSize > 0 {
		mb := j.FileSize / (1024 * 1024)
		if mb <= int64(p.SkipFileSizeMB) {
			return true, fmt.Sprintf("file too small: %d MB ≤ %d MB", mb, p.SkipFileSizeMB)
		}
	}

	// Everything else needs ffprobe.
	needsProbe := p.SkipCodecs != "" ||
		p.SkipBitrateMBPerHour > 0 ||
		p.SkipDurationMinutes > 0 ||
		p.SkipHeightPx > 0 ||
		p.SkipHDR
	if !needsProbe {
		return false, ""
	}

	pr, err := probe.Run(ctx, j.FilePath)
	if err != nil {
		// Couldn't probe — fall through to encoding. The caller logs.
		return false, ""
	}

	if p.SkipCodecs != "" && pr.Codec != "" {
		for _, c := range strings.Split(p.SkipCodecs, ",") {
			if strings.EqualFold(strings.TrimSpace(c), pr.Codec) {
				return true, fmt.Sprintf("source codec %s is in skip list", pr.Codec)
			}
		}
	}

	if p.SkipBitrateMBPerHour > 0 {
		mbph := pr.MBPerHour()
		if mbph > 0 && mbph <= float64(p.SkipBitrateMBPerHour) {
			return true, fmt.Sprintf("source bitrate %.0f MB/hour ≤ %d MB/hour", mbph, p.SkipBitrateMBPerHour)
		}
	}

	if p.SkipDurationMinutes > 0 && pr.DurationSec > 0 {
		mins := int(pr.DurationSec / 60)
		if mins <= p.SkipDurationMinutes {
			return true, fmt.Sprintf("source duration %d min ≤ %d min", mins, p.SkipDurationMinutes)
		}
	}

	if p.SkipHeightPx > 0 && pr.Height > 0 && pr.Height <= p.SkipHeightPx {
		return true, fmt.Sprintf("source height %dpx ≤ %dpx", pr.Height, p.SkipHeightPx)
	}

	if p.SkipHDR && pr.IsHDR {
		return true, fmt.Sprintf("source is HDR (%s)", pr.ColorTransfer)
	}

	return false, ""
}

func filtersConfigured(p *store.ProfileRow) bool {
	return p.SkipCodecs != "" ||
		p.SkipBitrateMBPerHour > 0 ||
		p.SkipFileSizeMB > 0 ||
		p.SkipDurationMinutes > 0 ||
		p.SkipHeightPx > 0 ||
		p.SkipHDR
}
