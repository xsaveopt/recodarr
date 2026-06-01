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

type EncoderCaps struct {
	Name     string   `json:"name"`
	Presets  []string `json:"presets"`
	Profiles []string `json:"profiles"`
	Tunes    []string `json:"tunes"`
	Levels   []string `json:"levels"`
}

type Caps struct {
	Encoders []EncoderCaps `json:"encoders"`
}

var (
	capsOnce sync.Once
	capsVal  Caps

	versionOnce sync.Once
	versionVal  string
)

func QueryCaps() Caps {
	capsOnce.Do(func() { capsVal = discoverCaps() })
	return capsVal
}

func VersionString() string {
	versionOnce.Do(func() {
		out, err := exec.Command("HandBrakeCLI", "--version").Output()
		v := strings.TrimSpace(string(out))
		if err != nil && v == "" {
			versionVal = "(HandBrakeCLI not found)"
			return
		}
		if i := strings.IndexByte(v, '\n'); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		versionVal = v
	})
	return versionVal
}

type Settings struct {
	Encoder        string
	EncoderPreset  string
	EncoderProfile string
	EncoderTune    string
	EncoderLevel   string

	RateControl  string
	Quality      int
	VideoBitrate int
	MaxWidth     int
	MaxHeight    int
	AudioEncoder string
	AudioBitrate int
	AudioMixdown string

	AudioBitratesPerTrack []int
	SubtitleCopy          bool
	TwoPass               bool
	ContainerFormat       string
	ExtraArgs             string
	Framerate             string
	NoCommit              bool
}

type Progress struct {
	Percent float64
	FPS     float64
	ETA     string
}

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

type RunResult struct {
	FinalSize int64
	TempPath  string

	Log string
}

type LineSink struct {
	Stdout io.Writer
	Stderr io.Writer
}

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

	if waitErr != nil {
		_ = os.Remove(tmp)
		return RunResult{Log: logText}, fmt.Errorf("HandBrakeCLI: %w", waitErr)
	}
	rc, ok := parseWorkResult(logText)
	if !ok {
		_ = os.Remove(tmp)
		return RunResult{Log: logText}, fmt.Errorf("HandBrakeCLI: no work result reported (encoder likely failed to initialize)")
	}
	if rc != 0 {
		_ = os.Remove(tmp)
		return RunResult{Log: logText}, fmt.Errorf("HandBrakeCLI: work result = %d", rc)
	}

	stat, err := os.Stat(tmp)
	if err != nil {
		return RunResult{Log: logText}, fmt.Errorf("stat tmp: %w", err)
	}
	if s.NoCommit {
		return RunResult{FinalSize: stat.Size(), TempPath: tmp, Log: logText}, nil
	}
	if err := os.Rename(tmp, input); err != nil {
		_ = os.Remove(tmp)
		return RunResult{Log: logText}, fmt.Errorf("rename: %w", err)
	}
	return RunResult{FinalSize: stat.Size(), Log: logText}, nil
}

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

func Commit(tempPath, finalPath string) error {
	if err := os.Rename(tempPath, finalPath); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("commit rename: %w", err)
	}
	return nil
}

func DiscardTemp(tempPath string) {
	_ = os.Remove(tempPath)
}

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
		if s.AudioEncoder != "copy" {
			if len(s.AudioBitratesPerTrack) > 0 {
				parts := make([]string, len(s.AudioBitratesPerTrack))
				for i, b := range s.AudioBitratesPerTrack {
					parts[i] = strconv.Itoa(b)
				}
				args = append(args, "--ab", strings.Join(parts, ","))
			} else if s.AudioBitrate > 0 {
				args = append(args, "--ab", strconv.Itoa(s.AudioBitrate))
			}
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

	switch {
	case strings.HasPrefix(encoder, "nvenc_"):
		args = append(args, "--enable-hw-decoding", "nvdec")
	case strings.HasPrefix(encoder, "qsv_"):
		args = append(args, "--enable-hw-decoding", "qsv")
	case strings.HasPrefix(encoder, "vce_") || strings.HasPrefix(encoder, "vt_"):

		args = append(args, "--enable-hw-decoding", "vaapi")
	}
	if s.Framerate != "" {
		args = append(args, "-r", s.Framerate)
	}

	args = append(args, "--cfr")
	if s.ExtraArgs != "" {
		args = append(args, splitShellArgs(s.ExtraArgs)...)
	}
	return args
}

var knownVideoEncoders = []string{
	"x264", "x264_10bit",
	"x265", "x265_10bit", "x265_12bit",
	"svt_av1", "svt_av1_10bit",
	"mpeg4", "mpeg2",
	"VP8", "VP9", "VP9_10bit",
	"theora",
	"ffv1",

	"nvenc_h264",
	"nvenc_h265", "nvenc_h265_10bit",
	"nvenc_av1", "nvenc_av1_10bit",

	"qsv_h264",
	"qsv_h265", "qsv_h265_10bit",
	"qsv_av1", "qsv_av1_10bit",

	"vce_h264",
	"vce_h265", "vce_h265_10bit",
	"vce_av1",

	"vt_h264",
	"vt_h265", "vt_h265_10bit",

	"mf_h264", "mf_h265",
}

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
	for _, enc := range knownVideoEncoders {
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

func isEncoderAvailable(enc string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "HandBrakeCLI", "--encoder-preset-list", enc)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}
