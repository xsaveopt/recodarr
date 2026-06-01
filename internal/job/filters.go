package job

import (
	"context"
	"fmt"
	"strings"

	"github.com/sratabix/recodarr/internal/probe"
	"github.com/sratabix/recodarr/internal/store"
)

func evaluateFilters(ctx context.Context, p *store.ProfileRow, j store.JobRow) (bool, string) {
	if !filtersConfigured(p) {
		return false, ""
	}

	if p.SkipFileSizeMB > 0 && j.FileSize > 0 {
		mb := j.FileSize / (1024 * 1024)
		if mb <= int64(p.SkipFileSizeMB) {
			return true, fmt.Sprintf("file too small: %d MB ≤ %d MB", mb, p.SkipFileSizeMB)
		}
	}

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
		if p.SkipBitrateUnit == "kbps" {
			if pr.BitrateBps > 0 {
				kbps := pr.BitrateBps / 1000
				if kbps <= int64(p.SkipBitrateMBPerHour) {
					return true, fmt.Sprintf("source bitrate %d kbps ≤ %d kbps", kbps, p.SkipBitrateMBPerHour)
				}
			}
		} else {
			mbph := pr.MBPerHour()
			if mbph > 0 && mbph <= float64(p.SkipBitrateMBPerHour) {
				return true, fmt.Sprintf("source bitrate %.0f MB/hour ≤ %d MB/hour", mbph, p.SkipBitrateMBPerHour)
			}
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
