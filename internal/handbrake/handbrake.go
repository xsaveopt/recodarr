package handbrake

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// EncoderCaps holds discovered capabilities for a single encoder.
type EncoderCaps struct {
	Name     string   `json:"name"`
	Presets  []string `json:"presets"`
	Profiles []string `json:"profiles"`
	Tunes    []string `json:"tunes"`
	Levels   []string `json:"levels"`
}

// Caps holds all capabilities discovered from the HandBrakeCLI binary.
type Caps struct {
	Encoders []EncoderCaps `json:"encoders"`
}

var (
	capsOnce sync.Once
	capsVal  Caps
)

// QueryCaps discovers HandBrakeCLI capabilities once and caches the result.
func QueryCaps() Caps {
	capsOnce.Do(func() { capsVal = discoverCaps() })
	return capsVal
}

// VersionString returns the output of HandBrakeCLI --version, or an error message if not found.
func VersionString() string {
	// stdout only — stderr carries libhb init noise (nvenc/qsv probes, thread starts).
	out, err := exec.Command("HandBrakeCLI", "--version").Output()
	v := strings.TrimSpace(string(out))
	if err != nil && v == "" {
		return "(HandBrakeCLI not found)"
	}
	if i := strings.IndexByte(v, '\n'); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	return v
}

// Settings describes an encode job in terms of HandBrakeCLI flags.
type Settings struct {
	Encoder         string
	EncoderPreset   string
	EncoderProfile  string
	EncoderTune     string
	EncoderLevel    string
	// RateControl picks the bitrate model: "crf" (default) for constant
	// quality (uses Quality), or "abr" for average bitrate (uses VideoBitrate).
	RateControl     string
	Quality         int
	VideoBitrate    int // kbps; only used when RateControl="abr"
	MaxWidth        int
	MaxHeight       int
	AudioEncoder    string // "copy", "av_aac", "mp3", etc.; "" = HandBrake default
	AudioBitrate    int    // kbps; 0 = auto
	AudioMixdown    string // "stereo", "5point1", etc.; "" = keep source
	SubtitleCopy    bool
	TwoPass         bool
	ContainerFormat string // mkv (default) or mp4
	ExtraArgs       string // raw HandBrakeCLI flags appended verbatim
	Framerate       string // e.g. "30", "24000/1001"; empty = source
	NoCommit        bool   // when true, the encoded file is left at TempPath instead of
	// being renamed over the input. Callers use Commit or DiscardTemp to finalize. Used by
	// the worker's size-guard policies, which want to compare new vs. original before
	// destroying the source.
}

// Progress is a single progress observation parsed from HandBrakeCLI's stdout.
type Progress struct {
	Percent float64 // 0–100
	FPS     float64
	ETA     string // e.g. "00h05m12s", empty if unknown
}

// HandBrakeCLI prints progress lines like:
//
//	Encoding: task 1 of 1, 12.34 % (45.67 fps, avg 50.00 fps, ETA 00h05m12s)
var progressRe = regexp.MustCompile(`(\d+\.\d+)\s*%(?:\s*\(\s*(\d+\.\d+)\s*fps[^)]*?ETA\s+([0-9hms]+))?`)

func parseProgressLine(line string) (Progress, bool) {
	if !strings.Contains(line, "%") {
		return Progress{}, false
	}
	m := progressRe.FindStringSubmatch(line)
	if m == nil {
		return Progress{}, false
	}
	pct, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return Progress{}, false
	}
	p := Progress{Percent: pct}
	if len(m) > 3 {
		if f, err := strconv.ParseFloat(m[2], 64); err == nil {
			p.FPS = f
		}
		p.ETA = m[3]
	}
	return p, true
}

// RunResult holds the outcome of a successful encode.
type RunResult struct {
	FinalSize int64
	TempPath  string // populated when Settings.NoCommit was true; the encoded file's
	// uncommitted location. Caller must Commit it or DiscardTemp it.
	Log string // captured combined output (always populated)
}

// LineSink is an optional destination for HandBrakeCLI's raw stdout/stderr.
// Callers usually point this at handbrake.log via the logging package; nil
// (or io.Discard) drops the verbose output. The captured-for-error buffer is
// independent and always populated.
type LineSink struct {
	Stdout io.Writer
	Stderr io.Writer
}

// Run encodes input to a temp file in the same directory and atomically renames over input on success.
// Combined stdout+stderr is always captured into the returned RunResult.Log for the failure path;
// raw line-by-line output additionally goes to sink (if non-nil) so the caller can route it to a file.
// onProgress, if non-nil, is called for each parsed progress line — keep it cheap and non-blocking.
func Run(ctx context.Context, input string, s Settings, sink *LineSink, onProgress func(Progress)) (RunResult, error) {
	if strings.EqualFold(s.RateControl, "abr") && s.VideoBitrate <= 0 {
		return RunResult{}, fmt.Errorf("profile uses ABR rate control but video bitrate is 0 — set a bitrate in the profile")
	}
	if _, err := os.Stat(input); err != nil {
		return RunResult{}, fmt.Errorf("stat input: %w", err)
	}
	dir := filepath.Dir(input)
	base := filepath.Base(input)
	outExt := filepath.Ext(base)
	if s.ContainerFormat == "mp4" {
		outExt = ".mp4"
	}
	tmp := filepath.Join(dir, "."+base+".recodarr.tmp"+outExt)

	var buf bytes.Buffer
	var bufMu sync.Mutex
	args := buildArgs(input, tmp, s)
	cmd := exec.CommandContext(ctx, "HandBrakeCLI", args...)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return RunResult{}, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return RunResult{}, fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return RunResult{}, fmt.Errorf("start: %w", err)
	}

	pump := func(r io.Reader, mirror io.Writer, parseProgress bool, done chan<- struct{}) {
		defer close(done)
		// HandBrake updates progress with carriage returns on the same line; split on either \n or \r.
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		scanner.Split(splitOnCRorLF)
		for scanner.Scan() {
			line := scanner.Text()
			bufMu.Lock()
			buf.WriteString(line)
			buf.WriteByte('\n')
			bufMu.Unlock()
			_, _ = fmt.Fprintln(mirror, line)
			if parseProgress && onProgress != nil {
				if p, ok := parseProgressLine(line); ok {
					onProgress(p)
				}
			}
		}
	}
	stdoutMirror := io.Discard
	stderrMirror := io.Discard
	if sink != nil {
		if sink.Stdout != nil {
			stdoutMirror = sink.Stdout
		}
		if sink.Stderr != nil {
			stderrMirror = sink.Stderr
		}
	}
	outDone := make(chan struct{})
	errDone := make(chan struct{})
	go pump(stdoutPipe, stdoutMirror, true, outDone)
	go pump(stderrPipe, stderrMirror, false, errDone)
	<-outDone
	<-errDone
	waitErr := cmd.Wait()
	logText := buf.String()
	// HandBrakeCLI exits 0 even when the encode fails (encoder init failure,
	// hwaccel session error, missing codec support, etc.) — the actual outcome
	// is buried in stdout as `libhb: work result = N` and/or `Encode failed
	// (error N)`. Treat a non-zero work result as a hard failure regardless of
	// the process exit code; otherwise we would cheerfully rename a 0-byte
	// temp file over the source. Also treat a missing work-result line on a
	// clean exit as failure: a successful encode always emits one.
	if waitErr == nil {
		rc, ok := parseWorkResult(logText)
		if !ok {
			_ = os.Remove(tmp)
			return RunResult{Log: logText}, fmt.Errorf("HandBrakeCLI: no work result reported (encoder likely failed to initialize)")
		}
		if rc != 0 {
			_ = os.Remove(tmp)
			return RunResult{Log: logText}, fmt.Errorf("HandBrakeCLI: work result = %d", rc)
		}
	} else {
		_ = os.Remove(tmp)
		return RunResult{Log: logText}, fmt.Errorf("HandBrakeCLI: %w", waitErr)
	}

	stat, err := os.Stat(tmp)
	if err != nil {
		return RunResult{Log: logText}, fmt.Errorf("stat tmp: %w", err)
	}
	if s.NoCommit {
		// Leave the temp file in place; caller decides what to do with it.
		return RunResult{FinalSize: stat.Size(), TempPath: tmp, Log: logText}, nil
	}
	if err := os.Rename(tmp, input); err != nil {
		_ = os.Remove(tmp)
		return RunResult{Log: logText}, fmt.Errorf("rename: %w", err)
	}
	return RunResult{FinalSize: stat.Size(), Log: logText}, nil
}

// parseWorkResult finds HandBrake's `libhb: work result = N` line in the captured
// output. Returns the integer N and true if found. Reads from the end backwards
// because the line is always near the tail of the log and the buffer may be large.
func parseWorkResult(log string) (int, bool) {
	const marker = "work result = "
	idx := strings.LastIndex(log, marker)
	if idx < 0 {
		return 0, false
	}
	rest := log[idx+len(marker):]
	end := 0
	for end < len(rest) && (rest[end] == '-' || (rest[end] >= '0' && rest[end] <= '9')) {
		end++
	}
	if end == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil {
		return 0, false
	}
	return n, true
}

// Commit atomically renames a temp file produced with Settings.NoCommit over the
// original input path. Use this once the caller has decided the encode is good
// to keep. On the same filesystem (which the temp always is — same dir as input),
// rename is atomic.
func Commit(tempPath, finalPath string) error {
	if err := os.Rename(tempPath, finalPath); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("commit rename: %w", err)
	}
	return nil
}

// DiscardTemp removes a temp file produced with Settings.NoCommit. Errors are
// swallowed because there's nothing useful the caller can do with them — the
// encode already succeeded, this is just cleanup of a file we chose not to keep.
func DiscardTemp(tempPath string) {
	_ = os.Remove(tempPath)
}

// splitOnCRorLF is a bufio.SplitFunc that breaks on \r or \n so we capture HandBrake's
// in-place progress updates (which use \r) as separate "lines".
func splitOnCRorLF(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for i, b := range data {
		if b == '\n' || b == '\r' {
			return i + 1, data[:i], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func buildArgs(input, output string, s Settings) []string {
	encoder := s.Encoder
	if encoder == "" {
		encoder = "x265"
	}

	format := s.ContainerFormat
	if format == "" {
		format = "mkv"
	}

	args := []string{
		"-e", encoder,
		"-f", format,
		"-i", input,
		"-o", output,
	}
	// Rate control. CRF emits -q; ABR emits --vb. Mutually exclusive — HandBrake
	// errors if both are present. ABR with no bitrate set would silently fall
	// back to CRF — guard against that explicitly upstream (Run returns an
	// error before getting here).
	if strings.EqualFold(s.RateControl, "abr") {
		args = append(args, "--vb", strconv.Itoa(s.VideoBitrate))
	} else {
		quality := s.Quality
		if quality == 0 {
			quality = 22
		}
		args = append(args, "-q", strconv.Itoa(quality))
	}
	if s.EncoderPreset != "" {
		args = append(args, "--encoder-preset", s.EncoderPreset)
	}
	if s.EncoderProfile != "" {
		args = append(args, "--encoder-profile", s.EncoderProfile)
	}
	if s.EncoderTune != "" {
		args = append(args, "--encoder-tune", s.EncoderTune)
	}
	if s.EncoderLevel != "" {
		args = append(args, "--encoder-level", s.EncoderLevel)
	}
	if s.MaxWidth > 0 {
		args = append(args, "--maxWidth", strconv.Itoa(s.MaxWidth))
	}
	if s.MaxHeight > 0 {
		args = append(args, "--maxHeight", strconv.Itoa(s.MaxHeight))
	}
	if s.AudioEncoder != "" {
		args = append(args, "--all-audio", "--aencoder", s.AudioEncoder)
		if s.AudioEncoder != "copy" && s.AudioBitrate > 0 {
			args = append(args, "--ab", strconv.Itoa(s.AudioBitrate))
		}
		if s.AudioMixdown != "" {
			args = append(args, "--mixdown", s.AudioMixdown)
		}
	}
	if s.SubtitleCopy {
		args = append(args, "--all-subtitles")
	}
	if s.TwoPass {
		args = append(args, "--two-pass", "--turbo")
	}
	// Auto-enable matching hardware decoder so the GPU does both decode and encode
	// (zero-copy pipeline). HandBrake silently falls back to software decode if the
	// input codec isn't supported by the chosen NVDEC/QSV/VAAPI backend, so this is
	// safe to always do when the encoder is hardware.
	switch {
	case strings.HasPrefix(encoder, "nvenc_"):
		args = append(args, "--enable-hw-decoding", "nvdec")
	case strings.HasPrefix(encoder, "qsv_"):
		args = append(args, "--enable-hw-decoding", "qsv")
	case strings.HasPrefix(encoder, "vce_") || strings.HasPrefix(encoder, "vt_"):
		// AMD VCE on Linux goes through VAAPI; Apple VideoToolbox is its own thing
		// but HandBrake exposes it via the same flag.
		args = append(args, "--enable-hw-decoding", "vaapi")
	}
	if s.Framerate != "" {
		args = append(args, "-r", s.Framerate)
	}
	// Always CFR — VFR/PFR cause sync drift in some players and we don't ship those modes.
	args = append(args, "--cfr")
	if s.ExtraArgs != "" {
		args = append(args, splitShellArgs(s.ExtraArgs)...)
	}
	return args
}

// knownVideoEncoders is the complete set of HandBrake video encoder identifiers across all
// builds and platforms. We probe each one with --encoder-preset-list to find which are
// compiled into the installed binary; HandBrakeCLI has no --encoder-list flag.
var knownVideoEncoders = []string{
	// Software
	"x264", "x264_10bit",
	"x265", "x265_10bit", "x265_12bit",
	"svt_av1", "svt_av1_10bit",
	"mpeg4", "mpeg2",
	"VP8", "VP9", "VP9_10bit",
	"theora",
	"ffv1",
	// NVIDIA NVENC
	"nvenc_h264",
	"nvenc_h265", "nvenc_h265_10bit",
	"nvenc_av1", "nvenc_av1_10bit",
	// Intel QSV
	"qsv_h264",
	"qsv_h265", "qsv_h265_10bit",
	"qsv_av1", "qsv_av1_10bit",
	// AMD VCE
	"vce_h264",
	"vce_h265", "vce_h265_10bit",
	"vce_av1",
	// Apple VideoToolbox
	"vt_h264",
	"vt_h265", "vt_h265_10bit",
	// Windows Media Foundation
	"mf_h264", "mf_h265",
}

// discoverCaps walks the static Catalog and keeps only the encoders that the installed
// HandBrakeCLI binary actually has compiled in. We probe availability by running
// `HandBrakeCLI --encoder-preset-list <name>` and looking at the exit code only — exit 0
// means the encoder exists, non-zero means it isn't compiled in. We never parse the output
// (HandBrakeCLI's list-flag output is human-formatted and unstable across versions); the
// canonical preset/tune/profile/level lists come from Catalog.
func discoverCaps() Caps {
	const concurrency = 6
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	available := make(map[string]bool, len(knownVideoEncoders))
	for _, enc := range knownVideoEncoders {
		enc := enc
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if isEncoderAvailable(enc) {
				mu.Lock()
				available[enc] = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	encoders := make([]EncoderCaps, 0, len(available))
	for _, enc := range knownVideoEncoders { // preserve declaration order
		if !available[enc] {
			continue
		}
		caps, ok := Catalog[enc]
		if !ok {
			caps = EncoderCaps{Name: enc}
		}
		encoders = append(encoders, caps)
	}
	return Caps{Encoders: encoders}
}

// isEncoderAvailable returns true if HandBrakeCLI accepts this encoder name. We run the
// cheapest list flag and only check the exit code (output is discarded). 5s timeout per
// probe prevents a hung subprocess from blocking startup.
func isEncoderAvailable(enc string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "HandBrakeCLI", "--encoder-preset-list", enc)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}
