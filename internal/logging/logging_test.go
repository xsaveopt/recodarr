package logging

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/natefinch/lumberjack.v2"
)

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug":    slog.LevelDebug,
		" DEBUG ":  slog.LevelDebug,
		"warn":     slog.LevelWarn,
		"Warning":  slog.LevelWarn,
		"error":    slog.LevelError,
		"info":     slog.LevelInfo,
		"":         slog.LevelInfo,
		"verbose":  slog.LevelInfo,
		"critical": slog.LevelInfo,
	}
	for in, want := range cases {
		if got := ParseLevel(in); got != want {
			t.Fatalf("ParseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestLevelLabel(t *testing.T) {
	cases := map[slog.Level]string{
		slog.LevelDebug - 4: "DEBUG",
		slog.LevelDebug:     "DEBUG",
		slog.LevelInfo:      "INFO ",
		slog.LevelInfo + 2:  "INFO ",
		slog.LevelWarn:      "WARN ",
		slog.LevelError:     "ERROR",
		slog.LevelError + 4: "ERROR",
	}
	for in, want := range cases {
		if got := levelLabel(in); got != want {
			t.Fatalf("levelLabel(%v) = %q, want %q", in, got, want)
		}
	}
}

func newTestLogger() (*slog.Logger, *bytes.Buffer, *slog.LevelVar) {
	var buf bytes.Buffer
	lv := &slog.LevelVar{}
	lv.Set(slog.LevelInfo)
	return slog.New(newAppHandler(&buf, lv)), &buf, lv
}

func lineAfterTimestamp(t *testing.T, buf *bytes.Buffer) string {
	t.Helper()
	out := buf.String()
	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("record %q does not end in a newline", out)
	}
	const stamp = len("2006-01-02 15:04:05")
	if len(out) < stamp+2 {
		t.Fatalf("record %q is too short", out)
	}
	return strings.TrimSuffix(out[stamp+2:], "\n")
}

func TestAppHandlerFormatsARecord(t *testing.T) {
	logger, buf, _ := newTestLogger()
	logger.Info("encode done", "id", 7, "path", "a b.mkv", "quote", `say "hi"`, "tab", "a\tb", "empty", "")

	got := lineAfterTimestamp(t, buf)
	want := `INFO   encode done id=7 path="a b.mkv" quote="say \"hi\"" tab="a\tb" empty=`
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestAppHandlerHonoursTheLevelVar(t *testing.T) {
	logger, buf, lv := newTestLogger()
	logger.Debug("hidden")
	if buf.Len() != 0 {
		t.Fatalf("debug record written at info level: %q", buf.String())
	}
	lv.Set(slog.LevelDebug)
	logger.Debug("shown")
	if !strings.Contains(buf.String(), "DEBUG  shown") {
		t.Fatalf("got %q, want the debug record once the level drops", buf.String())
	}
}

func TestAppHandlerWithAttrsAndGroup(t *testing.T) {
	logger, buf, _ := newTestLogger()
	logger.With("sink", "access").Info("req", "status", 200)
	if got := lineAfterTimestamp(t, buf); got != "INFO   req sink=access status=200" {
		t.Fatalf("got %q", got)
	}

	buf.Reset()
	logger.WithGroup("http").WithGroup("req").Info("m", "path", "/x")
	if got := lineAfterTimestamp(t, buf); got != "INFO   m http.req.path=/x" {
		t.Fatalf("got %q, want nested group prefixes", got)
	}
}

func TestAppHandlerAttrsBeforeAGroupStayUngrouped(t *testing.T) {
	logger, buf, _ := newTestLogger()
	logger.With("job", 1).WithGroup("hb").Info("m", "fps", 30)
	if got := lineAfterTimestamp(t, buf); got != "INFO   m job=1 hb.fps=30" {
		t.Fatalf("got %q, want attrs added before WithGroup left outside the group", got)
	}
}

func TestAppHandlerKeepsOneRecordPerLine(t *testing.T) {
	logger, buf, _ := newTestLogger()
	logger.Error("encode failed", "err", "exit_status_1\nforged=record")
	if n := strings.Count(buf.String(), "\n"); n != 1 {
		t.Fatalf("got %d lines for one record:\n%s", n, buf.String())
	}
}

func TestAppHandlerWithoutAttrsIsCloned(t *testing.T) {
	_, _, lv := newTestLogger()
	base := newAppHandler(io.Discard, lv)
	child := base.WithAttrs([]slog.Attr{slog.String("a", "1")}).(*appHandler)
	if len(base.attrs) != 0 || len(child.attrs) != 1 {
		t.Fatalf("WithAttrs mutated the parent: parent %v child %v", base.attrs, child.attrs)
	}
}

func TestSinksSetAppLevelToleratesNil(t *testing.T) {
	(&Sinks{}).SetAppLevel(slog.LevelDebug)
	lv := &slog.LevelVar{}
	s := &Sinks{AppLevel: lv}
	s.SetAppLevel(slog.LevelError)
	if lv.Level() != slog.LevelError {
		t.Fatalf("got %v, want error", lv.Level())
	}
}

func TestHandbrakeForWithoutASinkDiscards(t *testing.T) {
	if w := (&Sinks{}).HandbrakeFor(1); w != io.Discard {
		t.Fatalf("got %T, want io.Discard", w)
	}
}

func TestPrefixingWriterTagsEveryLine(t *testing.T) {
	var buf bytes.Buffer
	s := &Sinks{Handbrake: &buf}
	w := s.HandbrakeFor(42)

	for _, chunk := range []string{"first ", "line\nsecond", " line\rprogress\n", ""} {
		n, err := w.Write([]byte(chunk))
		if err != nil || n != len(chunk) {
			t.Fatalf("Write(%q) = %d, %v", chunk, n, err)
		}
	}
	want := "[job=42] first line\n[job=42] second line\r[job=42] progress\n"
	if buf.String() != want {
		t.Fatalf("got  %q\nwant %q", buf.String(), want)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestPrefixingWriterReportsErrors(t *testing.T) {
	w := newPrefixingWriter(failingWriter{}, "[p] ")
	if n, err := w.Write([]byte("x")); err == nil || n != 0 {
		t.Fatalf("got %d, %v, want the underlying error", n, err)
	}
}

func restoreDefaultLogger(t *testing.T) {
	t.Helper()
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
}

func TestSetupWithoutADirLogsToTheAppLogger(t *testing.T) {
	restoreDefaultLogger(t)
	s, err := Setup(Options{AppLevel: slog.LevelWarn})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if slog.Default() != s.App {
		t.Fatal("Setup must install the app logger as the default")
	}
	if s.AppLevel.Level() != slog.LevelWarn {
		t.Fatalf("got level %v, want warn", s.AppLevel.Level())
	}
	if s.Handbrake != io.Discard || s.Access == nil || s.Outbound == nil {
		t.Fatalf("got %+v, want access and outbound loggers and a discarded handbrake sink", s)
	}
}

func TestSetupWritesPlainFiles(t *testing.T) {
	restoreDefaultLogger(t)
	dir := filepath.Join(t.TempDir(), "logs")
	s, err := Setup(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	s.Access.Info("hit", "path", "/api")
	s.Outbound.Debug("hidden")
	_, _ = s.HandbrakeFor(3).Write([]byte("encoding\n"))
	s.Close()

	for _, name := range []string{"access.log", "outbound.log", "handbrake.log"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s missing: %v", name, err)
		}
	}
	var rec map[string]any
	access, _ := os.ReadFile(filepath.Join(dir, "access.log"))
	if err := json.Unmarshal(bytes.TrimSpace(access), &rec); err != nil || rec["path"] != "/api" {
		t.Fatalf("access log %q is not the expected JSON record (err %v)", access, err)
	}
	if outbound, _ := os.ReadFile(filepath.Join(dir, "outbound.log")); len(outbound) != 0 {
		t.Fatalf("debug record written to the outbound log at the default level: %q", outbound)
	}
	if hb, _ := os.ReadFile(filepath.Join(dir, "handbrake.log")); string(hb) != "[job=3] encoding\n" {
		t.Fatalf("got handbrake log %q", hb)
	}
}

func TestSetupAppendsToExistingFiles(t *testing.T) {
	restoreDefaultLogger(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "handbrake.log"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Setup(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.Handbrake.Write([]byte("new\n"))
	s.Close()
	if hb, _ := os.ReadFile(filepath.Join(dir, "handbrake.log")); string(hb) != "old\nnew\n" {
		t.Fatalf("got %q, want the new line appended", hb)
	}
}

func TestSetupWithRotationUsesLumberjack(t *testing.T) {
	restoreDefaultLogger(t)
	dir := t.TempDir()
	s, err := Setup(Options{Dir: dir, RotateEnabled: true, MaxAgeDays: 7, MaxBackups: 3, Compress: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	lj, ok := s.Handbrake.(*lumberjack.Logger)
	if !ok {
		t.Fatalf("got %T, want a rotating lumberjack sink", s.Handbrake)
	}
	if lj.MaxSize != 50 || lj.MaxAge != 7 || lj.MaxBackups != 3 || !lj.Compress {
		t.Fatalf("got %+v, want the default 50 MB size and the given retention", lj)
	}
	if lj.Filename != filepath.Join(dir, "handbrake.log") {
		t.Fatalf("got %q", lj.Filename)
	}
}

func TestSetupFailsWhenTheDirCannotBeCreated(t *testing.T) {
	restoreDefaultLogger(t)
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Setup(Options{Dir: filepath.Join(blocker, "logs")}); err == nil {
		t.Fatal("want an error when the log dir sits under a regular file")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("bad record %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

func TestOutboundTransportLogsByStatusClass(t *testing.T) {
	cases := []struct {
		status int
		level  string
		msg    string
	}{
		{200, "INFO", "outbound http"},
		{404, "INFO", "outbound http 4xx"},
		{503, "WARN", "outbound http 5xx"},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
		}))
		var buf bytes.Buffer
		client := &http.Client{Transport: OutboundTransport(nil, slog.New(slog.NewJSONHandler(&buf, nil)))}
		resp, err := client.Get(srv.URL + "/api/v3/series?apikey=secret")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		srv.Close()

		recs := jsonRecords(t, &buf)
		if len(recs) != 1 {
			t.Fatalf("status %d: got %d records, want one", tc.status, len(recs))
		}
		r := recs[0]
		if r["level"] != tc.level || r["msg"] != tc.msg || r["status"] != float64(tc.status) {
			t.Fatalf("status %d: got %+v", tc.status, r)
		}
		if r["method"] != "GET" || r["path"] != "/api/v3/series" {
			t.Fatalf("got %+v, want the method and path", r)
		}
		if strings.Contains(buf.String(), "secret") {
			t.Fatalf("query string leaked into the log: %s", buf.String())
		}
	}
}

func TestOutboundTransportLogsTransportErrors(t *testing.T) {
	var buf bytes.Buffer
	boom := errors.New("connection refused")
	tr := OutboundTransport(roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, boom }), slog.New(slog.NewJSONHandler(&buf, nil)))
	req := httptest.NewRequest(http.MethodPost, "http://arr.invalid/api/v3/command", nil)
	resp, err := tr.RoundTrip(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, boom) {
		t.Fatalf("got %v, want the transport error passed through", err)
	}
	recs := jsonRecords(t, &buf)
	if len(recs) != 1 || recs[0]["level"] != "WARN" || recs[0]["err"] != "connection refused" || recs[0]["host"] != "arr.invalid" {
		t.Fatalf("got %+v", recs)
	}
}

func TestOutboundTransportDumpsBodiesOnlyAtDebug(t *testing.T) {
	respond := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Proto:      "HTTP/1.1",
			ProtoMajor: 1,
			ProtoMinor: 1,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("0123456789abcdefghij")),
			Request:    r,
		}, nil
	})

	var info bytes.Buffer
	tr := &LoggedTransport{Base: respond, Logger: slog.New(slog.NewJSONHandler(&info, nil)), MaxBodyDump: 1000}
	resp, err := tr.RoundTrip(httptest.NewRequest(http.MethodGet, "http://q.invalid/x", nil))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if strings.Contains(info.String(), "outbound http body") {
		t.Fatal("body dumped although debug is off")
	}

	var debug bytes.Buffer
	tr = &LoggedTransport{Base: respond, Logger: slog.New(slog.NewJSONHandler(&debug, &slog.HandlerOptions{Level: slog.LevelDebug})), MaxBodyDump: 1000}
	resp, err = tr.RoundTrip(httptest.NewRequest(http.MethodGet, "http://q.invalid/x", nil))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "0123456789abcdefghij" {
		t.Fatalf("dumping consumed the body, caller got %q", body)
	}
	recs := jsonRecords(t, &debug)
	if len(recs) != 2 || recs[1]["msg"] != "outbound http body" || !strings.Contains(recs[1]["dump"].(string), "abcdefghij") {
		t.Fatalf("got %+v, want a body dump record", recs)
	}

	var capped bytes.Buffer
	tr = &LoggedTransport{Base: respond, Logger: slog.New(slog.NewJSONHandler(&capped, &slog.HandlerOptions{Level: slog.LevelDebug})), MaxBodyDump: 8}
	resp, err = tr.RoundTrip(httptest.NewRequest(http.MethodGet, "http://q.invalid/x", nil))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	recs = jsonRecords(t, &capped)
	if len(recs) != 2 || len(recs[1]["dump"].(string)) != 8 {
		t.Fatalf("got %+v, want the dump capped at 8 bytes", recs)
	}
}

func TestOutboundTransportDefaultsToTheDefaultTransport(t *testing.T) {
	lt, ok := OutboundTransport(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).(*LoggedTransport)
	if !ok || lt.Base != http.DefaultTransport {
		t.Fatalf("got %+v, want http.DefaultTransport as the base", lt)
	}
}
