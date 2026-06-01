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

var ErrNotInstalled = errors.New("ffprobe not found on PATH")

type Probe struct {
	Codec         string
	Height        int
	Width         int
	DurationSec   float64
	BitrateBps    int64
	IsHDR         bool
	ColorTransfer string

	AudioChannels []int
}

func (p Probe) MBPerHour() float64 {
	if p.DurationSec <= 0 || p.BitrateBps <= 0 {
		return 0
	}
	bytesPerSec := float64(p.BitrateBps) / 8
	bytesPerHour := bytesPerSec * 3600
	return bytesPerHour / (1024 * 1024)
}

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

		p.Codec = strings.ToLower(s.CodecName)
		p.Width = s.Width
		p.Height = s.Height
		p.ColorTransfer = strings.ToLower(s.ColorTransfer)

		switch p.ColorTransfer {
		case "smpte2084", "arib-std-b67":
			p.IsHDR = true
		}

		if p.BitrateBps == 0 {
			if b, err := strconv.ParseInt(s.BitRate, 10, 64); err == nil {
				p.BitrateBps = b
			}
		}
		gotVideo = true
	}
	return p, nil
}
