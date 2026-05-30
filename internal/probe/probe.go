// Package probe wraps ffprobe to extract the few fields Recodarr needs to
// decide whether a file is worth re-encoding (codec, resolution, duration,
// bitrate, HDR transfer). The dependency is optional at runtime: if ffprobe
// isn't on PATH, callers receive ErrNotInstalled and should treat it as
// "filters can't be evaluated" rather than failing the encode.
package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ErrNotInstalled is returned when ffprobe isn't on PATH. Callers should log
// once and continue without filtering — the user may have intentionally
// disabled probing by not installing ffprobe.
var ErrNotInstalled = errors.New("ffprobe not found on PATH")

// Probe is the subset of media info Recodarr inspects. All numeric fields are
// 0 when ffprobe couldn't extract them; callers must treat 0 as "unknown" and
// skip the corresponding filter rather than firing it on a zero.
type Probe struct {
	Codec         string // lowercase codec_name, e.g. "av1", "hevc", "h264"
	Height        int    // pixels
	Width         int    // pixels
	DurationSec   float64
	BitrateBps    int64
	IsHDR         bool   // true if color_transfer is PQ (smpte2084) or HLG (arib-std-b67)
	ColorTransfer string // raw value for diagnostics
	// AudioChannels lists the channel count of each audio stream in the order
	// ffprobe reports them — which is also the order HandBrake numbers tracks
	// for --ab/--mixdown lists. 0 means the channel count couldn't be parsed
	// for that stream.
	AudioChannels []int
}

// MBPerHour returns bitrate expressed as megabytes per hour. Used by the
// "bitrate floor" filter, which is much more intuitive in MB/h than in bps for
// users staring at file managers all day. Returns 0 if duration unknown.
func (p Probe) MBPerHour() float64 {
	if p.DurationSec <= 0 || p.BitrateBps <= 0 {
		return 0
	}
	bytesPerSec := float64(p.BitrateBps) / 8
	bytesPerHour := bytesPerSec * 3600
	return bytesPerHour / (1024 * 1024)
}

// ffprobe JSON shape — only the fields we touch.
type ffprobeOutput struct {
	Format struct {
		Duration string `json:"duration"`
		BitRate  string `json:"bit_rate"`
		Size     string `json:"size"`
	} `json:"format"`
	Streams []struct {
		CodecType     string `json:"codec_type"`
		CodecName     string `json:"codec_name"`
		Width         int    `json:"width"`
		Height        int    `json:"height"`
		ColorTransfer string `json:"color_transfer"`
		BitRate       string `json:"bit_rate"`
		Channels      int    `json:"channels"`
	} `json:"streams"`
}

// Run probes the given media file. The 30s timeout is generous: ffprobe normally
// returns in well under a second, but containers with long index parsing
// (multi-hour MKVs with many subtitle/audio tracks) occasionally need a few.
func Run(ctx context.Context, path string) (Probe, error) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		return Probe{}, ErrNotInstalled
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return Probe{}, fmt.Errorf("ffprobe: %w", err)
	}

	var raw ffprobeOutput
	if err := json.Unmarshal(out, &raw); err != nil {
		return Probe{}, fmt.Errorf("parse ffprobe json: %w", err)
	}

	p := Probe{}
	if d, err := strconv.ParseFloat(raw.Format.Duration, 64); err == nil {
		p.DurationSec = d
	}
	// Format-level bit_rate is the container's reported overall bitrate. If
	// missing (some MKVs), fall back to size/duration.
	if b, err := strconv.ParseInt(raw.Format.BitRate, 10, 64); err == nil && b > 0 {
		p.BitrateBps = b
	} else if size, err := strconv.ParseInt(raw.Format.Size, 10, 64); err == nil && size > 0 && p.DurationSec > 0 {
		p.BitrateBps = int64(float64(size) * 8 / p.DurationSec)
	}

	gotVideo := false
	for _, s := range raw.Streams {
		if s.CodecType == "audio" {
			p.AudioChannels = append(p.AudioChannels, s.Channels)
			continue
		}
		if s.CodecType != "video" || gotVideo {
			continue
		}
		// First video stream wins. Modern files almost never have multiple
		// video tracks, and when they do (multi-angle Blu-rays), the first
		// is always the canonical one. Keep iterating so audio streams after
		// the video (the typical container layout) still get collected.
		p.Codec = strings.ToLower(s.CodecName)
		p.Width = s.Width
		p.Height = s.Height
		p.ColorTransfer = strings.ToLower(s.ColorTransfer)
		// HDR detection: PQ (HDR10/HDR10+/Dolby Vision profile 5+8) and HLG.
		// SDR uses bt709, smpte170m, etc. — those don't qualify.
		switch p.ColorTransfer {
		case "smpte2084", "arib-std-b67":
			p.IsHDR = true
		}
		// Fall back to stream bitrate if format didn't have one.
		if p.BitrateBps == 0 {
			if b, err := strconv.ParseInt(s.BitRate, 10, 64); err == nil {
				p.BitrateBps = b
			}
		}
		gotVideo = true
	}
	return p, nil
}
