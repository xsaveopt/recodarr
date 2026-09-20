package probe

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestMBPerHour(t *testing.T) {
	p := Probe{DurationSec: 3600, BitrateBps: 8 * 1024 * 1024}
	if got := p.MBPerHour(); math.Abs(got-3600) > 0.001 {
		t.Fatalf("got %v MB/hour, want 3600", got)
	}
	for _, bad := range []Probe{
		{},
		{DurationSec: 0, BitrateBps: 1000},
		{DurationSec: 100, BitrateBps: 0},
		{DurationSec: -1, BitrateBps: 1000},
	} {
		if got := bad.MBPerHour(); got != 0 {
			t.Fatalf("%+v produced %v, want 0", bad, got)
		}
	}
}

func stubFfprobe(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "ffprobe")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func stubFfprobeJSON(t *testing.T, doc string) {
	t.Helper()
	stubFfprobe(t, fmt.Sprintf("cat <<'EOF'\n%s\nEOF", doc))
}

func TestRunReportsAMissingBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := Run(context.Background(), "/media/a.mkv")
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("got %v, want ErrNotInstalled", err)
	}
}

func TestRunParsesAFullDocument(t *testing.T) {
	stubFfprobeJSON(t, `{
  "format": {"duration": "2700.5", "bit_rate": "5000000", "size": "1687812500"},
  "streams": [
    {"codec_type": "video", "codec_name": "HEVC", "width": 3840, "height": 2160,
     "color_transfer": "SMPTE2084", "bit_rate": "4800000"},
    {"codec_type": "audio", "channels": 6},
    {"codec_type": "audio", "channels": 2},
    {"codec_type": "subtitle"}
  ]
}`)
	p, err := Run(context.Background(), "/media/a.mkv")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if p.Codec != "hevc" {
		t.Fatalf("got codec %q, want it lower-cased", p.Codec)
	}
	if p.Width != 3840 || p.Height != 2160 {
		t.Fatalf("got %dx%d", p.Width, p.Height)
	}
	if p.DurationSec != 2700.5 {
		t.Fatalf("got duration %v", p.DurationSec)
	}
	if p.BitrateBps != 5000000 {
		t.Fatalf("got bitrate %d, want the format bitrate preferred", p.BitrateBps)
	}
	if !p.IsHDR || p.ColorTransfer != "smpte2084" {
		t.Fatalf("got hdr=%v transfer=%q, want HDR detected", p.IsHDR, p.ColorTransfer)
	}
	if !slices.Equal(p.AudioChannels, []int{6, 2}) {
		t.Fatalf("got audio channels %v, want [6 2] in stream order", p.AudioChannels)
	}
}

func TestRunDetectsHLG(t *testing.T) {
	stubFfprobeJSON(t, `{
  "format": {"duration": "60"},
  "streams": [{"codec_type": "video", "codec_name": "hevc", "color_transfer": "arib-std-b67"}]
}`)
	p, err := Run(context.Background(), "/media/a.mkv")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !p.IsHDR {
		t.Fatal("HLG was not treated as HDR")
	}
}

func TestRunTreatsSDRTransfersAsSDR(t *testing.T) {
	for _, transfer := range []string{"bt709", "", "smpte170m"} {
		stubFfprobeJSON(t, fmt.Sprintf(`{
  "format": {"duration": "60"},
  "streams": [{"codec_type": "video", "codec_name": "h264", "color_transfer": %q}]
}`, transfer))
		p, err := Run(context.Background(), "/media/a.mkv")
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if p.IsHDR {
			t.Fatalf("transfer %q was reported as HDR", transfer)
		}
	}
}

func TestRunDerivesBitrateFromSizeWhenTheFormatOmitsIt(t *testing.T) {
	stubFfprobeJSON(t, `{
  "format": {"duration": "100", "size": "12500"},
  "streams": [{"codec_type": "video", "codec_name": "h264"}]
}`)
	p, err := Run(context.Background(), "/media/a.mkv")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if p.BitrateBps != 1000 {
		t.Fatalf("got %d bps, want it derived from size and duration", p.BitrateBps)
	}
}

func TestRunFallsBackToTheStreamBitrate(t *testing.T) {
	stubFfprobeJSON(t, `{
  "format": {},
  "streams": [{"codec_type": "video", "codec_name": "h264", "bit_rate": "777"}]
}`)
	p, err := Run(context.Background(), "/media/a.mkv")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if p.BitrateBps != 777 {
		t.Fatalf("got %d bps, want the stream bitrate", p.BitrateBps)
	}
}

func TestRunUsesOnlyTheFirstVideoStream(t *testing.T) {
	stubFfprobeJSON(t, `{
  "format": {"duration": "10"},
  "streams": [
    {"codec_type": "video", "codec_name": "h264", "height": 1080},
    {"codec_type": "video", "codec_name": "mjpeg", "height": 300}
  ]
}`)
	p, err := Run(context.Background(), "/media/a.mkv")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if p.Codec != "h264" || p.Height != 1080 {
		t.Fatalf("got %s at %dp, want the first video stream to win over the cover art", p.Codec, p.Height)
	}
}

func TestRunOnAFileWithNoVideoStream(t *testing.T) {
	stubFfprobeJSON(t, `{"format": {"duration": "10"}, "streams": [{"codec_type": "audio", "channels": 2}]}`)
	p, err := Run(context.Background(), "/media/a.mka")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if p.Codec != "" || p.Height != 0 {
		t.Fatalf("got %+v, want no video details", p)
	}
	if !slices.Equal(p.AudioChannels, []int{2}) {
		t.Fatalf("got %v, want the audio track still reported", p.AudioChannels)
	}
}

func TestRunSurfacesAFailedProbe(t *testing.T) {
	stubFfprobe(t, "echo 'moov atom not found' >&2\nexit 1")
	if _, err := Run(context.Background(), "/media/a.mkv"); err == nil {
		t.Fatal("a failing ffprobe was reported as success")
	}
}

func TestRunSurfacesMalformedJSON(t *testing.T) {
	stubFfprobe(t, "echo 'not json'")
	_, err := Run(context.Background(), "/media/a.mkv")
	if err == nil {
		t.Fatal("a malformed document was accepted")
	}
}

func TestRunPassesThePathToFfprobe(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	stubFfprobe(t, fmt.Sprintf(`printf '%%s\n' "$@" > %q
echo '{"format":{},"streams":[]}'`, argsFile))

	if _, err := Run(context.Background(), "/media/My Show/a.mkv"); err != nil {
		t.Fatalf("run: %v", err)
	}
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	got := string(raw)
	for _, want := range []string{"-print_format", "json", "-show_format", "-show_streams", "/media/My Show/a.mkv"} {
		if !slices.Contains(splitLines(got), want) {
			t.Fatalf("ffprobe was called with %q, missing %q", got, want)
		}
	}
}

func splitLines(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '\n' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
