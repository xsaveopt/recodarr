package job

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/xsaveopt/recodarr/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "recodarr.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestSanitizeMediaPath(t *testing.T) {
	abs := filepath.Join(string(filepath.Separator), "media", "tv", "show", "ep.mkv")
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "absolute", in: abs, want: abs},
		{name: "cleaned", in: abs + string(filepath.Separator) + ".", want: abs},
		{name: "empty", in: "", wantErr: true},
		{name: "relative", in: filepath.Join("media", "ep.mkv"), wantErr: true},
		{name: "null byte", in: abs + "\x00", wantErr: true},
		{
			name: "parent segments are resolved by clean, not rejected",
			in:   string(filepath.Separator) + filepath.Join("media", "..", "etc", "passwd"),
			want: filepath.Join(string(filepath.Separator), "etc", "passwd"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sanitizeMediaPath(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSidecarExists(t *testing.T) {
	dir := t.TempDir()
	media := filepath.Join(dir, "Episode.S01E01.mkv")
	if err := os.WriteFile(media, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if sidecarExists(media, "recodarr") {
		t.Fatal("no sidecar written yet, want false")
	}
	if sidecarExists(media, "") {
		t.Fatal("empty suffix must never report a sidecar")
	}
	if err := os.WriteFile(filepath.Join(dir, "Episode.S01E01.recodarr"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if !sidecarExists(media, "recodarr") {
		t.Fatal("sidecar present, want true")
	}
}

func TestMatchMapping(t *testing.T) {
	idx := map[int64]store.TagMappingRow{
		7: {ID: 1, TagID: 7, TagLabel: "anime", ProfileID: 3},
	}
	if _, ok := matchMapping(nil, idx); ok {
		t.Fatal("no tags must not match")
	}
	if _, ok := matchMapping([]int64{1, 2}, idx); ok {
		t.Fatal("unmapped tags must not match")
	}
	mp, ok := matchMapping([]int64{2, 7}, idx)
	if !ok || mp.ProfileID != 3 {
		t.Fatalf("got %+v ok=%v, want profile 3", mp, ok)
	}
}

type fakeArr struct {
	series  []map[string]any
	files   []map[string]any
	history []map[string]any
	calls   map[string]int
}

func (f *fakeArr) server(t *testing.T) *httptest.Server {
	t.Helper()
	f.calls = map[string]int{}
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("/api/v3/series", func(w http.ResponseWriter, r *http.Request) {
		f.calls["series"]++
		write(w, f.series)
	})
	mux.HandleFunc("/api/v3/episodefile", func(w http.ResponseWriter, r *http.Request) {
		f.calls["files"]++
		write(w, f.files)
	})
	mux.HandleFunc("/api/v3/history/series", func(w http.ResponseWriter, r *http.Request) {
		f.calls["history"]++
		write(w, f.history)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func seedInstanceAndMapping(t *testing.T, st *store.Store, url string) int64 {
	const tagID = int64(9)
	t.Helper()
	ctx := context.Background()
	instID, err := st.CreateArrInstance(ctx, store.ArrInstanceRow{
		Kind: "sonarr", Name: "sonarr", URL: url, APIKey: "k", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	profileID, err := st.UpsertProfile(ctx, store.ProfileRow{Name: "test-profile", Encoder: "x265"})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	if _, err := st.CreateTagMapping(ctx, store.TagMappingRow{
		ArrKind: "sonarr", TagID: tagID, TagLabel: "anime", ProfileID: profileID,
	}); err != nil {
		t.Fatalf("create mapping: %v", err)
	}
	return instID
}

func TestReconcileEnqueuesJobsAndPicksTheSeedGate(t *testing.T) {
	dir := t.TempDir()
	withHash := filepath.Join(dir, "S01E01.mkv")
	noHash := filepath.Join(dir, "S01E02.mkv")

	fa := &fakeArr{
		series: []map[string]any{
			{"id": 1, "title": "Mapped", "path": dir, "tags": []int{9}, "statistics": map[string]any{"episodeFileCount": 2, "sizeOnDisk": 100}},
			{"id": 2, "title": "Unmapped", "path": dir, "tags": []int{3}, "statistics": map[string]any{"episodeFileCount": 1, "sizeOnDisk": 100}},
		},
		files: []map[string]any{
			{"id": 11, "seriesId": 1, "path": withHash, "relativePath": "Season 1/S01E01.mkv", "size": 500},
			{"id": 12, "seriesId": 1, "path": noHash, "relativePath": "Season 1/S01E02.mkv", "size": 600},
			{"id": 13, "seriesId": 1, "path": "relative/path.mkv", "relativePath": "x.mkv", "size": 700},
			{"id": 14, "seriesId": 1, "path": filepath.Join(dir, ".S01E03.mkv.recodarr.tmp.mkv"), "relativePath": "t.mkv", "size": 800},
		},
		history: []map[string]any{
			{"eventType": "downloadFolderImported", "downloadId": "ABC123", "date": "2024-01-01T00:00:00Z", "data": map[string]any{"importedPath": withHash}},
			{"eventType": "grabbed", "downloadId": "DEF456", "date": "2024-01-02T00:00:00Z"},
		},
	}
	srv := fa.server(t)

	st := newTestStore(t)
	instID := seedInstanceAndMapping(t, st, srv.URL)

	w := NewWorker(st)
	ctx := context.Background()
	w.reconcile(ctx)

	jobs, total, err := st.ListJobs(ctx, store.JobListOptions{})
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if total != 2 {
		t.Fatalf("got %d jobs, want 2 (bad path and temp path must be skipped)", total)
	}
	byItem := map[int64]store.JobRow{}
	for _, j := range jobs {
		byItem[j.ArrItemID] = j
	}
	if got := byItem[11]; got.Status != string(StatusWaitingForSeed) || got.DownloadID != "ABC123" {
		t.Fatalf("file with import history: got status=%q downloadId=%q, want waiting_for_seed/ABC123", got.Status, got.DownloadID)
	}
	if got := byItem[12]; got.Status != string(StatusWaitingForHardlink) || got.DownloadID != "" {
		t.Fatalf("file without import history: got status=%q downloadId=%q, want waiting_for_hardlink and no hash", got.Status, got.DownloadID)
	}
	if got := byItem[11]; got.ArrInstanceID != instID || got.ArrParentID != 1 || got.Source != "poll" || got.Tags != `["anime"]` {
		t.Fatalf("unexpected job row: %+v", got)
	}
	if got := byItem[11]; !got.ProfileID.Valid {
		t.Fatal("job must carry the mapped profile")
	}
	if fa.calls["history"] != 1 {
		t.Fatalf("import history fetched %d times, want once per parent", fa.calls["history"])
	}
	if fa.calls["files"] != 1 {
		t.Fatalf("files fetched %d times, want only for the tagged parent", fa.calls["files"])
	}
}

func TestReconcileIsIdempotentAcrossTerminalJobs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "S01E01.mkv")
	fa := &fakeArr{
		series: []map[string]any{
			{"id": 1, "title": "Mapped", "path": dir, "tags": []int{9}, "statistics": map[string]any{"episodeFileCount": 1, "sizeOnDisk": 100}},
		},
		files: []map[string]any{
			{"id": 11, "seriesId": 1, "path": path, "relativePath": "Season 1/S01E01.mkv", "size": 500},
		},
	}
	srv := fa.server(t)
	st := newTestStore(t)
	seedInstanceAndMapping(t, st, srv.URL)

	w := NewWorker(st)
	ctx := context.Background()
	w.reconcile(ctx)
	w.reconcile(ctx)

	_, total, err := st.ListJobs(ctx, store.JobListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("got %d jobs after two reconciles, want 1", total)
	}

	jobs, _, err := st.ListJobs(ctx, store.JobListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkJobDone(ctx, jobs[0].ID, 100); err != nil {
		t.Fatal(err)
	}
	w.reconcile(ctx)
	_, total, err = st.ListJobs(ctx, store.JobListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("got %d jobs after a done job was reconciled again, want 1", total)
	}
}

func TestReconcileSkipsDisabledAndUnmappedInstances(t *testing.T) {
	dir := t.TempDir()
	fa := &fakeArr{
		series: []map[string]any{
			{"id": 1, "title": "Mapped", "path": dir, "tags": []int{9}, "statistics": map[string]any{"episodeFileCount": 1, "sizeOnDisk": 1}},
		},
		files: []map[string]any{
			{"id": 11, "seriesId": 1, "path": filepath.Join(dir, "a.mkv"), "relativePath": "a.mkv", "size": 1},
		},
	}
	srv := fa.server(t)
	st := newTestStore(t)
	ctx := context.Background()
	instID := seedInstanceAndMapping(t, st, srv.URL)
	if err := st.UpdateArrInstance(ctx, store.ArrInstanceRow{
		ID: instID, Kind: "sonarr", Name: "sonarr", URL: srv.URL, Enabled: false,
	}); err != nil {
		t.Fatal(err)
	}

	NewWorker(st).reconcile(ctx)

	if _, total, err := st.ListJobs(ctx, store.JobListOptions{}); err != nil || total != 0 {
		t.Fatalf("got %d jobs (err %v), want none from a disabled instance", total, err)
	}
	if fa.calls["series"] != 0 {
		t.Fatalf("disabled instance was polled %d times", fa.calls["series"])
	}
}

func TestReconcileSkipsFilesWithASidecar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "S01E01.mkv")
	if err := os.WriteFile(filepath.Join(dir, "S01E01.recodarr"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	fa := &fakeArr{
		series: []map[string]any{
			{"id": 1, "title": "Mapped", "path": dir, "tags": []int{9}, "statistics": map[string]any{"episodeFileCount": 1, "sizeOnDisk": 1}},
		},
		files: []map[string]any{
			{"id": 11, "seriesId": 1, "path": path, "relativePath": "S01E01.mkv", "size": 1},
		},
	}
	srv := fa.server(t)
	st := newTestStore(t)
	ctx := context.Background()
	seedInstanceAndMapping(t, st, srv.URL)
	if err := st.SetSetting(ctx, "output_suffix_enabled", "true"); err != nil {
		t.Fatal(err)
	}

	NewWorker(st).reconcile(ctx)

	if _, total, err := st.ListJobs(ctx, store.JobListOptions{}); err != nil || total != 0 {
		t.Fatalf("got %d jobs (err %v), want none when a sidecar marks the file", total, err)
	}
}

func TestReconcileSurvivesHistoryFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "S01E01.mkv")
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/series", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": 1, "title": "Mapped", "path": dir, "tags": []int{9}, "statistics": map[string]any{"episodeFileCount": 1, "sizeOnDisk": 1}},
		})
	})
	mux.HandleFunc("/api/v3/episodefile", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": 11, "seriesId": 1, "path": path, "relativePath": "S01E01.mkv", "size": 1},
		})
	})
	mux.HandleFunc("/api/v3/history/series", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	st := newTestStore(t)
	ctx := context.Background()
	seedInstanceAndMapping(t, st, srv.URL)
	NewWorker(st).reconcile(ctx)

	jobs, total, err := st.ListJobs(ctx, store.JobListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("got %d jobs, want 1", total)
	}
	if jobs[0].Status != string(StatusWaitingForHardlink) {
		t.Fatalf("got status %q, want the hardlink fallback when history is unavailable", jobs[0].Status)
	}
}

func TestReadReconcileIntervalUsesStoredSetting(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	w := NewWorker(st)
	if got := w.readReconcileInterval(ctx); got.Seconds() != 300 {
		t.Fatalf("got %v, want the 300s default", got)
	}
	if err := st.SetSetting(ctx, "reconcile_interval_seconds", "900"); err != nil {
		t.Fatal(err)
	}
	if got := w.readReconcileInterval(ctx); got.Seconds() != 900 {
		t.Fatalf("got %v, want 900s", got)
	}
}

func TestReconcileParentReportsInsertCount(t *testing.T) {
	dir := t.TempDir()
	var files []map[string]any
	for i := 1; i <= 3; i++ {
		files = append(files, map[string]any{
			"id": 10 + i, "seriesId": 1,
			"path":         filepath.Join(dir, fmt.Sprintf("S01E0%d.mkv", i)),
			"relativePath": fmt.Sprintf("S01E0%d.mkv", i),
			"size":         int64(i) * 100,
		})
	}
	fa := &fakeArr{
		series: []map[string]any{
			{"id": 1, "title": "Mapped", "path": dir, "tags": []int{9}, "statistics": map[string]any{"episodeFileCount": 3, "sizeOnDisk": 600}},
		},
		files: files,
	}
	srv := fa.server(t)
	st := newTestStore(t)
	ctx := context.Background()
	seedInstanceAndMapping(t, st, srv.URL)
	NewWorker(st).reconcile(ctx)

	rows, total, err := st.ListJobs(ctx, store.JobListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Fatalf("got %d jobs, want 3", total)
	}
	for _, r := range rows {
		if r.FileSize == 0 {
			t.Fatalf("job %d lost its file size", r.ID)
		}
	}
}
