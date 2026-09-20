package handbrake

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func argValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func TestTempPathIsASiblingHiddenFile(t *testing.T) {
	got := TempPath("/media/Show/S01E01.mkv", "")
	want := "/media/Show/.S01E01.mkv.recodarr.tmp.mkv"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if filepath.Dir(got) != "/media/Show" {
		t.Fatalf("the temp file left the source directory: %q", got)
	}
}

func TestTempPathHonoursTheMp4Container(t *testing.T) {
	got := TempPath("/media/Show/S01E01.mkv", "mp4")
	if filepath.Ext(got) != ".mp4" {
		t.Fatalf("got %q, want an .mp4 extension", got)
	}
	if got := TempPath("/media/Show/S01E01.mkv", "mkv"); filepath.Ext(got) != ".mkv" {
		t.Fatalf("got %q, want the source extension kept", got)
	}
}

func TestIsTempPathRecognizesOurOwnFiles(t *testing.T) {
	for _, p := range []string{
		"/media/.S01E01.mkv.recodarr.tmp.mkv",
		TempPath("/media/a.mkv", ""),
		TempPath("/media/a.mkv", "mp4"),
	} {
		if !IsTempPath(p) {
			t.Fatalf("IsTempPath(%q) = false, want true", p)
		}
	}
	for _, p := range []string{
		"/media/S01E01.mkv",
		"/media/.hidden.mkv",
		"/media/recodarr.tmp.mkv",
		"",
	} {
		if IsTempPath(p) {
			t.Fatalf("IsTempPath(%q) = true, want false", p)
		}
	}
}

func TestBuildArgsDefaults(t *testing.T) {
	args := buildArgs("/in.mkv", "/out.mkv", Settings{})
	if v, _ := argValue(args, "-e"); v != "x265" {
		t.Fatalf("got encoder %q, want the x265 default", v)
	}
	if v, _ := argValue(args, "-f"); v != "mkv" {
		t.Fatalf("got format %q, want the mkv default", v)
	}
	if v, _ := argValue(args, "-q"); v != "22" {
		t.Fatalf("got quality %q, want the default 22", v)
	}
	if v, _ := argValue(args, "-i"); v != "/in.mkv" {
		t.Fatalf("got input %q", v)
	}
	if v, _ := argValue(args, "-o"); v != "/out.mkv" {
		t.Fatalf("got output %q", v)
	}
	if !slices.Contains(args, "--cfr") {
		t.Fatalf("got %v, want --cfr always present", args)
	}
}

func TestBuildArgsRateControl(t *testing.T) {
	abr := buildArgs("/in.mkv", "/out.mkv", Settings{RateControl: "ABR", VideoBitrate: 4000, Quality: 18})
	if v, _ := argValue(abr, "--vb"); v != "4000" {
		t.Fatalf("got --vb %q, want 4000", v)
	}
	if slices.Contains(abr, "-q") {
		t.Fatalf("got %v, want no -q alongside --vb", abr)
	}

	cq := buildArgs("/in.mkv", "/out.mkv", Settings{RateControl: "cq", Quality: 28})
	if v, _ := argValue(cq, "-q"); v != "28" {
		t.Fatalf("got -q %q, want 28", v)
	}
	if slices.Contains(cq, "--vb") {
		t.Fatalf("got %v, want no --vb in CQ mode", cq)
	}
}

func TestBuildArgsHardwareDecodePairing(t *testing.T) {
	for encoder, want := range map[string]string{
		"nvenc_h265": "nvdec",
		"qsv_av1":    "qsv",
		"vce_h265":   "vaapi",
		"vt_h264":    "vaapi",
	} {
		args := buildArgs("/in.mkv", "/out.mkv", Settings{Encoder: encoder})
		got, ok := argValue(args, "--enable-hw-decoding")
		if !ok || got != want {
			t.Fatalf("encoder %s got hw decode %q, want %q", encoder, got, want)
		}
	}
	for _, encoder := range []string{"x265", "x264", "svt_av1", "VP9"} {
		args := buildArgs("/in.mkv", "/out.mkv", Settings{Encoder: encoder})
		if slices.Contains(args, "--enable-hw-decoding") {
			t.Fatalf("software encoder %s asked for hardware decode: %v", encoder, args)
		}
	}
}

func TestBuildArgsAudio(t *testing.T) {
	perTrack := buildArgs("/in.mkv", "/out.mkv", Settings{
		AudioEncoder: "opus", AudioBitrate: 128, AudioBitratesPerTrack: []int{96, 256, 128},
	})
	if v, _ := argValue(perTrack, "--ab"); v != "96,256,128" {
		t.Fatalf("got --ab %q, want the per-track list", v)
	}
	if !slices.Contains(perTrack, "--all-audio") {
		t.Fatalf("got %v, want --all-audio", perTrack)
	}

	flat := buildArgs("/in.mkv", "/out.mkv", Settings{AudioEncoder: "aac", AudioBitrate: 160})
	if v, _ := argValue(flat, "--ab"); v != "160" {
		t.Fatalf("got --ab %q, want the flat bitrate", v)
	}

	copied := buildArgs("/in.mkv", "/out.mkv", Settings{
		AudioEncoder: "copy", AudioBitrate: 160, AudioBitratesPerTrack: []int{96},
	})
	if slices.Contains(copied, "--ab") {
		t.Fatalf("got %v, want no bitrate when the audio is copied", copied)
	}

	none := buildArgs("/in.mkv", "/out.mkv", Settings{AudioBitrate: 160})
	if slices.Contains(none, "--aencoder") || slices.Contains(none, "--ab") {
		t.Fatalf("got %v, want audio untouched when no encoder is set", none)
	}
}

func TestBuildArgsOptionalFlags(t *testing.T) {
	full := buildArgs("/in.mkv", "/out.mkv", Settings{
		Encoder: "x265", EncoderPreset: "slow", EncoderProfile: "main10",
		EncoderTune: "grain", EncoderLevel: "5.1",
		MaxWidth: 1920, MaxHeight: 1080,
		AudioEncoder: "opus", AudioMixdown: "stereo",
		SubtitleCopy: true, TwoPass: true, Framerate: "24",
	})
	for flag, want := range map[string]string{
		"--encoder-preset":  "slow",
		"--encoder-profile": "main10",
		"--encoder-tune":    "grain",
		"--encoder-level":   "5.1",
		"--maxWidth":        "1920",
		"--maxHeight":       "1080",
		"--mixdown":         "stereo",
		"-r":                "24",
	} {
		if got, ok := argValue(full, flag); !ok || got != want {
			t.Fatalf("%s = %q, want %q", flag, got, want)
		}
	}
	for _, flag := range []string{"--all-subtitles", "--two-pass", "--turbo"} {
		if !slices.Contains(full, flag) {
			t.Fatalf("got %v, want %s", full, flag)
		}
	}

	bare := buildArgs("/in.mkv", "/out.mkv", Settings{})
	for _, flag := range []string{
		"--encoder-preset", "--encoder-profile", "--encoder-tune", "--encoder-level",
		"--maxWidth", "--maxHeight", "--mixdown", "--all-subtitles", "--two-pass", "-r",
	} {
		if slices.Contains(bare, flag) {
			t.Fatalf("got %v, want %s omitted when unset", bare, flag)
		}
	}
}

func TestBuildArgsPassesExtraArgsThroughVerbatim(t *testing.T) {
	args := buildArgs("/in.mkv", "/out.mkv", Settings{
		Encoder:   "qsv_av1",
		ExtraArgs: `--encopts la=1:la-depth=40 --audio-name "5.1 Surround"`,
	})
	if v, ok := argValue(args, "--encopts"); !ok || v != "la=1:la-depth=40" {
		t.Fatalf("got --encopts %q, want it passed through unrewritten", v)
	}
	if v, ok := argValue(args, "--audio-name"); !ok || v != "5.1 Surround" {
		t.Fatalf("got --audio-name %q, want the quoted value", v)
	}
}

func TestParseProgressLine(t *testing.T) {
	p, ok := parseProgressLine("Encoding: task 1 of 1, 42.35 % (120.50 fps, avg 118.00 fps, ETA 00h12m30s)")
	if !ok {
		t.Fatal("a real progress line was not recognized")
	}
	if p.Percent != 42.35 {
		t.Fatalf("got %v%%, want 42.35", p.Percent)
	}
	if p.FPS != 120.50 {
		t.Fatalf("got %v fps, want 120.50", p.FPS)
	}
	if p.ETA != "00h12m30s" {
		t.Fatalf("got ETA %q", p.ETA)
	}

	for _, line := range []string{
		"",
		"Encoding: task 1 of 1",
		"no percent here",
		"x265 [info]: frame I:  1234",
	} {
		if _, ok := parseProgressLine(line); ok {
			t.Fatalf("parseProgressLine(%q) claimed a match", line)
		}
	}
}

func TestParseWorkResult(t *testing.T) {
	for _, tc := range []struct {
		log  string
		want int
		ok   bool
	}{
		{"...\nEncode done!\nHandBrake has exited.\nwork result = 0\n", 0, true},
		{"work result = 3", 3, true},
		{"work result = -1", -1, true},
		{"work result = 0\nwork result = 4\n", 4, true},
		{"no marker at all", 0, false},
		{"work result = ", 0, false},
		{"work result = abc", 0, false},
	} {
		got, ok := parseWorkResult(tc.log)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("parseWorkResult(%q) = (%d, %v), want (%d, %v)", tc.log, got, ok, tc.want, tc.ok)
		}
	}
}

func TestSplitOnCRorLF(t *testing.T) {
	adv, tok, err := splitOnCRorLF([]byte("first\rsecond\n"), false)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if string(tok) != "first" || adv != 6 {
		t.Fatalf("got (%d, %q), want the carriage return to end the token", adv, tok)
	}

	adv, tok, _ = splitOnCRorLF([]byte("partial"), false)
	if adv != 0 || tok != nil {
		t.Fatalf("got (%d, %q), want an incomplete token to wait for more data", adv, tok)
	}

	adv, tok, _ = splitOnCRorLF([]byte("tail"), true)
	if adv != 4 || string(tok) != "tail" {
		t.Fatalf("got (%d, %q), want the trailing token at EOF", adv, tok)
	}
}

func TestCommitRenamesAndDiscardRemoves(t *testing.T) {
	dir := t.TempDir()
	tmp := filepath.Join(dir, ".a.mkv.recodarr.tmp.mkv")
	final := filepath.Join(dir, "a.mkv")
	if err := os.WriteFile(tmp, []byte("encoded"), 0o644); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	if err := Commit(tmp, final); err != nil {
		t.Fatalf("commit: %v", err)
	}
	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatalf("read final: %v", err)
	}
	if string(got) != "encoded" {
		t.Fatalf("got %q, want the encoded bytes", got)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatal("the temp file survived the commit")
	}

	again := filepath.Join(dir, ".b.mkv.recodarr.tmp.mkv")
	if err := os.WriteFile(again, []byte("x"), 0o644); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	DiscardTemp(again)
	if _, err := os.Stat(again); !os.IsNotExist(err) {
		t.Fatal("DiscardTemp left the file behind")
	}
	DiscardTemp(filepath.Join(dir, "never-existed"))
}

func TestCommitFailsWhenTheTempIsGone(t *testing.T) {
	dir := t.TempDir()
	if err := Commit(filepath.Join(dir, "missing"), filepath.Join(dir, "final.mkv")); err == nil {
		t.Fatal("Commit succeeded with no source file")
	}
}

func TestRunRejectsAbrWithoutABitrate(t *testing.T) {
	_, err := Run(context.Background(), "/nonexistent.mkv",
		Settings{RateControl: "abr", VideoBitrate: 0}, nil, nil)
	if err == nil {
		t.Fatal("ABR with a zero bitrate was accepted")
	}
	if !strings.Contains(err.Error(), "video bitrate is 0") {
		t.Fatalf("got %v, want the bitrate to be named", err)
	}
}

func TestRunRejectsAMissingInput(t *testing.T) {
	_, err := Run(context.Background(), filepath.Join(t.TempDir(), "nope.mkv"), Settings{}, nil, nil)
	if err == nil {
		t.Fatal("a missing input was accepted")
	}
	if !strings.Contains(err.Error(), "stat input") {
		t.Fatalf("got %v, want a stat failure", err)
	}
}

func stubHandBrake(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "HandBrakeCLI")
	body := "#!/bin/sh\n" + script + "\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func sourceFile(t *testing.T, size int) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "S01E01.mkv")
	if err := os.WriteFile(p, []byte(strings.Repeat("o", size)), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	return p
}

func TestRunReplacesTheSourceOnSuccess(t *testing.T) {
	stubHandBrake(t, `
out=""
while [ $# -gt 0 ]; do
  if [ "$1" = "-o" ]; then out="$2"; fi
  shift
done
printf 'encoded' > "$out"
echo "Encoding: task 1 of 1, 50.00 % (10.00 fps, avg 10.00 fps, ETA 00h00m10s)"
echo "work result = 0"
`)
	input := sourceFile(t, 1024)

	var seen []Progress
	res, err := Run(context.Background(), input, Settings{}, nil, func(p Progress) {
		seen = append(seen, p)
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.FinalSize != int64(len("encoded")) {
		t.Fatalf("got final size %d, want %d", res.FinalSize, len("encoded"))
	}
	got, err := os.ReadFile(input)
	if err != nil {
		t.Fatalf("read input: %v", err)
	}
	if string(got) != "encoded" {
		t.Fatalf("got %q at the source path, want the encoded output moved into place", got)
	}
	if _, err := os.Stat(TempPath(input, "")); !os.IsNotExist(err) {
		t.Fatal("the temp file was left next to the source")
	}
	if len(seen) == 0 || seen[0].Percent != 50 {
		t.Fatalf("got progress %v, want the 50%% line reported", seen)
	}
	if !strings.Contains(res.Log, "work result = 0") {
		t.Fatalf("the log did not capture the run: %q", res.Log)
	}
}

func TestRunKeepsTheSourceWhenNoCommitIsSet(t *testing.T) {
	stubHandBrake(t, `
out=""
while [ $# -gt 0 ]; do
  if [ "$1" = "-o" ]; then out="$2"; fi
  shift
done
printf 'encoded' > "$out"
echo "work result = 0"
`)
	input := sourceFile(t, 32)

	res, err := Run(context.Background(), input, Settings{NoCommit: true}, nil, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.TempPath == "" {
		t.Fatal("NoCommit returned no temp path to inspect")
	}
	if _, err := os.Stat(res.TempPath); err != nil {
		t.Fatalf("the temp file is gone: %v", err)
	}
	original, err := os.ReadFile(input)
	if err != nil {
		t.Fatalf("read input: %v", err)
	}
	if string(original) != strings.Repeat("o", 32) {
		t.Fatal("NoCommit overwrote the source anyway")
	}
}

func TestRunCleansUpWhenHandBrakeFails(t *testing.T) {
	stubHandBrake(t, `
out=""
while [ $# -gt 0 ]; do
  if [ "$1" = "-o" ]; then out="$2"; fi
  shift
done
printf 'partial' > "$out"
echo "something went wrong" >&2
exit 3
`)
	input := sourceFile(t, 64)

	res, err := Run(context.Background(), input, Settings{}, nil, nil)
	if err == nil {
		t.Fatal("a non-zero exit was reported as success")
	}
	if _, statErr := os.Stat(TempPath(input, "")); !os.IsNotExist(statErr) {
		t.Fatal("the partial temp file was left behind after a failure")
	}
	original, readErr := os.ReadFile(input)
	if readErr != nil {
		t.Fatalf("read input: %v", readErr)
	}
	if len(original) != 64 {
		t.Fatal("the source was damaged by a failed encode")
	}
	if !strings.Contains(res.Log, "something went wrong") {
		t.Fatalf("stderr was not captured into the log: %q", res.Log)
	}
}

func TestRunRejectsAnExitZeroWithNoWorkResult(t *testing.T) {
	stubHandBrake(t, `
out=""
while [ $# -gt 0 ]; do
  if [ "$1" = "-o" ]; then out="$2"; fi
  shift
done
printf 'junk' > "$out"
echo "libva: could not initialise"
exit 0
`)
	input := sourceFile(t, 64)

	_, err := Run(context.Background(), input, Settings{}, nil, nil)
	if err == nil {
		t.Fatal("an encode that never reported a work result was accepted")
	}
	if !strings.Contains(err.Error(), "no work result") {
		t.Fatalf("got %v, want the missing work result named", err)
	}
	if _, statErr := os.Stat(TempPath(input, "")); !os.IsNotExist(statErr) {
		t.Fatal("the temp file survived a run with no work result")
	}
	if got, _ := os.ReadFile(input); len(got) != 64 {
		t.Fatal("the source was replaced despite the failure")
	}
}

func TestRunRejectsANonZeroWorkResult(t *testing.T) {
	stubHandBrake(t, `
out=""
while [ $# -gt 0 ]; do
  if [ "$1" = "-o" ]; then out="$2"; fi
  shift
done
printf 'junk' > "$out"
echo "work result = 4"
`)
	input := sourceFile(t, 64)

	_, err := Run(context.Background(), input, Settings{}, nil, nil)
	if err == nil {
		t.Fatal("work result = 4 was treated as success")
	}
	if !strings.Contains(err.Error(), "work result = 4") {
		t.Fatalf("got %v, want the work result in the message", err)
	}
	if got, _ := os.ReadFile(input); len(got) != 64 {
		t.Fatal("the source was replaced despite a non-zero work result")
	}
}

func TestRunPassesTheBuiltArgsToTheBinary(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	stubHandBrake(t, fmt.Sprintf(`
printf '%%s\n' "$@" > %q
out=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-o" ]; then out="$a"; fi
  prev="$a"
done
printf 'x' > "$out"
echo "work result = 0"
`, argsFile))
	input := sourceFile(t, 16)

	if _, err := Run(context.Background(), input, Settings{
		Encoder: "qsv_av1", Quality: 30, EncoderPreset: "speed", ContainerFormat: "mkv",
	}, nil, nil); err != nil {
		t.Fatalf("run: %v", err)
	}

	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	got := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if v, _ := argValue(got, "-e"); v != "qsv_av1" {
		t.Fatalf("the binary saw encoder %q", v)
	}
	if v, _ := argValue(got, "-q"); v != "30" {
		t.Fatalf("the binary saw quality %q", v)
	}
	if v, _ := argValue(got, "--enable-hw-decoding"); v != "qsv" {
		t.Fatalf("the binary saw hw decoding %q", v)
	}
	if v, _ := argValue(got, "-i"); v != input {
		t.Fatalf("the binary saw input %q, want %q", v, input)
	}
}

func TestRunStopsWhenTheContextIsCancelled(t *testing.T) {
	stubHandBrake(t, `
out=""
while [ $# -gt 0 ]; do
  if [ "$1" = "-o" ]; then out="$2"; fi
  shift
done
printf 'partial' > "$out"
sleep 30
echo "work result = 0"
`)
	input := sourceFile(t, 64)

	ctx, cancel := context.WithCancel(context.Background())
	go cancel()
	if _, err := Run(ctx, input, Settings{}, nil, nil); err == nil {
		t.Fatal("a cancelled encode was reported as success")
	}
	if _, err := os.Stat(TempPath(input, "")); !os.IsNotExist(err) {
		t.Fatal("a cancelled encode left its temp file behind")
	}
	if got, _ := os.ReadFile(input); len(got) != 64 {
		t.Fatal("a cancelled encode damaged the source")
	}
}
