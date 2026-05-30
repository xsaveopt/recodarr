// Package audio handles per-channel-count audio bitrate selection for the
// keep-source-layout mixdown mode. When the user hasn't picked a specific
// mixdown, every output track may have a different channel count (stereo, 5.1,
// 7.1, ...) and applying a single flat --ab is wrong: 96 kbps is plenty for
// stereo but starves 5.1. We resolve per-track bitrates by looking up each
// source track's channel count against the profile's configured map (or the
// encoder's defaults), then hand HandBrake the comma-separated --ab list.
package audio

import "encoding/json"

// DefaultsAAC are the per-channel-count kbps defaults for AAC encoders
// (av_aac, fdk_aac) — roughly 64 kbps/channel, which is widely cited as the
// AAC LC quality sweet spot.
var DefaultsAAC = map[int]int{
	1: 64,
	2: 128,
	3: 192,
	4: 256,
	5: 320,
	6: 384,
	7: 448,
	8: 512,
}

// DefaultsOpus are the per-channel-count kbps defaults for Opus. Opus is
// ~30–40% more efficient than AAC, so the curve sits lower. 96 stereo and 256
// 5.1 are the industry-cited "transparent" values; the rest interpolate on a
// ~48 kbps/channel curve.
var DefaultsOpus = map[int]int{
	1: 48,
	2: 96,
	3: 144,
	4: 192,
	5: 224,
	6: 256,
	7: 320,
	8: 384,
}

// DefaultsForEncoder returns the bitrate-per-channel-count map appropriate for
// the given audio encoder. Unknown encoders fall back to the AAC curve, which
// is reasonable for any general-purpose lossy codec (MP3, Vorbis, AC-3 all
// land near AAC's per-channel rates).
func DefaultsForEncoder(encoder string) map[int]int {
	switch encoder {
	case "opus":
		return DefaultsOpus
	default:
		return DefaultsAAC
	}
}

// ResolveBitrates returns the kbps to apply to each source audio track given
// the profile's stored map (JSON of channel-count → kbps) and the encoder. An
// empty/invalid map falls back to encoder defaults; a partial map fills any
// missing channel counts from defaults. The output is aligned 1:1 with the
// sourceChannels slice so callers can pass it straight to HandBrake's --ab as
// a comma-separated list. A 0 source-channel value (probe couldn't read it)
// resolves to the stereo bitrate as a safe middle ground.
func ResolveBitrates(profileJSON, encoder string, sourceChannels []int) []int {
	defaults := DefaultsForEncoder(encoder)
	configured := parseMap(profileJSON)
	out := make([]int, len(sourceChannels))
	for i, ch := range sourceChannels {
		key := ch
		if key <= 0 {
			key = 2
		}
		if v, ok := configured[key]; ok && v > 0 {
			out[i] = v
			continue
		}
		if v, ok := defaults[key]; ok {
			out[i] = v
			continue
		}
		// Channel count beyond the 1–8 we ship defaults for. Extrapolate from
		// stereo at the same per-channel rate so we still produce something
		// sensible for exotic layouts.
		if stereo, ok := defaults[2]; ok && stereo > 0 {
			out[i] = (stereo / 2) * key
		}
	}
	return out
}

// parseMap decodes the stored JSON. Tolerant of empty / malformed input — a
// failure here just means "no overrides, use defaults", never a hard error.
// JSON object keys are always strings; we convert to int.
func parseMap(s string) map[int]int {
	if s == "" || s == "{}" {
		return nil
	}
	var raw map[string]int
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil
	}
	out := make(map[int]int, len(raw))
	for k, v := range raw {
		// Manual atoi to avoid importing strconv for one call; keys are tiny.
		n := 0
		for _, c := range k {
			if c < '0' || c > '9' {
				n = 0
				break
			}
			n = n*10 + int(c-'0')
		}
		if n > 0 {
			out[n] = v
		}
	}
	return out
}
