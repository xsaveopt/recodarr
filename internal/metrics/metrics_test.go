package metrics

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xsaveopt/recodarr/internal/job"
	"github.com/xsaveopt/recodarr/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "metrics.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func scrape(t *testing.T, h http.Handler, auth string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func body(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	b, err := io.ReadAll(w.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

func TestHandlerExportsQueueAndWorkerGauges(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	for i, status := range []string{"waiting_for_seed", "waiting_for_seed", "ready", "done", "failed", "skipped", "waiting_for_hardlink"} {
		if _, err := st.InsertJob(ctx, store.JobRow{
			ArrKind: "sonarr", Title: "t", FilePath: "/m/" + status + string(rune('a'+i)) + ".mkv", Status: status,
		}); err != nil {
			t.Fatalf("insert job: %v", err)
		}
	}
	if err := st.SetSetting(ctx, "max_parallel_encodes", "4"); err != nil {
		t.Fatalf("set parallel: %v", err)
	}
	if err := st.SetSetting(ctx, "encoding_paused", "true"); err != nil {
		t.Fatalf("set paused: %v", err)
	}

	w := scrape(t, Handler(st, job.NewWorker(st), ""), "")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	out := body(t, w)
	for _, want := range []string{
		`recodarr_jobs{status="waiting_for_seed"} 2`,
		`recodarr_jobs{status="waiting_for_hardlink"} 1`,
		`recodarr_jobs{status="ready"} 1`,
		`recodarr_jobs{status="encoding"} 0`,
		`recodarr_jobs{status="done"} 1`,
		`recodarr_jobs{status="failed"} 1`,
		`recodarr_jobs{status="skipped"} 1`,
		"recodarr_bytes_saved_total 0",
		"recodarr_worker_active_encodes 0",
		"recodarr_worker_max_parallel_encodes 4",
		"recodarr_worker_paused 1",
		"recodarr_worker_window_active 1",
		"recodarr_worker_last_tick_timestamp_seconds 0",
		"recodarr_handbrake_available ",
		"go_goroutines",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("the scrape is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "recodarr_encode_progress_percent{") {
		t.Fatal("per-job progress was exported with nothing encoding")
	}
}

func TestHandlerReportsBytesSaved(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	id, err := st.InsertJob(ctx, store.JobRow{ArrKind: "sonarr", Title: "t", FilePath: "/m/a.mkv", FileSize: 1000, Status: "ready"})
	if err != nil {
		t.Fatalf("insert job: %v", err)
	}
	if ok, err := st.MarkJobEncoding(ctx, id); err != nil || !ok {
		t.Fatalf("mark encoding: %v %v", ok, err)
	}
	if err := st.MarkJobDone(ctx, id, 400); err != nil {
		t.Fatalf("mark done: %v", err)
	}
	out := body(t, scrape(t, Handler(st, job.NewWorker(st), ""), ""))
	if !strings.Contains(out, "recodarr_bytes_saved_total 600") {
		t.Fatalf("got:\n%s\nwant 600 bytes saved", out)
	}
}

func TestHandlerRequiresTheBearerTokenWhenSet(t *testing.T) {
	st := openStore(t)
	h := Handler(st, job.NewWorker(st), "s3cret")

	for _, auth := range []string{"", "Bearer wrong", "s3cret", "Basic s3cret"} {
		w := scrape(t, h, auth)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("auth %q: got %d, want 401", auth, w.Code)
		}
		if w.Header().Get("WWW-Authenticate") != `Bearer realm="metrics"` {
			t.Fatalf("auth %q: got WWW-Authenticate %q", auth, w.Header().Get("WWW-Authenticate"))
		}
	}

	w := scrape(t, h, "Bearer s3cret")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d with the right token, want 200", w.Code)
	}
	if !strings.Contains(body(t, w), "recodarr_jobs") {
		t.Fatal("an authorised scrape returned no recodarr metrics")
	}
}
