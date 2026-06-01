package audio

import "encoding/json"

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

func DefaultsForEncoder(encoder string) map[int]int {
	switch encoder {
	case "opus":
		return DefaultsOpus
	default:
		return DefaultsAAC
	}
}

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

		if stereo, ok := defaults[2]; ok && stereo > 0 {
			out[i] = (stereo / 2) * key
		}
	}
	return out
}

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
