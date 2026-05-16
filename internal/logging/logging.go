// Package logging splits Recodarr's log output into multiple sinks so the
// container's stdout stays high-signal (one line per encode start/finish,
// errors, startup notices) and the verbose stuff (HTTP access, outbound HTTP
// calls, HandBrake's frame-by-frame output) goes to rotating files under
// <data-dir>/logs/.
//
// All sinks are independent: the app logger can be at INFO while the access
// log captures every request, without flooding the user's `docker logs` output.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

// Sinks holds every logger and writer the rest of the app pulls from. Build
// it once at startup with Setup() and pass the pieces explicitly to subsystems
// that need them. The App logger is also installed as slog.Default() so any
// code that does a bare slog.Info(...) lands on stdout in the operator-facing
// format.
type Sinks struct {
	// App is the human-facing logger that writes to stdout. Use it for
	// status events the operator should see in `docker logs`: encode
	// started/done/failed, settings changes, startup, shutdown, warnings.
	// Format is compact text: "<TIME>  INFO  message  key=value".
	App *slog.Logger

	// AppLevel is the live-updatable threshold for the App logger. Settings
	// changes from the UI flip this with SetLevel; the handler re-reads it
	// per record.
	AppLevel *slog.LevelVar

	// Access logs every inbound HTTP request to access.log. JSON format
	// so it's easy to grep / pipe to jq.
	Access *slog.Logger

	// Outbound logs every outbound HTTP call (Recodarr → qBit, → Sonarr,
	// → Radarr, → notification webhook) to outbound.log. JSON format.
	Outbound *slog.Logger

	// Handbrake is the raw byte sink for HandBrakeCLI's stdout/stderr.
	// Multi-encode safe: the rotating writer guards its own concurrent
	// writes. One file across all encodes, prefixed with `[job=N]` at
	// the call site (worker writes through HandbrakeFor(jobID)).
	Handbrake io.Writer

	// closers are file handles / rotators we hold so Close can flush them
	// cleanly on shutdown. Pure stdout sinks have no entry here.
	closers []io.Closer
}

// SetAppLevel changes the live threshold for the App logger. Safe to call
// at any time from any goroutine.
func (s *Sinks) SetAppLevel(l slog.Level) {
	if s.AppLevel != nil {
		s.AppLevel.Set(l)
	}
}

// ParseLevel maps the user-facing names ("DEBUG"/"INFO"/"WARN"/"ERROR") onto
// slog levels. Unknown values fall back to INFO.
func ParseLevel(s string) slog.Level {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "DEBUG":
		return slog.LevelDebug
	case "WARN", "WARNING":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Close flushes any open log files. Safe to call multiple times; the rotators
// no-op after their first Close.
func (s *Sinks) Close() {
	for _, c := range s.closers {
		_ = c.Close()
	}
}

// HandbrakeFor returns a writer that tags every line it receives with the
// given job id, so a single handbrake.log shared across concurrent encodes
// remains untangleable. The writer is one-shot per encode — discard after the
// run finishes.
func (s *Sinks) HandbrakeFor(jobID int64) io.Writer {
	if s.Handbrake == nil {
		return io.Discard
	}
	return newPrefixingWriter(s.Handbrake, fmt.Sprintf("[job=%d] ", jobID))
}

// Options controls log destinations. Pass Dir="" to skip file logging entirely
// (useful for tests and one-shot CLI subcommands). MaxSizeMB / MaxAgeDays /
// MaxBackups follow lumberjack's defaults if zero.
type Options struct {
	Dir           string // directory for *.log files; empty = stdout-only
	RotateEnabled bool   // when false, files are plain *os.File in append mode (grow unbounded)
	MaxSizeMB     int
	MaxAgeDays    int
	MaxBackups    int
	Compress      bool
	AppLevel      slog.Level // default INFO; honored even when Dir is empty
	AccessLevel   slog.Level
}

// Setup builds the Sinks. Returned Sinks installs Sinks.App as slog.Default so
// any code path that calls slog.* directly lands on stdout in the new format.
//
// File-backed sinks are created lazily-but-eagerly: their lumberjack writers
// touch the directory and the file at startup, surfacing permission errors
// loudly rather than burying them at first-write time hours later.
func Setup(opts Options) (*Sinks, error) {
	// We only default MaxSizeMB. MaxAgeDays=0 and MaxBackups=0 are valid
	// user-facing values meaning "no limit" (matching lumberjack's own
	// semantics) — don't substitute them. The persisted settings layer
	// supplies its own first-use defaults (50/30/5); callers like tests
	// that pass an empty Options still get a sane file size cap.
	if opts.MaxSizeMB == 0 {
		opts.MaxSizeMB = 50
	}

	levelVar := &slog.LevelVar{}
	levelVar.Set(opts.AppLevel)
	s := &Sinks{
		App:      slog.New(newAppHandler(os.Stdout, levelVar)),
		AppLevel: levelVar,
	}
	slog.SetDefault(s.App)

	if opts.Dir == "" {
		// Stdout-only mode. Everything else points at the App logger or
		// the discard writer.
		s.Access = s.App.With("sink", "access")
		s.Outbound = s.App.With("sink", "outbound")
		s.Handbrake = io.Discard
		return s, nil
	}

	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("create log dir %s: %w", opts.Dir, err)
	}

	access, err := openSink(filepath.Join(opts.Dir, "access.log"), opts)
	if err != nil {
		return nil, err
	}
	outbound, err := openSink(filepath.Join(opts.Dir, "outbound.log"), opts)
	if err != nil {
		return nil, err
	}
	handbrake, err := openSink(filepath.Join(opts.Dir, "handbrake.log"), opts)
	if err != nil {
		return nil, err
	}
	s.closers = append(s.closers, access, outbound, handbrake)

	level := opts.AccessLevel
	s.Access = slog.New(slog.NewJSONHandler(access, &slog.HandlerOptions{Level: level}))
	s.Outbound = slog.New(slog.NewJSONHandler(outbound, &slog.HandlerOptions{Level: level}))
	s.Handbrake = handbrake

	if opts.RotateEnabled {
		s.App.Info("logging initialized",
			"dir", opts.Dir,
			"rotate", true,
			"max_size_mb", opts.MaxSizeMB,
			"max_backups", opts.MaxBackups,
			"max_age_days", opts.MaxAgeDays,
		)
	} else {
		s.App.Info("logging initialized", "dir", opts.Dir, "rotate", false)
	}
	return s, nil
}

// openSink returns the writer for one log file. With rotation it's a
// lumberjack.Logger; without, it's a plain *os.File opened in append mode.
// Both satisfy io.WriteCloser.
func openSink(path string, opts Options) (io.WriteCloser, error) {
	if opts.RotateEnabled {
		return &lumberjack.Logger{
			Filename:   path,
			MaxSize:    opts.MaxSizeMB,
			MaxAge:     opts.MaxAgeDays,
			MaxBackups: opts.MaxBackups,
			Compress:   opts.Compress,
			LocalTime:  true,
		}, nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return f, nil
}

// ── App stdout handler ─────────────────────────────────────────────────
//
// Compact one-line text format aimed at humans skimming `docker logs`:
//
//	2026-05-16 10:31:42  INFO   encode done  title="Severance" saved=33%
//	2026-05-16 10:32:01  ERROR  encode failed  title="Andor" err="cancelled"
//
// We intentionally don't use slog.TextHandler because its `time=… level=INFO
// msg=…` framing dilutes the operator-facing line. We only need three fields
// in fixed positions (time, level, message) and then the attribute soup.

type appHandler struct {
	w     io.Writer
	level *slog.LevelVar // shared with Sinks.AppLevel; re-read per record
	attrs []slog.Attr
	group string
}

func newAppHandler(w io.Writer, level *slog.LevelVar) *appHandler {
	return &appHandler{w: w, level: level}
}

func (h *appHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level.Level()
}

func (h *appHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Time.Format("2006-01-02 15:04:05"))
	b.WriteString("  ")
	b.WriteString(levelLabel(r.Level))
	b.WriteString("  ")
	b.WriteString(r.Message)

	// Pre-existing attrs from WithAttrs, then per-record attrs.
	for _, a := range h.attrs {
		appendAttr(&b, a, h.group)
	}
	r.Attrs(func(a slog.Attr) bool {
		appendAttr(&b, a, h.group)
		return true
	})
	b.WriteByte('\n')
	_, err := io.WriteString(h.w, b.String())
	return err
}

func (h *appHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &clone
}

func (h *appHandler) WithGroup(name string) slog.Handler {
	clone := *h
	if h.group != "" {
		clone.group = h.group + "." + name
	} else {
		clone.group = name
	}
	return &clone
}

func levelLabel(l slog.Level) string {
	// Fixed-width so the column lines up.
	switch {
	case l >= slog.LevelError:
		return "ERROR"
	case l >= slog.LevelWarn:
		return "WARN "
	case l >= slog.LevelInfo:
		return "INFO "
	default:
		return "DEBUG"
	}
}

func appendAttr(b *strings.Builder, a slog.Attr, group string) {
	if a.Equal(slog.Attr{}) {
		return
	}
	b.WriteByte(' ')
	if group != "" {
		b.WriteString(group)
		b.WriteByte('.')
	}
	b.WriteString(a.Key)
	b.WriteByte('=')
	v := a.Value.String()
	if strings.ContainsAny(v, " \t\"") {
		fmt.Fprintf(b, "%q", v)
	} else {
		b.WriteString(v)
	}
}

// ── Outbound HTTP transport wrapper ─────────────────────────────────────
//
// LoggedTransport wraps any http.RoundTripper so every outgoing request is
// logged with its method, target, status, and elapsed time. Used by qbit and
// arr clients (and the notify dispatcher) so calls to those external services
// land in outbound.log rather than stdout.

type LoggedTransport struct {
	Base   http.RoundTripper
	Logger *slog.Logger
	// MaxBodyDump, if >0 and Debug level is enabled on Logger, dumps up
	// to N bytes of the response body. Defaults to 0 (off) since outbound
	// bodies can be large (qBit's torrent lists, *arr's command responses).
	MaxBodyDump int
}

// OutboundTransport returns a RoundTripper that logs each request to the
// outbound logger. Pass http.DefaultTransport (or nil to default to it) as
// the base.
func OutboundTransport(base http.RoundTripper, logger *slog.Logger) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &LoggedTransport{Base: base, Logger: logger}
}

func (t *LoggedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := t.Base.RoundTrip(req)
	dur := time.Since(start)

	attrs := []any{
		"method", req.Method,
		"host", req.URL.Host,
		"path", req.URL.Path,
		"dur_ms", dur.Milliseconds(),
	}
	if err != nil {
		attrs = append(attrs, "err", err.Error())
		t.Logger.Warn("outbound http error", attrs...)
		return resp, err
	}
	attrs = append(attrs, "status", resp.StatusCode)
	switch {
	case resp.StatusCode >= 500:
		t.Logger.Warn("outbound http 5xx", attrs...)
	case resp.StatusCode >= 400:
		t.Logger.Info("outbound http 4xx", attrs...)
	default:
		t.Logger.Info("outbound http", attrs...)
	}

	if t.MaxBodyDump > 0 && t.Logger.Enabled(req.Context(), slog.LevelDebug) {
		if dump, _ := httputil.DumpResponse(resp, true); dump != nil {
			if len(dump) > t.MaxBodyDump {
				dump = dump[:t.MaxBodyDump]
			}
			t.Logger.Debug("outbound http body", "host", req.URL.Host, "dump", string(dump))
		}
	}
	return resp, nil
}

// ── HandBrake line-prefixing writer ─────────────────────────────────────
//
// prefixingWriter tags every line written through it with a job-id prefix so
// concurrent encodes sharing handbrake.log remain readable. HandBrakeCLI's
// progress lines come in via CR-delimited chunks; we treat both \n and \r as
// "end of line" so the prefix lands on every visible update.

type prefixingWriter struct {
	w      io.Writer
	prefix []byte
	atEOL  bool // true at start, and after each \n/\r seen
}

func newPrefixingWriter(w io.Writer, prefix string) *prefixingWriter {
	return &prefixingWriter{w: w, prefix: []byte(prefix), atEOL: true}
}

func (p *prefixingWriter) Write(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	var out []byte
	for _, c := range b {
		if p.atEOL {
			out = append(out, p.prefix...)
			p.atEOL = false
		}
		out = append(out, c)
		if c == '\n' || c == '\r' {
			p.atEOL = true
		}
	}
	if _, err := p.w.Write(out); err != nil {
		return 0, err
	}
	return len(b), nil
}
