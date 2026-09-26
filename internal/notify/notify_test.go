package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/xsaveopt/recodarr/internal/store"
)

type receiver struct {
	mu       sync.Mutex
	payloads []map[string]any
}

func (rc *receiver) start(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		rc.mu.Lock()
		rc.payloads = append(rc.payloads, body)
		rc.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func (rc *receiver) got() []map[string]any {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return append([]map[string]any(nil), rc.payloads...)
}

func openStore(t *testing.T, settings map[string]string) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "notify.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	for k, v := range settings {
		if err := st.SetSetting(context.Background(), k, v); err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}
	return st
}

func TestSendDoesNothingWithoutAURL(t *testing.T) {
	rc := &receiver{}
	rc.start(t)
	st := openStore(t, nil)
	Send(context.Background(), st, "Show", "done", "/m/a.mkv", 100, 50)
	SendHealth(context.Background(), st, "qbit", "down", "", "error", "opened")
	if len(rc.got()) != 0 {
		t.Fatal("a notification went out with no URL configured")
	}
}

func TestSendReportsADoneEncodeWithSavings(t *testing.T) {
	rc := &receiver{}
	st := openStore(t, map[string]string{"notify_url": rc.start(t)})

	Send(context.Background(), st, "Show S01E01", "done", "/m/a.mkv", 3*1024*1024, 1024*1024)
	got := rc.got()
	if len(got) != 1 {
		t.Fatalf("got %d notifications, want 1", len(got))
	}
	p := got[0]
	if p["title"] != "Recodarr" || p["status"] != "done" || p["filePath"] != "/m/a.mkv" {
		t.Fatalf("got %v", p)
	}
	if p["savedBytes"] != float64(2*1024*1024) {
		t.Fatalf("got savedBytes %v", p["savedBytes"])
	}
	if p["message"] != "Show S01E01 encoded — saved 2.0 MB" {
		t.Fatalf("got message %q", p["message"])
	}
}

func TestSendDoneWithoutSizesSkipsTheSavings(t *testing.T) {
	rc := &receiver{}
	st := openStore(t, map[string]string{"notify_url": rc.start(t)})
	Send(context.Background(), st, "Film", "done", "/m/f.mkv", 0, 0)
	got := rc.got()
	if len(got) != 1 || got[0]["message"] != "Film encoded" || got[0]["savedBytes"] != float64(0) {
		t.Fatalf("got %v", got)
	}
}

func TestSendReportsAFailure(t *testing.T) {
	rc := &receiver{}
	st := openStore(t, map[string]string{"notify_url": rc.start(t)})
	Send(context.Background(), st, "Film", "failed", "/m/f.mkv", 10, 0)
	got := rc.got()
	if len(got) != 1 || got[0]["message"] != "Failed to encode Film" || got[0]["status"] != "failed" {
		t.Fatalf("got %v", got)
	}
}

func TestSendHonoursThePerStatusToggles(t *testing.T) {
	rc := &receiver{}
	st := openStore(t, map[string]string{
		"notify_url":     rc.start(t),
		"notify_on_done": "false",
		"notify_on_fail": "false",
	})
	Send(context.Background(), st, "a", "done", "", 1, 1)
	Send(context.Background(), st, "a", "failed", "", 1, 1)
	Send(context.Background(), st, "a", "skipped", "", 1, 1)
	if n := len(rc.got()); n != 0 {
		t.Fatalf("got %d notifications, want none", n)
	}
}

func TestSendIgnoresUnknownStatuses(t *testing.T) {
	rc := &receiver{}
	st := openStore(t, map[string]string{"notify_url": rc.start(t)})
	Send(context.Background(), st, "a", "skipped", "", 1, 1)
	if n := len(rc.got()); n != 0 {
		t.Fatalf("got %d notifications for a skipped job", n)
	}
}

func TestSendSurvivesAnUnreachableEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	st := openStore(t, map[string]string{"notify_url": srv.URL})
	Send(context.Background(), st, "a", "done", "", 1, 1)
	SendHealth(context.Background(), st, "s", "t", "", "error", "opened")
}

func TestSendHealthTransitions(t *testing.T) {
	rc := &receiver{}
	st := openStore(t, map[string]string{"notify_url": rc.start(t)})

	SendHealth(context.Background(), st, "qbit:1", "qBittorrent down", "timeout", "error", "opened")
	SendHealth(context.Background(), st, "qbit:1", "qBittorrent down", "", "error", "opened")
	SendHealth(context.Background(), st, "qbit:1", "qBittorrent down", "", "error", "resolved")
	SendHealth(context.Background(), st, "qbit:1", "qBittorrent down", "", "error", "flapping")

	got := rc.got()
	if len(got) != 3 {
		t.Fatalf("got %d notifications, want 3", len(got))
	}
	for i, want := range []string{"qBittorrent down — timeout", "qBittorrent down", "Resolved: qBittorrent down"} {
		if got[i]["message"] != want {
			t.Fatalf("notification %d: got %q, want %q", i, got[i]["message"], want)
		}
	}
	if got[0]["status"] != "health" || got[0]["source"] != "qbit:1" || got[0]["level"] != "error" ||
		got[0]["transition"] != "opened" {
		t.Fatalf("got %v", got[0])
	}
	if got[2]["transition"] != "resolved" {
		t.Fatalf("got %v", got[2])
	}
}

func TestSendHealthHonoursItsToggle(t *testing.T) {
	rc := &receiver{}
	st := openStore(t, map[string]string{"notify_url": rc.start(t), "notify_on_health": "false"})
	SendHealth(context.Background(), st, "s", "t", "", "warn", "opened")
	if n := len(rc.got()); n != 0 {
		t.Fatalf("got %d health notifications with the toggle off", n)
	}
}

func TestFormatBytes(t *testing.T) {
	for _, tc := range []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{5 * 1024 * 1024, "5.0 MB"},
		{3 * 1024 * 1024 * 1024, "3.0 GB"},
		{2 * 1024 * 1024 * 1024 * 1024, "2.0 TB"},
		{2048 * 1024 * 1024 * 1024 * 1024, "2048.0 TB"},
	} {
		if got := formatBytes(tc.n); got != tc.want {
			t.Fatalf("formatBytes(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}
