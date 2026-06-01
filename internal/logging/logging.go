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

type Sinks struct {
	App *slog.Logger

	AppLevel *slog.LevelVar

	Access *slog.Logger

	Outbound *slog.Logger

	Handbrake io.Writer

	closers []io.Closer
}

func (s *Sinks) SetAppLevel(l slog.Level) {
	if s.AppLevel != nil {
		s.AppLevel.Set(l)
	}
}

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

func (s *Sinks) Close() {
	for _, c := range s.closers {
		_ = c.Close()
	}
}

func (s *Sinks) HandbrakeFor(jobID int64) io.Writer {
	if s.Handbrake == nil {
		return io.Discard
	}
	return newPrefixingWriter(s.Handbrake, fmt.Sprintf("[job=%d] ", jobID))
}

type Options struct {
	Dir           string
	RotateEnabled bool
	MaxSizeMB     int
	MaxAgeDays    int
	MaxBackups    int
	Compress      bool
	AppLevel      slog.Level
	AccessLevel   slog.Level
}

func Setup(opts Options) (*Sinks, error) {
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

type appHandler struct {
	w     io.Writer
	level *slog.LevelVar
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

type LoggedTransport struct {
	Base   http.RoundTripper
	Logger *slog.Logger

	MaxBodyDump int
}

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

type prefixingWriter struct {
	w      io.Writer
	prefix []byte
	atEOL  bool
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
