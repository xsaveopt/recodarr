package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return st
}

func sampleRequest() JobRequest {
	return JobRequest{Filename: "S01E01.mkv", SizeBytes: 10, OutputContainer: "mkv"}
}

func TestCreateStartsAwaitingSourceUnlessLocal(t *testing.T) {
	st := openTestStore(t)

	remote, err := st.Create(sampleRequest(), false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if remote.State != StateAwaitingSource || remote.LocalSource {
		t.Fatalf("got %+v, want awaiting_source", remote)
	}

	local, err := st.Create(sampleRequest(), true)
	if err != nil {
		t.Fatalf("create local: %v", err)
	}
	if local.State != StateQueued || !local.LocalSource {
		t.Fatalf("got %+v, want a queued local job", local)
	}
	if remote.ID == local.ID {
		t.Fatal("two jobs share an id")
	}
	if _, err := os.Stat(filepath.Join(st.JobDir(remote.ID), "state.json")); err != nil {
		t.Fatalf("the manifest was not written: %v", err)
	}
}

func TestGetAndListReturnCopies(t *testing.T) {
	st := openTestStore(t)
	js, err := st.Create(sampleRequest(), false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got, ok := st.Get(js.ID)
	if !ok {
		t.Fatal("get missed a fresh job")
	}
	got.State = StateDone
	again, _ := st.Get(js.ID)
	if again.State != StateAwaitingSource {
		t.Fatal("mutating the returned snapshot changed the store")
	}

	list := st.List()
	if len(list) != 1 {
		t.Fatalf("got %d jobs, want 1", len(list))
	}
	list[0].State = StateFailed
	if again, _ := st.Get(js.ID); again.State != StateAwaitingSource {
		t.Fatal("mutating a listed snapshot changed the store")
	}

	if _, ok := st.Get("missing"); ok {
		t.Fatal("get found a job that does not exist")
	}
}

func TestUpdatePersistsAndReportsMissingJobs(t *testing.T) {
	st := openTestStore(t)
	js, _ := st.Create(sampleRequest(), false)

	if err := st.Update(js.ID, func(j *JobStateSnapshot) error {
		j.State = StateQueued
		return nil
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	reopened, err := OpenStore(st.Root())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got, _ := reopened.Get(js.ID); got.State != StateQueued {
		t.Fatalf("got %s after reopening, want queued", got.State)
	}

	boom := errors.New("boom")
	if err := st.Update(js.ID, func(*JobStateSnapshot) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("got %v, want the callback error", err)
	}
	if err := st.Update("missing", func(*JobStateSnapshot) error { return nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestClaimQueuedTakesTheOldestQueuedJob(t *testing.T) {
	st := openTestStore(t)
	if _, ok := st.ClaimQueued(); ok {
		t.Fatal("claimed from an empty store")
	}

	waiting, _ := st.Create(sampleRequest(), false)
	newer, _ := st.Create(sampleRequest(), true)
	older, _ := st.Create(sampleRequest(), true)
	if err := st.Update(older.ID, func(j *JobStateSnapshot) error {
		j.CreatedAt = time.Now().Add(-time.Hour)
		return nil
	}); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	first, ok := st.ClaimQueued()
	if !ok || first.ID != older.ID {
		t.Fatalf("got %+v, want the older job claimed first", first)
	}
	if first.State != StateEncoding || first.StartedAt == nil {
		t.Fatalf("got %+v, want it marked encoding with a start time", first)
	}

	second, ok := st.ClaimQueued()
	if !ok || second.ID != newer.ID {
		t.Fatalf("got %+v, want the newer job next", second)
	}
	if _, ok := st.ClaimQueued(); ok {
		t.Fatal("claimed a job that was never queued")
	}
	if got, _ := st.Get(waiting.ID); got.State != StateAwaitingSource {
		t.Fatalf("got %s, want the awaiting job untouched", got.State)
	}
}

func TestDeleteRemovesTheJobAndItsDirectory(t *testing.T) {
	st := openTestStore(t)
	js, _ := st.Create(sampleRequest(), false)
	if err := st.Delete(js.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := st.Get(js.ID); ok {
		t.Fatal("the job is still in memory")
	}
	if _, err := os.Stat(st.JobDir(js.ID)); !os.IsNotExist(err) {
		t.Fatal("the job directory survived the delete")
	}
	if err := st.Delete("missing"); err != nil {
		t.Fatalf("deleting a missing job failed: %v", err)
	}
}

func TestOpenStoreRecoversInFlightJobsAsFailed(t *testing.T) {
	st := openTestStore(t)
	done, _ := st.Create(sampleRequest(), true)
	_ = st.Update(done.ID, func(j *JobStateSnapshot) error {
		j.State = StateDone
		return nil
	})
	running, _ := st.Create(sampleRequest(), true)
	if _, ok := st.ClaimQueued(); !ok {
		t.Fatal("claim failed")
	}

	broken := filepath.Join(st.Root(), "jobs", "broken")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(broken, "state.json"), []byte("{nope"), 0o644); err != nil {
		t.Fatalf("write broken manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(st.Root(), "jobs", "stray.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write stray file: %v", err)
	}

	reopened, err := OpenStore(st.Root())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if len(reopened.List()) != 2 {
		t.Fatalf("got %d jobs, want the two valid ones", len(reopened.List()))
	}
	got, _ := reopened.Get(running.ID)
	if got.State != StateFailed || got.Error == "" || got.FinishedAt == nil {
		t.Fatalf("got %+v, want the encoding job failed on restart", got)
	}
	if kept, _ := reopened.Get(done.ID); kept.State != StateDone {
		t.Fatalf("got %s, want the done job left alone", kept.State)
	}
}

func TestPathsFallBackToMkv(t *testing.T) {
	st := openTestStore(t)
	js := &JobStateSnapshot{ID: "abc", Request: &JobRequest{Filename: "noext"}}
	if got := st.SourcePath(js); filepath.Base(got) != "source.mkv" {
		t.Fatalf("got %s, want source.mkv", got)
	}
	if got := st.OutputPath(js); filepath.Base(got) != "output.mkv" {
		t.Fatalf("got %s, want output.mkv", got)
	}

	js.Request = &JobRequest{Filename: "movie.mp4", OutputContainer: "mp4"}
	if got := st.SourcePath(js); filepath.Base(got) != "source.mp4" {
		t.Fatalf("got %s, want source.mp4", got)
	}
	if got := st.OutputPath(js); filepath.Base(got) != "output.mp4" {
		t.Fatalf("got %s, want output.mp4", got)
	}
	if got := st.LogPath(js); got != filepath.Join(st.JobDir("abc"), "handbrake.log") {
		t.Fatalf("got %s, want the log inside the job dir", got)
	}
}

func TestStateTerminal(t *testing.T) {
	for _, s := range []State{StateDone, StateFailed, StateCancelled} {
		if !s.Terminal() {
			t.Fatalf("%s should be terminal", s)
		}
	}
	for _, s := range []State{StateAwaitingSource, StateQueued, StateEncoding} {
		if s.Terminal() {
			t.Fatalf("%s should not be terminal", s)
		}
	}
}
