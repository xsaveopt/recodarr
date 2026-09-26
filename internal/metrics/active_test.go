package metrics

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xsaveopt/recodarr/internal/handbrake"
	"github.com/xsaveopt/recodarr/internal/job"
	"github.com/xsaveopt/recodarr/internal/store"
)

type blockingEncoder struct {
	progress map[string]handbrake.Progress
	started  chan struct{}
}

func (b *blockingEncoder) Encode(ctx context.Context, path string, _ handbrake.Settings, onProgress func(handbrake.Progress)) (handbrake.RunResult, error) {
	onProgress(b.progress[path])
	b.started <- struct{}{}
	<-ctx.Done()
	return handbrake.RunResult{}, ctx.Err()
}

func startEncodes(t *testing.T, st *store.Store, progress []handbrake.Progress) (*job.Worker, []int64) {
	t.Helper()
	ctx := context.Background()
	if err := st.SetSetting(ctx, "max_parallel_encodes", fmt.Sprint(len(progress))); err != nil {
		t.Fatalf("set parallel: %v", err)
	}
	pid, err := st.UpsertProfile(ctx, store.ProfileRow{Name: "HEVC", Encoder: "x265", AudioEncoder: "copy"})
	if err != nil {
		t.Fatalf("upsert profile: %v", err)
	}
	dir := t.TempDir()
	ids := make([]int64, len(progress))
	enc := &blockingEncoder{progress: map[string]handbrake.Progress{}, started: make(chan struct{}, len(progress))}
	for i, p := range progress {
		path := filepath.Join(dir, fmt.Sprintf("e%d.mkv", i))
		enc.progress[path] = p
		ids[i], err = st.InsertJob(ctx, store.JobRow{
			ArrKind:   "sonarr",
			Title:     fmt.Sprintf("show %d", i),
			FilePath:  path,
			ProfileID: sql.NullInt64{Int64: pid, Valid: true},
			Status:    "ready",
		})
		if err != nil {
			t.Fatalf("insert job: %v", err)
		}
	}

	w := job.NewWorker(st)
	w.SetRemoteEncoderResolver(func(context.Context) job.RemoteEncoder { return enc })

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(runCtx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		deadline := time.Now().Add(5 * time.Second)
		for len(w.EncodingJobIDs()) > 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
	})

	for range progress {
		select {
		case <-enc.started:
		case <-time.After(5 * time.Second):
			t.Fatal("the worker never started the encodes")
		}
	}
	return w, ids
}

func TestHandlerExportsLiveEncodes(t *testing.T) {
	st := openStore(t)
	w, ids := startEncodes(t, st, []handbrake.Progress{
		{Percent: 42.5, FPS: 31.25},
		{Percent: 7, FPS: 12},
	})

	out := body(t, scrape(t, Handler(st, w, ""), ""))
	want := []string{
		"recodarr_worker_active_encodes 2",
		`recodarr_jobs{status="encoding"} 2`,
		`recodarr_jobs{status="ready"} 0`,
		fmt.Sprintf(`recodarr_encode_progress_percent{job_id="%d"} 42.5`, ids[0]),
		fmt.Sprintf(`recodarr_encode_fps{job_id="%d"} 31.25`, ids[0]),
		fmt.Sprintf(`recodarr_encode_progress_percent{job_id="%d"} 7`, ids[1]),
		fmt.Sprintf(`recodarr_encode_fps{job_id="%d"} 12`, ids[1]),
	}
	for _, s := range want {
		if !strings.Contains(out, s) {
			t.Fatalf("the scrape is missing %q:\n%s", s, out)
		}
	}
	if strings.Contains(out, "recodarr_worker_last_tick_timestamp_seconds 0\n") {
		t.Fatalf("the last tick stayed at zero after the worker ran:\n%s", out)
	}
}

func TestHandlerDropsFinishedEncodes(t *testing.T) {
	st := openStore(t)
	w, ids := startEncodes(t, st, []handbrake.Progress{{Percent: 50, FPS: 20}})

	if !w.CancelEncoding(ids[0]) {
		t.Fatal("the encode was not running")
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(w.EncodingJobIDs()) > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	out := body(t, scrape(t, Handler(st, w, ""), ""))
	if !strings.Contains(out, "recodarr_worker_active_encodes 0") {
		t.Fatalf("a cancelled encode is still counted as active:\n%s", out)
	}
	if strings.Contains(out, "recodarr_encode_progress_percent{") {
		t.Fatalf("progress is still exported for a finished encode:\n%s", out)
	}
}
