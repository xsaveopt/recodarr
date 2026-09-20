package audio

import (
	"slices"
	"testing"
)

func TestDefaultsForEncoder(t *testing.T) {
	if got := DefaultsForEncoder("opus"); got[2] != DefaultsOpus[2] {
		t.Fatalf("got %v, want the opus table", got)
	}
	for _, enc := range []string{"", "aac", "av_aac", "ca_aac", "anything"} {
		if got := DefaultsForEncoder(enc); got[2] != DefaultsAAC[2] {
			t.Fatalf("encoder %q got %v, want the aac table", enc, got)
		}
	}
}

func TestDefaultTablesScaleWithChannels(t *testing.T) {
	for name, table := range map[string]map[int]int{"aac": DefaultsAAC, "opus": DefaultsOpus} {
		for ch := 1; ch <= 8; ch++ {
			if table[ch] <= 0 {
				t.Fatalf("%s has no bitrate for %d channels", name, ch)
			}
			if ch > 1 && table[ch] <= table[ch-1] {
				t.Fatalf("%s at %d channels (%d) is not above %d channels (%d)",
					name, ch, table[ch], ch-1, table[ch-1])
			}
		}
	}
	if DefaultsOpus[2] >= DefaultsAAC[2] {
		t.Fatal("opus is not cheaper than aac at stereo")
	}
}

func TestResolveBitratesFallsBackToDefaults(t *testing.T) {
	got := ResolveBitrates("", "aac", []int{2, 6})
	want := []int{DefaultsAAC[2], DefaultsAAC[6]}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := ResolveBitrates("{}", "opus", []int{6}); got[0] != DefaultsOpus[6] {
		t.Fatalf("got %v, want the opus default for 5.1", got)
	}
}

func TestResolveBitratesPrefersTheProfileMap(t *testing.T) {
	got := ResolveBitrates(`{"2":192,"6":640}`, "aac", []int{2, 6, 8})
	if got[0] != 192 || got[1] != 640 {
		t.Fatalf("got %v, want the configured values used", got)
	}
	if got[2] != DefaultsAAC[8] {
		t.Fatalf("got %v, want an unconfigured channel count to fall back", got)
	}
}

func TestResolveBitratesIgnoresZeroAndNegativeOverrides(t *testing.T) {
	got := ResolveBitrates(`{"2":0,"6":-5}`, "aac", []int{2, 6})
	if got[0] != DefaultsAAC[2] || got[1] != DefaultsAAC[6] {
		t.Fatalf("got %v, want non-positive overrides ignored", got)
	}
}

func TestResolveBitratesIgnoresMalformedProfileJSON(t *testing.T) {
	for _, raw := range []string{"not json", `{"2":"loud"}`, `[]`, `{"stereo":192}`} {
		got := ResolveBitrates(raw, "aac", []int{2})
		if got[0] != DefaultsAAC[2] {
			t.Fatalf("profile %q produced %v, want the default", raw, got)
		}
	}
}

func TestResolveBitratesTreatsUnknownChannelsAsStereo(t *testing.T) {
	got := ResolveBitrates("", "aac", []int{0, -1})
	for i, v := range got {
		if v != DefaultsAAC[2] {
			t.Fatalf("track %d got %d, want the stereo default", i, v)
		}
	}
	configured := ResolveBitrates(`{"2":111}`, "aac", []int{0})
	if configured[0] != 111 {
		t.Fatalf("got %v, want a zero channel count to use the stereo override", configured)
	}
}

func TestResolveBitratesExtrapolatesBeyondTheTable(t *testing.T) {
	got := ResolveBitrates("", "aac", []int{12})
	want := (DefaultsAAC[2] / 2) * 12
	if got[0] != want {
		t.Fatalf("got %d for 12 channels, want %d", got[0], want)
	}
}

func TestResolveBitratesKeepsOneEntryPerTrack(t *testing.T) {
	if got := ResolveBitrates("", "aac", nil); len(got) != 0 {
		t.Fatalf("got %v, want no entries for no tracks", got)
	}
	if got := ResolveBitrates("", "aac", []int{2, 2, 6, 8}); len(got) != 4 {
		t.Fatalf("got %d entries, want one per track", len(got))
	}
}
