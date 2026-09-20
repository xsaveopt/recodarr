package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func mustJob(t *testing.T, st *Store, r JobRow) int64 {
	t.Helper()
	if r.ArrKind == "" {
		r.ArrKind = "sonarr"
	}
	if r.Status == "" {
		r.Status = "waiting_for_seed"
	}
	id, err := st.InsertJob(context.Background(), r)
	if err != nil {
		t.Fatalf("insert job: %v", err)
	}
	return id
}

func mustGet(t *testing.T, st *Store, id int64) JobRow {
	t.Helper()
	row, err := st.GetJob(context.Background(), id)
	if err != nil {
		t.Fatalf("get job %d: %v", id, err)
	}
	return *row
}

func TestInsertJobFillsDefaults(t *testing.T) {
	st := openTestStore(t)
	id := mustJob(t, st, JobRow{Title: "Show", FilePath: "/m/a.mkv"})
	row := mustGet(t, st, id)
	if row.Tags != "[]" {
		t.Fatalf("got tags %q, want an empty JSON array", row.Tags)
	}
	if row.Source != "poll" {
		t.Fatalf("got source %q, want poll", row.Source)
	}
	if row.Attempts != 0 || row.Status != "waiting_for_seed" {
		t.Fatalf("unexpected fresh job: %+v", row)
	}
}

func TestGetJobReportsNotFound(t *testing.T) {
	st := openTestStore(t)
	if _, err := st.GetJob(context.Background(), 404); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestTransitionJobStatusIsConditional(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	id := mustJob(t, st, JobRow{Title: "j", FilePath: "/m/a.mkv", Status: "waiting_for_seed"})

	ok, err := st.TransitionJobStatus(ctx, id, "ready", "encoding")
	if err != nil || ok {
		t.Fatalf("got ok=%v (err %v), want a refused transition from the wrong status", ok, err)
	}
	ok, err = st.TransitionJobStatus(ctx, id, "waiting_for_seed", "ready")
	if err != nil || !ok {
		t.Fatalf("got ok=%v (err %v), want the transition to apply", ok, err)
	}
	if got := mustGet(t, st, id).Status; got != "ready" {
		t.Fatalf("got %q, want ready", got)
	}
	ok, _ = st.TransitionJobStatus(ctx, id, "waiting_for_seed", "ready")
	if ok {
		t.Fatal("replaying the same transition must not apply twice")
	}
}

func TestMarkJobEncodingClaimsOnce(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	id := mustJob(t, st, JobRow{Title: "j", FilePath: "/m/a.mkv", FileSize: 900, Status: "ready"})

	const racers = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	claims := 0
	wg.Add(racers)
	for i := 0; i < racers; i++ {
		go func() {
			defer wg.Done()
			ok, err := st.MarkJobEncoding(ctx, id)
			if err != nil {
				return
			}
			if ok {
				mu.Lock()
				claims++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if claims != 1 {
		t.Fatalf("got %d claims, want exactly one worker to win", claims)
	}
	row := mustGet(t, st, id)
	if row.Status != "encoding" {
		t.Fatalf("got %q, want encoding", row.Status)
	}
	if row.Attempts != 1 {
		t.Fatalf("got %d attempts, want 1", row.Attempts)
	}
	if !row.OriginalSize.Valid || row.OriginalSize.Int64 != 900 {
		t.Fatalf("got original_size %+v, want the file size snapshotted", row.OriginalSize)
	}
	if !row.StartedAt.Valid {
		t.Fatal("started_at was not set")
	}
}

func TestMarkJobEncodingRefusesPastTheAttemptCap(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	id := mustJob(t, st, JobRow{Title: "j", FilePath: "/m/a.mkv", Status: "ready"})

	for i := 1; i <= MaxJobAttempts; i++ {
		ok, err := st.MarkJobEncoding(ctx, id)
		if err != nil || !ok {
			t.Fatalf("attempt %d: got ok=%v err=%v, want a claim", i, ok, err)
		}
		if ok, err := st.TransitionJobStatus(ctx, id, "encoding", "ready"); err != nil || !ok {
			t.Fatalf("attempt %d: requeue failed: %v", i, err)
		}
	}
	ok, err := st.MarkJobEncoding(ctx, id)
	if err != nil || ok {
		t.Fatalf("got ok=%v (err %v), want the claim refused at the cap", ok, err)
	}
	if got := mustGet(t, st, id).Attempts; got != MaxJobAttempts {
		t.Fatalf("got %d attempts, want %d", got, MaxJobAttempts)
	}
}

func TestRequeueEncodingGivesTheAttemptBack(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	id := mustJob(t, st, JobRow{Title: "j", FilePath: "/m/a.mkv", FileSize: 10, Status: "ready"})
	if _, err := st.MarkJobEncoding(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := st.RequeueEncoding(ctx, id); err != nil {
		t.Fatal(err)
	}
	row := mustGet(t, st, id)
	if row.Status != "ready" || row.Attempts != 0 || row.StartedAt.Valid || row.OriginalSize.Valid {
		t.Fatalf("got %+v, want a clean ready job", row)
	}
	if err := st.RequeueEncoding(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got := mustGet(t, st, id).Attempts; got != 0 {
		t.Fatalf("got %d attempts, want the floor at 0", got)
	}
}

func TestTerminalMarkers(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	done := mustJob(t, st, JobRow{Title: "done", FilePath: "/m/a.mkv", FileSize: 1000, Status: "ready"})
	if _, err := st.MarkJobEncoding(ctx, done); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkJobDone(ctx, done, 400); err != nil {
		t.Fatal(err)
	}
	row := mustGet(t, st, done)
	if row.Status != "done" || !row.FinalSize.Valid || row.FinalSize.Int64 != 400 || !row.FinishedAt.Valid {
		t.Fatalf("got %+v, want a completed job", row)
	}

	failed := mustJob(t, st, JobRow{Title: "failed", FilePath: "/m/b.mkv", Status: "ready"})
	if err := st.MarkJobFailed(ctx, failed, "boom", "the log"); err != nil {
		t.Fatal(err)
	}
	row = mustGet(t, st, failed)
	if row.Status != "failed" || row.Error != "boom" || row.EncodeLog != "the log" {
		t.Fatalf("got %+v, want the failure recorded", row)
	}

	skipped := mustJob(t, st, JobRow{Title: "skipped", FilePath: "/m/c.mkv", Status: "ready"})
	if err := st.MarkJobSkipped(ctx, skipped, "too small"); err != nil {
		t.Fatal(err)
	}
	row = mustGet(t, st, skipped)
	if row.Status != "skipped" || row.Error != "too small" || row.EncodeLog != "" {
		t.Fatalf("got %+v, want the skip recorded with no log", row)
	}

	if err := st.SetRefreshError(ctx, done, "sonarr down"); err != nil {
		t.Fatal(err)
	}
	if got := mustGet(t, st, done).RefreshError; got != "sonarr down" {
		t.Fatalf("got %q, want the refresh error stored", got)
	}
	if err := st.SetRefreshError(ctx, done, ""); err != nil {
		t.Fatal(err)
	}
	if got := mustGet(t, st, done).RefreshError; got != "" {
		t.Fatalf("got %q, want the refresh error cleared", got)
	}
}

func TestRetryJobOnlyFromTerminalStatuses(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	failed := mustJob(t, st, JobRow{Title: "failed", FilePath: "/m/a.mkv", Status: "ready"})
	if err := st.MarkJobFailed(ctx, failed, "boom", "log"); err != nil {
		t.Fatal(err)
	}
	if err := st.RetryJob(ctx, failed); err != nil {
		t.Fatal(err)
	}
	row := mustGet(t, st, failed)
	if row.Status != "waiting_for_seed" || row.Error != "" || row.EncodeLog != "" || row.Attempts != 0 {
		t.Fatalf("got %+v, want a fully reset job", row)
	}

	encoding := mustJob(t, st, JobRow{Title: "encoding", FilePath: "/m/b.mkv", Status: "ready"})
	if _, err := st.MarkJobEncoding(ctx, encoding); err != nil {
		t.Fatal(err)
	}
	if err := st.RetryJob(ctx, encoding); err != nil {
		t.Fatal(err)
	}
	if got := mustGet(t, st, encoding).Status; got != "encoding" {
		t.Fatalf("got %q, want an in-flight encode left alone", got)
	}
}

func TestRetryAllFailedAndByIDs(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	a := mustJob(t, st, JobRow{Title: "a", FilePath: "/m/a.mkv", Status: "failed"})
	b := mustJob(t, st, JobRow{Title: "b", FilePath: "/m/b.mkv", Status: "failed"})
	ready := mustJob(t, st, JobRow{Title: "c", FilePath: "/m/c.mkv", Status: "ready"})

	n, err := st.RetryAllFailed(ctx)
	if err != nil || n != 2 {
		t.Fatalf("got %d (err %v), want both failed jobs retried", n, err)
	}
	if got := mustGet(t, st, ready).Status; got != "ready" {
		t.Fatalf("got %q, want the ready job untouched", got)
	}

	if err := st.MarkJobSkipped(ctx, a, "nope"); err != nil {
		t.Fatal(err)
	}
	n, err = st.RetryJobsByIDs(ctx, []int64{a, b, ready})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("got %d, want only the skipped job retried", n)
	}
	if n, err := st.RetryJobsByIDs(ctx, nil); err != nil || n != 0 {
		t.Fatalf("got %d (err %v), want a no-op for an empty id list", n, err)
	}
}

func TestDeleteJobRules(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	ready := mustJob(t, st, JobRow{Title: "ready", FilePath: "/m/a.mkv", Status: "ready"})
	if err := st.DeleteJob(ctx, ready); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetJob(ctx, ready); err != nil {
		t.Fatalf("a ready job must survive DeleteJob: %v", err)
	}

	done := mustJob(t, st, JobRow{Title: "done", FilePath: "/m/b.mkv", Status: "done"})
	if err := st.DeleteJob(ctx, done); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetJob(ctx, done); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want the done job deleted", err)
	}
}

func TestDeleteTerminalJobsWhitelistsStatuses(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	mustJob(t, st, JobRow{Title: "done", FilePath: "/m/a.mkv", Status: "done"})
	mustJob(t, st, JobRow{Title: "failed", FilePath: "/m/b.mkv", Status: "failed"})
	mustJob(t, st, JobRow{Title: "skipped", FilePath: "/m/c.mkv", Status: "skipped"})
	encoding := mustJob(t, st, JobRow{Title: "encoding", FilePath: "/m/d.mkv", Status: "ready"})
	if _, err := st.MarkJobEncoding(ctx, encoding); err != nil {
		t.Fatal(err)
	}

	n, err := st.DeleteTerminalJobs(ctx, []string{"encoding"})
	if err != nil || n != 0 {
		t.Fatalf("got %d (err %v), want encoding to be rejected as a delete target", n, err)
	}
	n, err = st.DeleteTerminalJobs(ctx, nil)
	if err != nil || n != 3 {
		t.Fatalf("got %d (err %v), want the three terminal jobs deleted", n, err)
	}
	if _, err := st.GetJob(ctx, encoding); err != nil {
		t.Fatalf("the encoding job must survive: %v", err)
	}
}

func TestBulkDeleteAndSetProfileSkipEncodingJobs(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	profileID, err := st.UpsertProfile(ctx, ProfileRow{Name: "bulk", Encoder: "x265"})
	if err != nil {
		t.Fatal(err)
	}
	done := mustJob(t, st, JobRow{Title: "done", FilePath: "/m/a.mkv", Status: "done"})
	encoding := mustJob(t, st, JobRow{Title: "enc", FilePath: "/m/b.mkv", Status: "ready"})
	if _, err := st.MarkJobEncoding(ctx, encoding); err != nil {
		t.Fatal(err)
	}

	n, err := st.SetJobsProfile(ctx, []int64{done, encoding}, sql.NullInt64{Int64: profileID, Valid: true})
	if err != nil || n != 1 {
		t.Fatalf("got %d (err %v), want only the non-encoding job updated", n, err)
	}
	n, err = st.DeleteJobsByIDs(ctx, []int64{done, encoding})
	if err != nil || n != 1 {
		t.Fatalf("got %d (err %v), want only the non-encoding job deleted", n, err)
	}
	if n, err := st.DeleteJobsByIDs(ctx, nil); err != nil || n != 0 {
		t.Fatalf("got %d (err %v), want a no-op", n, err)
	}
	if n, err := st.SetJobsProfile(ctx, nil, sql.NullInt64{}); err != nil || n != 0 {
		t.Fatalf("got %d (err %v), want a no-op", n, err)
	}
}

func TestRecoverOrphanEncoding(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	id := mustJob(t, st, JobRow{Title: "orphan", FilePath: "/m/a.mkv", Status: "ready"})
	if _, err := st.MarkJobEncoding(ctx, id); err != nil {
		t.Fatal(err)
	}

	paths, err := st.RecoverOrphanEncoding(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != "/m/a.mkv" {
		t.Fatalf("got %v, want the orphaned path reported", paths)
	}
	row := mustGet(t, st, id)
	if row.Status != "ready" || row.StartedAt.Valid {
		t.Fatalf("got %+v, want the job requeued", row)
	}
	if paths, err := st.RecoverOrphanEncoding(ctx); err != nil || len(paths) != 0 {
		t.Fatalf("got %v (err %v), want nothing left to recover", paths, err)
	}
}

func TestJobIdempotencyLookups(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	id := mustJob(t, st, JobRow{ArrInstanceID: 1, ArrItemID: 42, Title: "j", FilePath: "/m/a.mkv", Status: "ready"})

	has, err := st.HasJobForItem(ctx, "sonarr", 1, 42)
	if err != nil || !has {
		t.Fatalf("got %v (err %v), want the job found", has, err)
	}
	active, err := st.HasActiveJob(ctx, "sonarr", 1, 42)
	if err != nil || !active {
		t.Fatalf("got %v (err %v), want an active job", active, err)
	}

	if err := st.MarkJobDone(ctx, id, 1); err != nil {
		t.Fatal(err)
	}
	if has, _ := st.HasJobForItem(ctx, "sonarr", 1, 42); !has {
		t.Fatal("a terminal job must still block a re-insert for the same file id")
	}
	if active, _ := st.HasActiveJob(ctx, "sonarr", 1, 42); active {
		t.Fatal("a done job is not active")
	}
	if has, _ := st.HasJobForItem(ctx, "sonarr", 1, 43); has {
		t.Fatal("a different file id must be free to enqueue")
	}
	if has, _ := st.HasJobForItem(ctx, "radarr", 1, 42); has {
		t.Fatal("a different arr kind must be free to enqueue")
	}
	if has, _ := st.HasJobForItem(ctx, "sonarr", 2, 42); has {
		t.Fatal("a different instance must be free to enqueue")
	}
}

func TestJobsByStatusRespectsLimit(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		mustJob(t, st, JobRow{Title: fmt.Sprintf("j%d", i), FilePath: fmt.Sprintf("/m/%d.mkv", i), Status: "ready"})
	}
	mustJob(t, st, JobRow{Title: "other", FilePath: "/m/x.mkv", Status: "failed"})

	rows, err := st.JobsByStatus(ctx, "ready", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want the limit honoured", len(rows))
	}
	for _, r := range rows {
		if r.Status != "ready" {
			t.Fatalf("got a %q job in a ready query", r.Status)
		}
	}
	if rows[0].ID > rows[1].ID {
		t.Fatal("jobs must come back oldest first")
	}
}

func TestListJobsFiltersAndPages(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	profileID, err := st.UpsertProfile(ctx, ProfileRow{Name: "listed", Encoder: "x265"})
	if err != nil {
		t.Fatal(err)
	}
	pid := sql.NullInt64{Int64: profileID, Valid: true}

	mustJob(t, st, JobRow{ArrKind: "sonarr", Title: "Attack on Titan", FilePath: "/m/a.mkv", Status: "done", ProfileID: pid})
	mustJob(t, st, JobRow{ArrKind: "sonarr", Title: "Frieren", FilePath: "/m/b.mkv", Status: "failed"})
	mustJob(t, st, JobRow{ArrKind: "radarr", Title: "Dune", FilePath: "/m/c.mkv", Status: "done"})

	rows, total, err := st.ListJobs(ctx, JobListOptions{Statuses: []string{"done"}})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("got %d/%d, want the two done jobs", len(rows), total)
	}
	if rows[0].ID < rows[1].ID {
		t.Fatal("the default order is newest first")
	}

	if _, total, _ = st.ListJobs(ctx, JobListOptions{Kinds: []string{"radarr"}}); total != 1 {
		t.Fatalf("got %d, want one radarr job", total)
	}
	if _, total, _ = st.ListJobs(ctx, JobListOptions{ProfileID: profileID}); total != 1 {
		t.Fatalf("got %d, want one job on the profile", total)
	}
	if _, total, _ = st.ListJobs(ctx, JobListOptions{Search: "frier"}); total != 1 {
		t.Fatalf("got %d, want the case-insensitive title match", total)
	}
	if _, total, _ = st.ListJobs(ctx, JobListOptions{Statuses: []string{"done"}, Kinds: []string{"radarr"}}); total != 1 {
		t.Fatalf("got %d, want the filters to combine", total)
	}

	rows, total, err = st.ListJobs(ctx, JobListOptions{Limit: 1, Offset: 1, Ascending: true})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(rows) != 1 || rows[0].Title != "Frieren" {
		t.Fatalf("got %d rows of %d (%v), want the second job ascending", len(rows), total, rows)
	}

	if rows, _, _ := st.ListJobs(ctx, JobListOptions{Limit: 10_000}); len(rows) > 500 {
		t.Fatal("the page size must be capped at 500")
	}
	if _, _, err := st.ListJobs(ctx, JobListOptions{SortBy: "updated"}); err != nil {
		t.Fatalf("sorting by updated_at: %v", err)
	}
}

func TestGetJobStats(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	stats, err := st.GetJobStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats != (JobStatsRow{}) {
		t.Fatalf("got %+v, want zeroes on an empty database", stats)
	}

	mustJob(t, st, JobRow{Title: "a", FilePath: "/m/a.mkv", Status: "waiting_for_seed"})
	mustJob(t, st, JobRow{Title: "b", FilePath: "/m/b.mkv", Status: "waiting_for_hardlink"})
	mustJob(t, st, JobRow{Title: "c", FilePath: "/m/c.mkv", Status: "ready"})
	mustJob(t, st, JobRow{Title: "d", FilePath: "/m/d.mkv", Status: "failed"})
	mustJob(t, st, JobRow{Title: "e", FilePath: "/m/e.mkv", Status: "skipped"})
	done := mustJob(t, st, JobRow{Title: "f", FilePath: "/m/f.mkv", FileSize: 1000, Status: "ready"})
	if _, err := st.MarkJobEncoding(ctx, done); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkJobDone(ctx, done, 600); err != nil {
		t.Fatal(err)
	}

	stats, err = st.GetJobStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := JobStatsRow{WaitingForSeed: 1, WaitingForHardlink: 1, Ready: 1, Done: 1, Failed: 1, Skipped: 1, TotalSavedBytes: 400}
	if stats != want {
		t.Fatalf("got %+v, want %+v", stats, want)
	}
}

func TestArrInstanceSecretsAndSoftDelete(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	id, err := st.CreateArrInstance(ctx, ArrInstanceRow{Kind: "sonarr", Name: "s", URL: "http://s:8989", APIKey: "secret", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	if err := st.UpdateArrInstance(ctx, ArrInstanceRow{ID: id, Kind: "sonarr", Name: "renamed", URL: "http://s:8989", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	row, err := st.GetArrInstance(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if row.APIKey != "secret" {
		t.Fatalf("got api key %q, want a blank update to preserve it", row.APIKey)
	}
	if row.Name != "renamed" {
		t.Fatalf("got %q, want the rename applied", row.Name)
	}

	if err := st.UpdateArrInstance(ctx, ArrInstanceRow{ID: id, Kind: "sonarr", Name: "renamed", URL: "http://s:8989", APIKey: "rotated", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if row, _ := st.GetArrInstance(ctx, id); row.APIKey != "rotated" {
		t.Fatalf("got %q, want the new key stored", row.APIKey)
	}

	if err := st.DeleteArrInstance(ctx, id); err != nil {
		t.Fatal(err)
	}
	live, err := st.ListArrInstances(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 0 {
		t.Fatalf("got %d live instances, want the row soft-deleted", len(live))
	}
	all, err := st.ListArrInstancesIncludingDeleted(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || !all[0].DeletedAt.Valid {
		t.Fatalf("got %+v, want one soft-deleted row", all)
	}
	if _, err := st.GetArrInstance(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestQbitInstancePasswordIsPreservedOnBlankUpdate(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	id, err := st.UpsertQbitInstance(ctx, QbitInstanceRow{Name: "qbit", URL: "http://q:8080", Username: "u", Password: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertQbitInstance(ctx, QbitInstanceRow{ID: id, Name: "qbit", URL: "http://q:9090", Username: "u"}); err != nil {
		t.Fatal(err)
	}
	row, err := st.GetQbitInstance(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if row.Password != "p" || row.URL != "http://q:9090" {
		t.Fatalf("got %+v, want the password kept and the url updated", row)
	}

	first, err := st.FirstQbitInstance(ctx)
	if err != nil || first.ID != id {
		t.Fatalf("got %+v (err %v), want the only instance", first, err)
	}
	if err := st.DeleteQbitInstance(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := st.FirstQbitInstance(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound once the instance is gone", err)
	}
	if _, err := st.GetQbitInstance(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
	if list, err := st.ListQbitInstances(ctx); err != nil || len(list) != 0 {
		t.Fatalf("got %v (err %v), want an empty list", list, err)
	}
}

func TestUpsertProfileNormalizesDefaults(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	id, err := st.UpsertProfile(ctx, ProfileRow{Name: "normalized", Encoder: "x265", SkipBitrateUnit: "nonsense"})
	if err != nil {
		t.Fatal(err)
	}
	row, err := st.GetProfile(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if row.RateControl != "crf" {
		t.Fatalf("got rate control %q, want crf", row.RateControl)
	}
	if row.SkipBitrateUnit != "mb_per_hour" {
		t.Fatalf("got skip bitrate unit %q, want mb_per_hour", row.SkipBitrateUnit)
	}
	if row.AudioBitratesByChannels != "{}" {
		t.Fatalf("got %q, want an empty JSON object", row.AudioBitratesByChannels)
	}

	row.SkipBitrateUnit = "kbps"
	row.Quality = 19
	if _, err := st.UpsertProfile(ctx, *row); err != nil {
		t.Fatal(err)
	}
	updated, err := st.GetProfile(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if updated.SkipBitrateUnit != "kbps" || updated.Quality != 19 {
		t.Fatalf("got %+v, want the update applied", updated)
	}
	if _, err := st.GetProfile(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestDeleteProfileSoftDeletesAndDropsMappings(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	id, err := st.UpsertProfile(ctx, ProfileRow{Name: "doomed", Encoder: "x265"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateTagMapping(ctx, TagMappingRow{ArrKind: "sonarr", TagID: 5, TagLabel: "anime", ProfileID: id}); err != nil {
		t.Fatal(err)
	}

	if err := st.DeleteProfile(ctx, id); err != nil {
		t.Fatal(err)
	}
	mappings, err := st.ListTagMappings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(mappings) != 0 {
		t.Fatalf("got %+v, want the mappings dropped with the profile", mappings)
	}

	live, err := st.ListProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range live {
		if p.ID == id {
			t.Fatal("the deleted profile is still listed")
		}
	}
	all, err := st.ListProfilesIncludingDeleted(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, p := range all {
		if p.ID == id {
			found = p.DeletedAt.Valid
		}
	}
	if !found {
		t.Fatal("the profile row should survive as a soft delete so old jobs still resolve it")
	}
	if _, err := st.GetProfile(ctx, id); err != nil {
		t.Fatalf("a soft-deleted profile must still be fetchable by id: %v", err)
	}
}

func TestTagMappingsByKindIncludeBoth(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	id, err := st.UpsertProfile(ctx, ProfileRow{Name: "mapped", Encoder: "x265"})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"sonarr", "radarr", "both"} {
		if _, err := st.CreateTagMapping(ctx, TagMappingRow{ArrKind: kind, TagID: 1, TagLabel: kind, ProfileID: id}); err != nil {
			t.Fatal(err)
		}
	}

	sonarr, err := st.ListTagMappingsByKind(ctx, "sonarr")
	if err != nil {
		t.Fatal(err)
	}
	if len(sonarr) != 2 {
		t.Fatalf("got %d mappings, want the sonarr one plus 'both'", len(sonarr))
	}
	for _, m := range sonarr {
		if m.ArrKind == "radarr" {
			t.Fatal("a radarr-only mapping leaked into the sonarr set")
		}
	}

	all, err := st.ListTagMappings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d mappings, want 3", len(all))
	}
	if err := st.DeleteTagMapping(ctx, all[0].ID); err != nil {
		t.Fatal(err)
	}
	if rest, _ := st.ListTagMappings(ctx); len(rest) != 2 {
		t.Fatalf("got %d mappings, want one removed", len(rest))
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	if _, ok, err := st.GetSetting(ctx, "missing"); err != nil || ok {
		t.Fatalf("got ok=%v (err %v), want a miss", ok, err)
	}
	if err := st.SetSetting(ctx, "worker_interval_seconds", "45"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, "worker_interval_seconds", "60"); err != nil {
		t.Fatal(err)
	}
	v, ok, err := st.GetSetting(ctx, "worker_interval_seconds")
	if err != nil || !ok || v != "60" {
		t.Fatalf("got %q ok=%v (err %v), want the upserted value", v, ok, err)
	}
	all, err := st.GetAllSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if all["worker_interval_seconds"] != "60" {
		t.Fatalf("got %v, want the setting listed", all)
	}
}

func TestPlaceholdersAndHelpers(t *testing.T) {
	cases := map[int]string{0: "", 1: "?", 3: "?,?,?"}
	for n, want := range cases {
		if got := placeholders(n); got != want {
			t.Fatalf("placeholders(%d) = %q, want %q", n, got, want)
		}
	}
	if placeholders(-1) != "" {
		t.Fatal("a negative count must produce no placeholders")
	}
	if boolToInt(true) != 1 || boolToInt(false) != 0 {
		t.Fatal("boolToInt is wrong")
	}
	if !isTerminalDeletable("done") || !isTerminalDeletable("ready") {
		t.Fatal("done and ready are deletable")
	}
	if isTerminalDeletable("encoding") {
		t.Fatal("encoding must never be deletable")
	}
}
