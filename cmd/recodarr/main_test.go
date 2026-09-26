package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/xsaveopt/recodarr/internal/handbrake"
	"github.com/xsaveopt/recodarr/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "recodarr.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestRecoverOrphanEncodesRequeuesAndRemovesStaleTemps(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	dir := t.TempDir()

	media := filepath.Join(dir, "Show.S01E01.mkv")
	touch(t, media)
	staleMkv := handbrake.TempPath(media, "mkv")
	staleMp4 := handbrake.TempPath(media, "mp4")
	touch(t, staleMkv)
	touch(t, staleMp4)

	sibling := filepath.Join(dir, "Show.S01E02.mkv")
	touch(t, sibling)
	siblingTemp := handbrake.TempPath(sibling, "mkv")
	touch(t, siblingTemp)
	unrelated := filepath.Join(dir, ".hidden")
	touch(t, unrelated)

	orphan, err := st.InsertJob(ctx, store.JobRow{ArrKind: "sonarr", Title: "orphan", FilePath: media, Status: "encoding"})
	if err != nil {
		t.Fatal(err)
	}
	vanished, err := st.InsertJob(ctx, store.JobRow{ArrKind: "sonarr", Title: "vanished", FilePath: filepath.Join(dir, "gone", "ep.mkv"), Status: "encoding"})
	if err != nil {
		t.Fatal(err)
	}
	done, err := st.InsertJob(ctx, store.JobRow{ArrKind: "sonarr", Title: "done", FilePath: sibling, Status: "done"})
	if err != nil {
		t.Fatal(err)
	}

	recoverOrphanEncodes(ctx, st)

	for id, want := range map[int64]string{orphan: "ready", vanished: "ready", done: "done"} {
		row, err := st.GetJob(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if row.Status != want {
			t.Fatalf("job %d: got %q, want %q", id, row.Status, want)
		}
	}
	if exists(staleMkv) || exists(staleMp4) {
		t.Fatal("stale encode temps for the orphaned job were left behind")
	}
	for _, keep := range []string{media, sibling, siblingTemp, unrelated} {
		if !exists(keep) {
			t.Fatalf("%s was removed but does not belong to an orphaned encode", filepath.Base(keep))
		}
	}
}

func TestRecoverOrphanEncodesIsANoOpWithoutEncodingJobs(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	dir := t.TempDir()
	media := filepath.Join(dir, "Movie.mkv")
	temp := handbrake.TempPath(media, "mkv")
	touch(t, temp)
	id, err := st.InsertJob(ctx, store.JobRow{ArrKind: "radarr", Title: "ready", FilePath: media, Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}

	recoverOrphanEncodes(ctx, st)

	if !exists(temp) {
		t.Fatal("a temp was removed although no job was orphaned")
	}
	row, err := st.GetJob(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != "ready" {
		t.Fatalf("got %q, want the ready job untouched", row.Status)
	}
}
