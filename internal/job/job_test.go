package job

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xsaveopt/recodarr/internal/handbrake"
	"github.com/xsaveopt/recodarr/internal/store"
)

func insertJob(t *testing.T, st *store.Store, r store.JobRow) int64 {
	t.Helper()
	if r.ArrKind == "" {
		r.ArrKind = "sonarr"
	}
	id, err := st.InsertJob(context.Background(), r)
	if err != nil {
		t.Fatalf("insert job: %v", err)
	}
	return id
}

func jobStatus(t *testing.T, st *store.Store, id int64) string {
	t.Helper()
	row, err := st.GetJob(context.Background(), id)
	if err != nil {
		t.Fatalf("get job %d: %v", id, err)
	}
	return row.Status
}

func TestParseHHMM(t *testing.T) {
	cases := []struct {
		in   string
		h, m int
	}{
		{"00:00", 0, 0},
		{"23:59", 23, 59},
		{"07:05", 7, 5},
		{"garbage", 0, 0},
		{"12", 0, 0},
		{"aa:bb", 0, 0},
	}
	for _, tc := range cases {
		h, m := parseHHMM(tc.in)
		if h != tc.h || m != tc.m {
			t.Fatalf("parseHHMM(%q) = %d:%d, want %d:%d", tc.in, h, m, tc.h, tc.m)
		}
	}
}

func TestInEncodingWindow(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	w := NewWorker(st)

	if !w.inEncodingWindow(ctx) {
		t.Fatal("no window configured must mean always active")
	}

	now := time.Now()
	hhmm := func(offset time.Duration) string { return now.Add(offset).Format("15:04") }

	set := func(start, end string) {
		if err := st.SetSetting(ctx, "encoding_window_start", start); err != nil {
			t.Fatal(err)
		}
		if err := st.SetSetting(ctx, "encoding_window_end", end); err != nil {
			t.Fatal(err)
		}
	}

	set(hhmm(-2*time.Hour), hhmm(2*time.Hour))
	if !w.inEncodingWindow(ctx) {
		t.Fatal("now sits inside the window, want active")
	}

	set(hhmm(2*time.Hour), hhmm(4*time.Hour))
	if w.inEncodingWindow(ctx) {
		t.Fatal("now sits before the window, want inactive")
	}

	set(hhmm(-2*time.Hour), hhmm(-1*time.Hour))
	if w.inEncodingWindow(ctx) {
		t.Fatal("now sits after the window, want inactive")
	}

	status := w.WindowStatus(ctx)
	if !status.HasLimit || status.Active {
		t.Fatalf("got %+v, want a limited, inactive window", status)
	}
	set("", "")
	if status := w.WindowStatus(ctx); status.HasLimit || !status.Active {
		t.Fatalf("got %+v, want an unlimited, active window", status)
	}
}

func TestInEncodingWindowOvernight(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	w := NewWorker(st)
	now := time.Now()
	hhmm := func(offset time.Duration) string { return now.Add(offset).Format("15:04") }

	if err := st.SetSetting(ctx, "encoding_window_start", hhmm(-1*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, "encoding_window_end", hhmm(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !w.inEncodingWindow(ctx) {
		t.Fatal("a window that wraps midnight must include now when it started an hour ago")
	}
}

func TestFormatBytes(t *testing.T) {
	cases := map[int64]string{
		0:               "0 B",
		512:             "512 B",
		1024:            "1.0 KB",
		1536:            "1.5 KB",
		1024 * 1024:     "1.0 MB",
		3 * 1024 * 1024: "3.0 MB",
		1 << 30:         "1.0 GB",
		1 << 40:         "1.0 TB",
		1 << 50:         "1024.0 TB",
	}
	for in, want := range cases {
		if got := formatBytes(in); got != want {
			t.Fatalf("formatBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncateLog(t *testing.T) {
	if got := truncateLog("a\nb\nc", 2); got != "b\nc" {
		t.Fatalf("got %q, want the last two lines", got)
	}
	if got := truncateLog("  only  ", 5); got != "only" {
		t.Fatalf("got %q, want the trimmed line", got)
	}
	big := strings.Repeat("x", maxEncodeLogBytes*2)
	got := truncateLog(big, 10)
	if len(got) <= maxEncodeLogBytes {
		t.Fatalf("got %d bytes, want the marker plus the byte cap", len(got))
	}
	if !strings.HasPrefix(got, "…[truncated]\n") {
		t.Fatalf("got %q..., want a truncation marker", got[:20])
	}
	if len(got)-len("…[truncated]\n") != maxEncodeLogBytes {
		t.Fatalf("truncated body is %d bytes, want %d", len(got)-len("…[truncated]\n"), maxEncodeLogBytes)
	}
}

func TestHardlinkCount(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "media.mkv")
	if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := HardlinkCount(path)
	if err != nil || n != 1 {
		t.Fatalf("got %d (err %v), want 1", n, err)
	}
	link := filepath.Join(dir, "seeding.mkv")
	if err := os.Link(path, link); err != nil {
		t.Skipf("hardlinks unavailable here: %v", err)
	}
	if n, err := HardlinkCount(path); err != nil || n != 2 {
		t.Fatalf("got %d (err %v), want 2 while the download copy exists", n, err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if n, err := HardlinkCount(path); err != nil || n != 1 {
		t.Fatalf("got %d (err %v), want 1 once the torrent copy is gone", n, err)
	}
	if _, err := HardlinkCount(filepath.Join(dir, "missing.mkv")); err == nil {
		t.Fatal("want an error for a missing file")
	}
}

func TestCheckHardlinksGate(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	dir := t.TempDir()

	free := filepath.Join(dir, "free.mkv")
	held := filepath.Join(dir, "held.mkv")
	for _, p := range []string{free, held} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Link(held, filepath.Join(dir, "held.seeding.mkv")); err != nil {
		t.Skipf("hardlinks unavailable here: %v", err)
	}

	freeID := insertJob(t, st, store.JobRow{Title: "free", FilePath: free, Status: string(StatusWaitingForHardlink)})
	heldID := insertJob(t, st, store.JobRow{Title: "held", FilePath: held, Status: string(StatusWaitingForHardlink)})
	goneID := insertJob(t, st, store.JobRow{Title: "gone", FilePath: filepath.Join(dir, "missing.mkv"), Status: string(StatusWaitingForHardlink)})
	otherID := insertJob(t, st, store.JobRow{Title: "other", FilePath: free, Status: string(StatusWaitingForSeed)})

	NewWorker(st).checkHardlinks(ctx)

	if got := jobStatus(t, st, freeID); got != string(StatusReady) {
		t.Fatalf("file with no extra links: got %q, want ready", got)
	}
	if got := jobStatus(t, st, heldID); got != string(StatusWaitingForHardlink) {
		t.Fatalf("file the torrent still links to: got %q, want it held", got)
	}
	if got := jobStatus(t, st, goneID); got != string(StatusReady) {
		t.Fatalf("stat failure must fail open: got %q, want ready", got)
	}
	if got := jobStatus(t, st, otherID); got != string(StatusWaitingForSeed) {
		t.Fatalf("hardlink gate touched a seed-gated job: got %q", got)
	}
}

func qbitServer(t *testing.T, present []string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("Ok."))
	})
	mux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, r *http.Request) {
		wanted := strings.Split(r.URL.Query().Get("hashes"), "|")
		var out []map[string]any
		for _, h := range wanted {
			for _, p := range present {
				if strings.EqualFold(h, p) {
					out = append(out, map[string]any{"hash": p, "name": "t", "state": "uploading"})
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestCheckSeedingReleasesOnlyVanishedTorrents(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	srv := qbitServer(t, []string{"aaaa1111"})
	if _, err := st.UpsertQbitInstance(ctx, store.QbitInstanceRow{Name: "qbit", URL: srv.URL, Username: "u", Password: "p"}); err != nil {
		t.Fatal(err)
	}

	seeding := insertJob(t, st, store.JobRow{Title: "seeding", FilePath: "/m/a.mkv", DownloadID: "AAAA1111", Status: string(StatusWaitingForSeed)})
	gone := insertJob(t, st, store.JobRow{Title: "gone", FilePath: "/m/b.mkv", DownloadID: "bbbb2222", Status: string(StatusWaitingForSeed)})
	noHash := insertJob(t, st, store.JobRow{Title: "nohash", FilePath: "/m/c.mkv", Status: string(StatusWaitingForSeed)})

	NewWorker(st).checkSeeding(ctx)

	if got := jobStatus(t, st, seeding); got != string(StatusWaitingForSeed) {
		t.Fatalf("torrent still in qbit: got %q, want it held", got)
	}
	if got := jobStatus(t, st, gone); got != string(StatusReady) {
		t.Fatalf("torrent removed from qbit: got %q, want ready", got)
	}
	if got := jobStatus(t, st, noHash); got != string(StatusReady) {
		t.Fatalf("job without a hash: got %q, want ready", got)
	}
}

func TestCheckSeedingHoldsEverythingWhenQbitIsMissing(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	id := insertJob(t, st, store.JobRow{Title: "held", FilePath: "/m/a.mkv", DownloadID: "abc", Status: string(StatusWaitingForSeed)})

	NewWorker(st).checkSeeding(ctx)

	if got := jobStatus(t, st, id); got != string(StatusWaitingForSeed) {
		t.Fatalf("got %q, want the job held while qbit is unconfigured", got)
	}
}

func TestCheckSeedingHoldsEverythingWhenQbitErrors(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	if _, err := st.UpsertQbitInstance(ctx, store.QbitInstanceRow{Name: "qbit", URL: srv.URL, Username: "u", Password: "p"}); err != nil {
		t.Fatal(err)
	}
	id := insertJob(t, st, store.JobRow{Title: "held", FilePath: "/m/a.mkv", DownloadID: "abc", Status: string(StatusWaitingForSeed)})

	NewWorker(st).checkSeeding(ctx)

	if got := jobStatus(t, st, id); got != string(StatusWaitingForSeed) {
		t.Fatalf("got %q, want the job held when qbit cannot be reached", got)
	}
}

func TestTransitionOnlyMovesFromTheExpectedStatus(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	w := NewWorker(st)
	id := insertJob(t, st, store.JobRow{Title: "j", FilePath: "/m/a.mkv", Status: string(StatusWaitingForSeed)})

	w.transition(ctx, id, string(StatusWaitingForHardlink), "wrong source status")
	if got := jobStatus(t, st, id); got != string(StatusWaitingForSeed) {
		t.Fatalf("got %q, want the job untouched", got)
	}
	w.transition(ctx, id, string(StatusWaitingForSeed), "released")
	if got := jobStatus(t, st, id); got != string(StatusReady) {
		t.Fatalf("got %q, want ready", got)
	}
}

func TestReresolveProfileFollowsCurrentMappings(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	w := NewWorker(st)

	first, err := st.UpsertProfile(ctx, store.ProfileRow{Name: "first", Encoder: "x265"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.UpsertProfile(ctx, store.ProfileRow{Name: "second", Encoder: "x265"})
	if err != nil {
		t.Fatal(err)
	}
	mappingID, err := st.CreateTagMapping(ctx, store.TagMappingRow{ArrKind: "sonarr", TagID: 1, TagLabel: "anime", ProfileID: second})
	if err != nil {
		t.Fatal(err)
	}

	job := store.JobRow{
		ArrKind:   "sonarr",
		Tags:      `["anime"]`,
		ProfileID: sql.NullInt64{Int64: first, Valid: true},
	}
	got, changed := w.reresolveProfile(ctx, job)
	if !changed || got.Int64 != second {
		t.Fatalf("got %+v changed=%v, want profile %d", got, changed, second)
	}

	job.ProfileID = sql.NullInt64{Int64: second, Valid: true}
	if _, changed := w.reresolveProfile(ctx, job); changed {
		t.Fatal("profile already matches the mapping, want no change")
	}

	if err := st.DeleteTagMapping(ctx, mappingID); err != nil {
		t.Fatal(err)
	}
	got, changed = w.reresolveProfile(ctx, job)
	if !changed || got.Valid {
		t.Fatalf("got %+v changed=%v, want the profile cleared once the mapping is gone", got, changed)
	}

	for _, tags := range []string{"", "[]", "not json"} {
		j := store.JobRow{ArrKind: "sonarr", Tags: tags, ProfileID: sql.NullInt64{Int64: first, Valid: true}}
		if got, changed := w.reresolveProfile(ctx, j); changed || got.Int64 != first {
			t.Fatalf("tags %q: got %+v changed=%v, want the stored profile kept", tags, got, changed)
		}
	}
}

func TestRunEncodesFailsJobsPastTheAttemptCap(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	id := insertJob(t, st, store.JobRow{Title: "stuck", FilePath: "/m/a.mkv", Status: string(StatusReady)})
	if _, err := st.DB.ExecContext(ctx, `UPDATE jobs SET attempts = ? WHERE id = ?`, store.MaxJobAttempts, id); err != nil {
		t.Fatal(err)
	}

	NewWorker(st).runEncodes(ctx)

	row, err := st.GetJob(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != string(StatusFailed) {
		t.Fatalf("got %q, want failed", row.Status)
	}
	if !strings.Contains(row.Error, fmt.Sprintf("gave up after %d attempts", store.MaxJobAttempts)) {
		t.Fatalf("got error %q, want the give-up message", row.Error)
	}
}

func TestRunEncodesRefusesRecodarrTempFiles(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	temp := handbrake.TempPath("/media/tv/show/S01E01.mkv", "mkv")
	id := insertJob(t, st, store.JobRow{Title: "temp", FilePath: temp, Status: string(StatusReady)})

	NewWorker(st).runEncodes(ctx)

	row, err := st.GetJob(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != string(StatusFailed) {
		t.Fatalf("got %q, want failed", row.Status)
	}
	if !strings.Contains(row.Error, "temp file") {
		t.Fatalf("got error %q, want it to name the temp file", row.Error)
	}
}

func TestRunEncodesRespectsPauseAndWindow(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	id := insertJob(t, st, store.JobRow{Title: "ready", FilePath: "/m/a.mkv", Status: string(StatusReady)})
	w := NewWorker(st)

	if err := st.SetSetting(ctx, "encoding_paused", "true"); err != nil {
		t.Fatal(err)
	}
	w.runEncodes(ctx)
	if got := jobStatus(t, st, id); got != string(StatusReady) {
		t.Fatalf("paused worker claimed a job: got %q", got)
	}

	if err := st.SetSetting(ctx, "encoding_paused", "false"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := st.SetSetting(ctx, "encoding_window_start", now.Add(2*time.Hour).Format("15:04")); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, "encoding_window_end", now.Add(4*time.Hour).Format("15:04")); err != nil {
		t.Fatal(err)
	}
	w.runEncodes(ctx)
	if got := jobStatus(t, st, id); got != string(StatusReady) {
		t.Fatalf("worker encoded outside its window: got %q", got)
	}
}

func TestSetPausedPersistsAndCancelsNothingWhenIdle(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	w := NewWorker(st)

	n, err := w.SetPaused(ctx, true)
	if err != nil || n != 0 {
		t.Fatalf("got %d (err %v), want no in-flight encodes cancelled", n, err)
	}
	if !w.IsPaused(ctx) {
		t.Fatal("pause was not persisted")
	}
	if _, err := w.SetPaused(ctx, false); err != nil {
		t.Fatal(err)
	}
	if w.IsPaused(ctx) {
		t.Fatal("resume was not persisted")
	}
}

func TestCheckDiskSpace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "media.mkv")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkDiskSpace(path, 1024); err != nil {
		t.Fatalf("a kilobyte should fit: %v", err)
	}
	if err := checkDiskSpace(path, math.MaxInt64/2); err == nil {
		t.Fatal("want an error when the file cannot possibly fit")
	}
	if err := checkDiskSpace(filepath.Join("/nonexistent-recodarr-test", "f.mkv"), 1024); err != nil {
		t.Fatalf("statfs failure must be a no-op precheck: %v", err)
	}
}

func TestSidecarWriters(t *testing.T) {
	dir := t.TempDir()
	media := filepath.Join(dir, "Show.S01E01.mkv")
	j := store.JobRow{ID: 42, Title: "Show S01E01", FileSize: 1000}
	p := &store.ProfileRow{Name: "anime", Encoder: "x265_10bit", EncoderPreset: "slow", RateControl: "crf", Quality: 24, ContainerFormat: "mkv"}

	if got := sidecarPath(media, "recodarr"); got != filepath.Join(dir, "Show.S01E01.recodarr") {
		t.Fatalf("got %q, want the stem plus suffix", got)
	}

	if err := writeSidecar(media, "recodarr", j, p, 400); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(sidecarPath(media, "recodarr"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"job_id=42", "profile=anime", "rate_control=crf", "quality=24", "original_size=1000", "final_size=400", "saved_percent=60.0"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("sidecar missing %q:\n%s", want, body)
		}
	}

	if err := writeSkipSidecar(media, "recodarr", j, p, "file too small"); err != nil {
		t.Fatal(err)
	}
	body, err = os.ReadFile(sidecarPath(media, "recodarr"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"status=skipped", "reason=file too small", "job_id=42"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("skip sidecar missing %q:\n%s", want, body)
		}
	}
}

func TestSidecarWriterRecordsABRProfiles(t *testing.T) {
	dir := t.TempDir()
	media := filepath.Join(dir, "Movie.mkv")
	p := &store.ProfileRow{Name: "abr", Encoder: "x265", RateControl: "ABR", VideoBitrate: 3000, ContainerFormat: "mkv"}
	if err := writeSidecar(media, "recodarr", store.JobRow{ID: 1}, p, 10); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(sidecarPath(media, "recodarr"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "rate_control=abr") || !strings.Contains(string(body), "video_bitrate_kbps=3000") {
		t.Fatalf("sidecar did not record the ABR settings:\n%s", body)
	}
}

func TestProgressBroadcastAndSubscribers(t *testing.T) {
	w := NewWorker(newTestStore(t))
	ch, cancel := w.Subscribe()
	defer cancel()

	w.mu.Lock()
	w.encoding[7] = &activeEncode{cancel: func() {}, title: "Show"}
	w.mu.Unlock()

	w.broadcast(ProgressEvent{JobID: 7, Title: "Show", Percent: 12.5, FPS: 30})
	select {
	case ev := <-ch:
		if ev.JobID != 7 || ev.Percent != 12.5 {
			t.Fatalf("got %+v, want the broadcast event", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber never received the event")
	}

	if got := w.CurrentProgress(); got.JobID != 7 || got.Percent != 12.5 {
		t.Fatalf("got %+v, want the last progress for job 7", got)
	}
	if got := w.EncodingJobIDs(); len(got) != 1 || got[0] != 7 {
		t.Fatalf("got %v, want [7]", got)
	}
	if got := w.AllProgress(); len(got) != 1 || got[0].JobID != 7 {
		t.Fatalf("got %+v, want one entry for job 7", got)
	}
	if !w.CancelEncoding(7) {
		t.Fatal("cancelling a live encode must report true")
	}
	if w.CancelEncoding(999) {
		t.Fatal("cancelling an unknown job must report false")
	}
}

func TestEncodingJobIDIsZeroWhenIdle(t *testing.T) {
	w := NewWorker(newTestStore(t))
	if got := w.EncodingJobID(); got != 0 {
		t.Fatalf("got %d, want 0", got)
	}
	if got := w.CurrentProgress(); got != (ProgressEvent{}) {
		t.Fatalf("got %+v, want a zero event", got)
	}
}
