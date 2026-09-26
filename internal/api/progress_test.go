package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xsaveopt/recodarr/internal/auth"
	"github.com/xsaveopt/recodarr/internal/health"
	"github.com/xsaveopt/recodarr/internal/job"
	"github.com/xsaveopt/recodarr/internal/store"
)

type streamWorker struct {
	stubWorker
	current job.ProgressEvent
	events  chan job.ProgressEvent

	mu           sync.Mutex
	unsubscribed bool
}

func newStreamWorker(current job.ProgressEvent) *streamWorker {
	return &streamWorker{current: current, events: make(chan job.ProgressEvent, 8)}
}

func (s *streamWorker) CurrentProgress() job.ProgressEvent { return s.current }

func (s *streamWorker) Subscribe() (<-chan job.ProgressEvent, func()) {
	return s.events, func() {
		s.mu.Lock()
		s.unsubscribed = true
		s.mu.Unlock()
	}
}

func (s *streamWorker) wasUnsubscribed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.unsubscribed
}

type sseEvent struct {
	name string
	data string
}

type sseReader struct {
	t      *testing.T
	events chan sseEvent
}

func readSSE(t *testing.T, body io.Reader) *sseReader {
	t.Helper()
	r := &sseReader{t: t, events: make(chan sseEvent, 16)}
	go func() {
		defer close(r.events)
		sc := bufio.NewScanner(body)
		var ev sseEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if ev.name != "" {
					r.events <- ev
				}
				ev = sseEvent{}
			case strings.HasPrefix(line, "event: "):
				ev.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				ev.data = strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	return r
}

func (r *sseReader) next() sseEvent {
	r.t.Helper()
	select {
	case ev, ok := <-r.events:
		if !ok {
			r.t.Fatal("the stream ended before the next event")
		}
		return ev
	case <-time.After(5 * time.Second):
		r.t.Fatal("timed out waiting for an event")
	}
	return sseEvent{}
}

func (r *sseReader) waitClosed() {
	r.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-r.events:
			if !ok {
				return
			}
		case <-deadline:
			r.t.Fatal("the stream stayed open")
		}
	}
}

func progressOf(t *testing.T, ev sseEvent) job.ProgressEvent {
	t.Helper()
	if ev.name != "progress" {
		t.Fatalf("got a %q event, want progress", ev.name)
	}
	var p job.ProgressEvent
	if err := json.Unmarshal([]byte(ev.data), &p); err != nil {
		t.Fatalf("decode %q: %v", ev.data, err)
	}
	return p
}

type stream struct {
	status int
	header http.Header
	body   io.Reader
}

func openStream(t *testing.T, h http.Handler, header http.Header) (stream, context.CancelFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/worker/progress", nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return stream{status: res.StatusCode, header: res.Header, body: res.Body}, cancel
}

func sseHandler(wk workerClient) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/worker/progress", workerProgressSSE(wk))
	return mux
}

func TestProgressStreamStartsIdleWhenNothingEncodes(t *testing.T) {
	res, _ := openStream(t, sseHandler(newStreamWorker(job.ProgressEvent{})), nil)
	for header, want := range map[string]string{
		"Content-Type":      "text/event-stream",
		"Cache-Control":     "no-cache",
		"X-Accel-Buffering": "no",
	} {
		if got := res.header.Get(header); got != want {
			t.Fatalf("got %s %q, want %q", header, got, want)
		}
	}
	if ev := readSSE(t, res.body).next(); ev.name != "idle" || ev.data != "{}" {
		t.Fatalf("got %+v, want an idle event", ev)
	}
}

func TestProgressStreamSendsTheCurrentEncodeFirst(t *testing.T) {
	wk := newStreamWorker(job.ProgressEvent{JobID: 4, Title: "Show", Percent: 12.5, FPS: 24, ETA: "00h10m"})
	res, _ := openStream(t, sseHandler(wk), nil)
	got := progressOf(t, readSSE(t, res.body).next())
	if got != wk.current {
		t.Fatalf("got %+v, want %+v", got, wk.current)
	}
}

func TestProgressStreamRelaysEventsAndIdleMarkers(t *testing.T) {
	wk := newStreamWorker(job.ProgressEvent{})
	res, _ := openStream(t, sseHandler(wk), nil)
	sse := readSSE(t, res.body)
	sse.next()

	wk.events <- job.ProgressEvent{JobID: 9, Title: "Movie", Percent: 50, FPS: 30}
	if got := progressOf(t, sse.next()); got.JobID != 9 || got.Percent != 50 || got.Title != "Movie" {
		t.Fatalf("got %+v, want job 9 at 50%%", got)
	}

	wk.events <- job.ProgressEvent{}
	if ev := sse.next(); ev.name != "idle" {
		t.Fatalf("got %+v, want an idle event for a zero job id", ev)
	}

	close(wk.events)
	sse.waitClosed()
}

func TestProgressStreamUnsubscribesWhenTheClientLeaves(t *testing.T) {
	wk := newStreamWorker(job.ProgressEvent{})
	res, cancel := openStream(t, sseHandler(wk), nil)
	readSSE(t, res.body).next()
	cancel()

	deadline := time.Now().Add(5 * time.Second)
	for !wk.wasUnsubscribed() {
		if time.Now().After(deadline) {
			t.Fatal("the handler kept its subscription after the client disconnected")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type plainWriter struct {
	header http.Header
	code   int
	body   strings.Builder
}

func (p *plainWriter) Header() http.Header         { return p.header }
func (p *plainWriter) Write(b []byte) (int, error) { return p.body.Write(b) }
func (p *plainWriter) WriteHeader(code int)        { p.code = code }

func TestProgressStreamNeedsAFlushingWriter(t *testing.T) {
	w := &plainWriter{header: http.Header{}}
	workerProgressSSE(newStreamWorker(job.ProgressEvent{}))(w, httptest.NewRequest(http.MethodGet, "/api/worker/progress", nil))
	if w.code != http.StatusInternalServerError || !strings.Contains(w.body.String(), "streaming unsupported") {
		t.Fatalf("got %d %q, want a 500 explaining streaming is unsupported", w.code, w.body.String())
	}
}

type noLogLevel struct{}

func (noLogLevel) SetAppLevel(slog.Level) {}

func newRouterForTest(t *testing.T) (http.Handler, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "router.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	access := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewRouter(st, job.NewWorker(st), health.New(st), noLogLevel{}, testAssets(), access), st
}

func TestRouterProgressStreamRequiresASession(t *testing.T) {
	h, _ := newRouterForTest(t)
	r := httptest.NewRequest(http.MethodGet, "/api/worker/progress", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	wantStatus(t, w, http.StatusUnauthorized)
}

func TestRouterServesTheProgressStreamToASignedInAdmin(t *testing.T) {
	h, st := newRouterForTest(t)
	uid := seedAdmin(t, st)
	tok, _, err := auth.New(st.DB).CreateSession(context.Background(), uid)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	res, _ := openStream(t, h, http.Header{"Cookie": {auth.CookieName + "=" + tok}})
	if res.status != http.StatusOK {
		t.Fatalf("got %d, want 200", res.status)
	}
	if ct := res.header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("got Content-Type %q, want text/event-stream", ct)
	}
	if ev := readSSE(t, res.body).next(); ev.name != "idle" {
		t.Fatalf("got %+v, want the idle event from an idle worker", ev)
	}
}
