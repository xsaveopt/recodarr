package handbrake

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestIsEncoderAvailableAsksTheBinary(t *testing.T) {
	stubHandBrake(t, `
[ "$1" = "--encoder-preset-list" ] || exit 2
case "$2" in
  x265|nvenc_h265) echo "presets for $2"; exit 0 ;;
esac
echo "unknown encoder $2" >&2
exit 1`)
	if !isEncoderAvailable("x265") || !isEncoderAvailable("nvenc_h265") {
		t.Fatal("encoders the binary lists must be reported available")
	}
	if isEncoderAvailable("qsv_h265") {
		t.Fatal("an encoder the binary rejects must be reported unavailable")
	}
}

func TestIsEncoderAvailableWithoutTheBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if isEncoderAvailable("x265") {
		t.Fatal("no HandBrakeCLI on PATH, want unavailable")
	}
}

func TestDiscoverCapsKeepsCatalogOrderAndDetails(t *testing.T) {
	stubHandBrake(t, `
case "$2" in
  x265|ffv1|qsv_av1|mf_h264|x264) exit 0 ;;
esac
exit 1`)
	caps := discoverCaps()

	var names []string
	for _, e := range caps.Encoders {
		names = append(names, e.Name)
	}
	want := []string{"x264", "x265", "ffv1", "qsv_av1", "mf_h264"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("got %v, want %v in known-encoder order", names, want)
	}
	for _, e := range caps.Encoders {
		if cat, ok := Catalog[e.Name]; ok {
			if !reflect.DeepEqual(e, cat) {
				t.Fatalf("%s: got %+v, want the catalog entry", e.Name, e)
			}
			continue
		}
		if !reflect.DeepEqual(e, EncoderCaps{Name: e.Name}) {
			t.Fatalf("%s is not in the catalog, want a bare entry, got %+v", e.Name, e)
		}
	}
}

func TestDiscoverCapsIsEmptyWithoutTheBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if caps := discoverCaps(); len(caps.Encoders) != 0 {
		t.Fatalf("got %+v, want no encoders", caps.Encoders)
	}
}

func TestDiscoverCapsProbesEveryKnownEncoderOnce(t *testing.T) {
	log := filepath.Join(t.TempDir(), "calls")
	stubHandBrake(t, `echo "$2" >> "`+log+`"
exit 1`)
	discoverCaps()

	body, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, line := range strings.Fields(string(body)) {
		seen[line]++
	}
	for _, enc := range knownVideoEncoders {
		if seen[enc] != 1 {
			t.Fatalf("%s probed %d times, want once", enc, seen[enc])
		}
	}
	if len(seen) != len(knownVideoEncoders) {
		t.Fatalf("probed %d distinct encoders, want %d", len(seen), len(knownVideoEncoders))
	}
}
