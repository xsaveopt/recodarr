package job

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xsaveopt/recodarr/internal/handbrake"
	"github.com/xsaveopt/recodarr/internal/store"
)

type encodeCall struct {
	ctx      context.Context
	n        int
	source   string
	settings handbrake.Settings
}

type fakeEncoder struct {
	mu      sync.Mutex
	calls   []handbrake.Settings
	started chan int
	fn      func(c encodeCall) (handbrake.RunResult, error)
}

func newFakeEncoder(fn func(c encodeCall) (handbrake.RunResult, error)) *fakeEncoder {
	return &fakeEncoder{started: make(chan int, 16), fn: fn}
}

func (f *fakeEncoder) Encode(ctx context.Context, sourcePath string, s handbrake.Settings, _ func(handbrake.Progress)) (handbrake.RunResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	n := len(f.calls)
	f.mu.Unlock()
	f.started <- n
	return f.fn(encodeCall{ctx: ctx, n: n, source: sourcePath, settings: s})
}

func (f *fakeEncoder) settings() []handbrake.Settings {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]handbrake.Settings(nil), f.calls...)
}

func writeTemp(t *testing.T, c encodeCall, size int64) handbrake.RunResult {
	t.Helper()
	tmp := handbrake.TempPath(c.source, c.settings.ContainerFormat)
	if err := os.WriteFile(tmp, []byte(strings.Repeat("e", int(size))), 0o644); err != nil {
		t.Errorf("write temp: %v", err)
	}
	return handbrake.RunResult{FinalSize: size, TempPath: tmp, Log: fmt.Sprintf("pass %d log", c.n)}
}

func sizedEncoder(t *testing.T, sizes ...int64) *fakeEncoder {
	t.Helper()
	return newFakeEncoder(func(c encodeCall) (handbrake.RunResult, error) {
		size := sizes[len(sizes)-1]
		if c.n <= len(sizes) {
			size = sizes[c.n-1]
		}
		return writeTemp(t, c, size), nil
	})
}

func useEncoder(w *Worker, e RemoteEncoder) {
	w.SetRemoteEncoderResolver(func(context.Context) RemoteEncoder { return e })
}

func mediaFile(t *testing.T, size int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "Show.S01E01.mkv")
	if err := os.WriteFile(path, []byte(strings.Repeat("o", size)), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func newProfile(t *testing.T, st *store.Store, p store.ProfileRow) int64 {
	t.Helper()
	if p.Name == "" {
		p.Name = "test"
	}
	if p.Encoder == "" {
		p.Encoder = "x265"
	}
	if p.ContainerFormat == "" {
		p.ContainerFormat = "mkv"
	}
	if p.AudioEncoder == "" {
		p.AudioEncoder = "copy"
	}
	id, err := st.UpsertProfile(context.Background(), p)
	if err != nil {
		t.Fatalf("upsert profile: %v", err)
	}
	return id
}

func claimedJob(t *testing.T, st *store.Store, r store.JobRow) store.JobRow {
	t.Helper()
	ctx := context.Background()
	r.Status = string(StatusReady)
	id := insertJob(t, st, r)
	ok, err := st.MarkJobEncoding(ctx, id)
	if err != nil || !ok {
		t.Fatalf("claim job: ok=%v err=%v", ok, err)
	}
	row, err := st.GetJob(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return *row
}

func reload(t *testing.T, st *store.Store, id int64) *store.JobRow {
	t.Helper()
	row, err := st.GetJob(context.Background(), id)
	if err != nil {
		t.Fatalf("get job %d: %v", id, err)
	}
	return row
}

func setSettings(t *testing.T, st *store.Store, kv ...string) {
	t.Helper()
	for i := 0; i+1 < len(kv); i += 2 {
		if err := st.SetSetting(context.Background(), kv[i], kv[i+1]); err != nil {
			t.Fatal(err)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func assertNoTemp(t *testing.T, source string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(source))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if handbrake.IsTempPath(e.Name()) {
			t.Fatalf("temp file %q was left behind", e.Name())
		}
	}
}

func waitIdle(t *testing.T, w *Worker) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(w.EncodingJobIDs()) > 0 {
		if time.Now().After(deadline) {
			t.Fatal("encode never finished")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitStarted(t *testing.T, f *fakeEncoder) {
	t.Helper()
	select {
	case <-f.started:
	case <-time.After(5 * time.Second):
		t.Fatal("encoder was never called")
	}
}

func TestEncodeOneFailsJobsWithoutAProfile(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	w := NewWorker(st)
	enc := sizedEncoder(t, 10)
	useEncoder(w, enc)
	j := claimedJob(t, st, store.JobRow{Title: "orphan", FilePath: mediaFile(t, 100), FileSize: 100})

	w.encodeOne(ctx, ctx, j)

	row := reload(t, st, j.ID)
	if row.Status != string(StatusFailed) || !strings.Contains(row.Error, "no profile assigned") {
		t.Fatalf("got status %q error %q, want failed for the missing profile", row.Status, row.Error)
	}
	if len(enc.settings()) != 0 {
		t.Fatal("encoder ran for a job without a profile")
	}
}

func TestEncodeOneFailsWhenTheProfileIsGone(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	w := NewWorker(st)
	enc := sizedEncoder(t, 10)
	useEncoder(w, enc)
	j := claimedJob(t, st, store.JobRow{Title: "stale", FilePath: mediaFile(t, 100), FileSize: 100, ProfileID: sql.NullInt64{Int64: 9999, Valid: true}})

	w.encodeOne(ctx, ctx, j)

	row := reload(t, st, j.ID)
	if row.Status != string(StatusFailed) || !strings.HasPrefix(row.Error, "profile lookup") {
		t.Fatalf("got status %q error %q, want a profile lookup failure", row.Status, row.Error)
	}
	if len(enc.settings()) != 0 {
		t.Fatal("encoder ran without a profile")
	}
}

func TestEncodeOneAppliesTheCurrentTagMapping(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	w := NewWorker(st)
	enc := sizedEncoder(t, 10)
	useEncoder(w, enc)
	old := newProfile(t, st, store.ProfileRow{Name: "old", Quality: 20})
	current := newProfile(t, st, store.ProfileRow{Name: "current", Quality: 28})
	if _, err := st.CreateTagMapping(ctx, store.TagMappingRow{ArrKind: "sonarr", TagID: 1, TagLabel: "anime", ProfileID: current}); err != nil {
		t.Fatal(err)
	}
	j := claimedJob(t, st, store.JobRow{Title: "t", FilePath: mediaFile(t, 100), FileSize: 100, Tags: `["anime"]`, ProfileID: sql.NullInt64{Int64: old, Valid: true}})

	w.encodeOne(ctx, ctx, j)

	row := reload(t, st, j.ID)
	if !row.ProfileID.Valid || row.ProfileID.Int64 != current {
		t.Fatalf("got profile %+v, want the re-resolved profile %d stored", row.ProfileID, current)
	}
	calls := enc.settings()
	if len(calls) != 1 || calls[0].Quality != 28 {
		t.Fatalf("got %+v, want one encode with the current profile quality", calls)
	}
}

func TestEncodeOneSkipsFilteredJobsAndWritesASkipSidecar(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	w := NewWorker(st)
	enc := sizedEncoder(t, 10)
	useEncoder(w, enc)
	setSettings(t, st, "output_suffix_enabled", "true", "output_suffix", "marker")
	pid := newProfile(t, st, store.ProfileRow{Name: "tiny", SkipFileSizeMB: 100})
	src := mediaFile(t, 100)
	j := claimedJob(t, st, store.JobRow{Title: "small", FilePath: src, FileSize: 100, ProfileID: sql.NullInt64{Int64: pid, Valid: true}})

	w.encodeOne(ctx, ctx, j)

	row := reload(t, st, j.ID)
	if row.Status != string(StatusSkipped) || !strings.Contains(row.Error, "file too small") {
		t.Fatalf("got status %q error %q, want skipped by the size filter", row.Status, row.Error)
	}
	if len(enc.settings()) != 0 {
		t.Fatal("encoder ran for a filtered job")
	}
	body := readFile(t, sidecarPath(src, "marker"))
	if !strings.Contains(body, "status=skipped") || !strings.Contains(body, "profile=tiny") {
		t.Fatalf("skip sidecar missing fields:\n%s", body)
	}
}

func TestEncodeOneCommitsAndMarksDone(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	w := NewWorker(st)
	enc := sizedEncoder(t, 40)
	useEncoder(w, enc)
	setSettings(t, st, "output_suffix_enabled", "true")
	pid := newProfile(t, st, store.ProfileRow{Name: "hevc", Quality: 22, RateControl: "crf"})
	src := mediaFile(t, 100)
	j := claimedJob(t, st, store.JobRow{Title: "done", FilePath: src, FileSize: 100, ProfileID: sql.NullInt64{Int64: pid, Valid: true}})

	w.encodeOne(ctx, ctx, j)

	row := reload(t, st, j.ID)
	if row.Status != string(StatusDone) || row.Error != "" {
		t.Fatalf("got status %q error %q, want done", row.Status, row.Error)
	}
	if !row.FinalSize.Valid || row.FinalSize.Int64 != 40 {
		t.Fatalf("got final size %+v, want 40", row.FinalSize)
	}
	if got := readFile(t, src); got != strings.Repeat("e", 40) {
		t.Fatalf("source was not replaced by the encode, got %d bytes", len(got))
	}
	assertNoTemp(t, src)
	calls := enc.settings()
	if len(calls) != 1 || !calls[0].NoCommit || calls[0].Quality != 22 {
		t.Fatalf("got %+v, want one NoCommit encode at quality 22", calls)
	}
	body := readFile(t, sidecarPath(src, "recodarr"))
	for _, want := range []string{"quality=22", "original_size=100", "final_size=40", "saved_percent=60.0"} {
		if !strings.Contains(body, want) {
			t.Fatalf("sidecar missing %q:\n%s", want, body)
		}
	}
}

func TestEncodeOneWritesNoSidecarWhenDisabled(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	w := NewWorker(st)
	useEncoder(w, sizedEncoder(t, 40))
	pid := newProfile(t, st, store.ProfileRow{})
	src := mediaFile(t, 100)
	j := claimedJob(t, st, store.JobRow{Title: "t", FilePath: src, FileSize: 100, ProfileID: sql.NullInt64{Int64: pid, Valid: true}})

	w.encodeOne(ctx, ctx, j)

	if got := jobStatus(t, st, j.ID); got != string(StatusDone) {
		t.Fatalf("got %q, want done", got)
	}
	if _, err := os.Stat(sidecarPath(src, "recodarr")); !os.IsNotExist(err) {
		t.Fatalf("sidecar written although the suffix is disabled (err %v)", err)
	}
}

func TestEncodeOneFailsWhenTheCommitFails(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	w := NewWorker(st)
	useEncoder(w, newFakeEncoder(func(c encodeCall) (handbrake.RunResult, error) {
		return handbrake.RunResult{FinalSize: 10, TempPath: filepath.Join(filepath.Dir(c.source), ".missing.recodarr.tmp.mkv"), Log: "ok"}, nil
	}))
	pid := newProfile(t, st, store.ProfileRow{})
	src := mediaFile(t, 100)
	j := claimedJob(t, st, store.JobRow{Title: "t", FilePath: src, FileSize: 100, ProfileID: sql.NullInt64{Int64: pid, Valid: true}})

	w.encodeOne(ctx, ctx, j)

	row := reload(t, st, j.ID)
	if row.Status != string(StatusFailed) || !strings.Contains(row.Error, "commit rename") {
		t.Fatalf("got status %q error %q, want a commit failure", row.Status, row.Error)
	}
	if got := readFile(t, src); got != strings.Repeat("o", 100) {
		t.Fatal("source changed although the commit failed")
	}
}

func TestEncodeOneRecordsTheFailureLogAndDiscardsTheTemp(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	w := NewWorker(st)
	useEncoder(w, newFakeEncoder(func(c encodeCall) (handbrake.RunResult, error) {
		res := writeTemp(t, c, 10)
		res.Log = "line one\nencoder exploded"
		return res, errors.New("encoder exploded")
	}))
	pid := newProfile(t, st, store.ProfileRow{})
	src := mediaFile(t, 100)
	j := claimedJob(t, st, store.JobRow{Title: "t", FilePath: src, FileSize: 100, ProfileID: sql.NullInt64{Int64: pid, Valid: true}})

	w.encodeOne(ctx, ctx, j)

	row := reload(t, st, j.ID)
	if row.Status != string(StatusFailed) || row.Error != "encoder exploded" {
		t.Fatalf("got status %q error %q, want the encoder error", row.Status, row.Error)
	}
	if !strings.Contains(row.EncodeLog, "line one") || !strings.Contains(row.EncodeLog, "encoder exploded") {
		t.Fatalf("encode log %q does not carry the encoder output", row.EncodeLog)
	}
	assertNoTemp(t, src)
	if got := readFile(t, src); got != strings.Repeat("o", 100) {
		t.Fatal("source changed although the encode failed")
	}
}

func TestEncodeOneFailureLogSpansEveryRetry(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	w := NewWorker(st)
	useEncoder(w, newFakeEncoder(func(c encodeCall) (handbrake.RunResult, error) {
		if c.n == 1 {
			return writeTemp(t, c, 500), nil
		}
		return handbrake.RunResult{Log: "second pass crashed"}, errors.New("crash")
	}))
	pid := newProfile(t, st, store.ProfileRow{Quality: 20, BloatPolicy: "retry_higher_crf", BloatRetryMax: 2, BloatRetryStep: 4})
	src := mediaFile(t, 100)
	j := claimedJob(t, st, store.JobRow{Title: "t", FilePath: src, FileSize: 100, ProfileID: sql.NullInt64{Int64: pid, Valid: true}})

	w.encodeOne(ctx, ctx, j)

	row := reload(t, st, j.ID)
	if row.Status != string(StatusFailed) {
		t.Fatalf("got %q, want failed", row.Status)
	}
	for _, want := range []string{"pass 1 log", "--- retry 1 (CRF 24) ---", "second pass crashed"} {
		if !strings.Contains(row.EncodeLog, want) {
			t.Fatalf("encode log missing %q:\n%s", want, row.EncodeLog)
		}
	}
	assertNoTemp(t, src)
}

func TestEncodeOneMarksAUserCancelAsCancelled(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	w := NewWorker(st)
	useEncoder(w, newFakeEncoder(func(c encodeCall) (handbrake.RunResult, error) {
		<-c.ctx.Done()
		return handbrake.RunResult{Log: "killed"}, c.ctx.Err()
	}))
	pid := newProfile(t, st, store.ProfileRow{})
	j := claimedJob(t, st, store.JobRow{Title: "t", FilePath: mediaFile(t, 100), FileSize: 100, ProfileID: sql.NullInt64{Int64: pid, Valid: true}})

	encCtx, cancel := context.WithCancel(ctx)
	cancel()
	w.encodeOne(encCtx, ctx, j)

	row := reload(t, st, j.ID)
	if row.Status != string(StatusFailed) || row.Error != "cancelled" {
		t.Fatalf("got status %q error %q, want failed as cancelled", row.Status, row.Error)
	}
}

func TestEncodeOneKeepOriginalDiscardsALargerEncode(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	w := NewWorker(st)
	enc := sizedEncoder(t, 150)
	useEncoder(w, enc)
	setSettings(t, st, "output_suffix_enabled", "true")
	pid := newProfile(t, st, store.ProfileRow{BloatPolicy: "keep_original", BloatRetryMax: 5})
	src := mediaFile(t, 100)
	j := claimedJob(t, st, store.JobRow{Title: "t", FilePath: src, FileSize: 100, ProfileID: sql.NullInt64{Int64: pid, Valid: true}})

	w.encodeOne(ctx, ctx, j)

	row := reload(t, st, j.ID)
	if row.Status != string(StatusSkipped) || !strings.Contains(row.Error, "kept original") {
		t.Fatalf("got status %q error %q, want skipped with the original kept", row.Status, row.Error)
	}
	if got := readFile(t, src); got != strings.Repeat("o", 100) {
		t.Fatal("the original was replaced by a larger encode")
	}
	assertNoTemp(t, src)
	if n := len(enc.settings()); n != 1 {
		t.Fatalf("keep_original ran %d encodes, want exactly one", n)
	}
	if !enc.settings()[0].NoCommit {
		t.Fatal("the size guard needs the encoder to leave the temp uncommitted")
	}
	if body := readFile(t, sidecarPath(src, "recodarr")); !strings.Contains(body, "status=skipped") {
		t.Fatalf("want a skip sidecar, got:\n%s", body)
	}
}

func TestEncodeOneKeepOriginalCommitsASmallerEncode(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	w := NewWorker(st)
	useEncoder(w, sizedEncoder(t, 100))
	pid := newProfile(t, st, store.ProfileRow{BloatPolicy: "keep_original"})
	src := mediaFile(t, 100)
	j := claimedJob(t, st, store.JobRow{Title: "t", FilePath: src, FileSize: 100, ProfileID: sql.NullInt64{Int64: pid, Valid: true}})

	w.encodeOne(ctx, ctx, j)

	if got := jobStatus(t, st, j.ID); got != string(StatusDone) {
		t.Fatalf("an encode no larger than the source: got %q, want done", got)
	}
	if got := readFile(t, src); got != strings.Repeat("e", 100) {
		t.Fatal("the encode was not committed")
	}
	assertNoTemp(t, src)
}

func TestEncodeOneMinSavingsThreshold(t *testing.T) {
	cases := []struct {
		name  string
		final int64
		want  Status
	}{
		{"meets the savings target", 900, StatusDone},
		{"smaller but short of the target", 950, StatusSkipped},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newTestStore(t)
			ctx := context.Background()
			w := NewWorker(st)
			useEncoder(w, sizedEncoder(t, tc.final))
			pid := newProfile(t, st, store.ProfileRow{BloatPolicy: "keep_original", BloatMinSavingsPercent: 10})
			src := mediaFile(t, 1000)
			j := claimedJob(t, st, store.JobRow{Title: "t", FilePath: src, FileSize: 1000, ProfileID: sql.NullInt64{Int64: pid, Valid: true}})

			w.encodeOne(ctx, ctx, j)

			if got := jobStatus(t, st, j.ID); got != string(tc.want) {
				t.Fatalf("final %d of 1000 with a 10%% target: got %q, want %q", tc.final, got, tc.want)
			}
			assertNoTemp(t, src)
		})
	}
}

func TestEncodeOneUnknownBloatPolicyCommitsWithoutAGuard(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	w := NewWorker(st)
	enc := sizedEncoder(t, 500)
	useEncoder(w, enc)
	pid := newProfile(t, st, store.ProfileRow{BloatPolicy: "something_else", BloatRetryMax: 3})
	src := mediaFile(t, 100)
	j := claimedJob(t, st, store.JobRow{Title: "t", FilePath: src, FileSize: 100, ProfileID: sql.NullInt64{Int64: pid, Valid: true}})

	w.encodeOne(ctx, ctx, j)

	if got := jobStatus(t, st, j.ID); got != string(StatusDone) {
		t.Fatalf("got %q, want an unknown policy to behave as off", got)
	}
	if n := len(enc.settings()); n != 1 {
		t.Fatalf("got %d encodes, want one", n)
	}
}

func TestEncodeOneRetryHigherCRFStepsQualityUntilSmaller(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	w := NewWorker(st)
	enc := sizedEncoder(t, 300, 200, 80)
	useEncoder(w, enc)
	pid := newProfile(t, st, store.ProfileRow{Quality: 20, BloatPolicy: "retry_higher_crf", BloatRetryMax: 3, BloatRetryStep: 3})
	src := mediaFile(t, 100)
	j := claimedJob(t, st, store.JobRow{Title: "t", FilePath: src, FileSize: 100, ProfileID: sql.NullInt64{Int64: pid, Valid: true}})

	w.encodeOne(ctx, ctx, j)

	row := reload(t, st, j.ID)
	if row.Status != string(StatusDone) || row.FinalSize.Int64 != 80 {
		t.Fatalf("got status %q final %+v, want done at 80 bytes", row.Status, row.FinalSize)
	}
	var got []int
	for _, s := range enc.settings() {
		got = append(got, s.Quality)
	}
	if fmt.Sprint(got) != "[20 23 26]" {
		t.Fatalf("got qualities %v, want [20 23 26]", got)
	}
	assertNoTemp(t, src)
}

func TestEncodeOneRetryHigherCRFGivesUpAfterMaxRetries(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	w := NewWorker(st)
	enc := sizedEncoder(t, 500)
	useEncoder(w, enc)
	pid := newProfile(t, st, store.ProfileRow{Quality: 20, BloatPolicy: "retry_higher_crf", BloatRetryMax: 2})
	src := mediaFile(t, 100)
	j := claimedJob(t, st, store.JobRow{Title: "t", FilePath: src, FileSize: 100, ProfileID: sql.NullInt64{Int64: pid, Valid: true}})

	w.encodeOne(ctx, ctx, j)

	row := reload(t, st, j.ID)
	if row.Status != string(StatusSkipped) || !strings.Contains(row.Error, "after 2 retries") {
		t.Fatalf("got status %q error %q, want skipped after 2 retries", row.Status, row.Error)
	}
	var got []int
	for _, s := range enc.settings() {
		got = append(got, s.Quality)
	}
	if fmt.Sprint(got) != "[20 23 26]" {
		t.Fatalf("got qualities %v, want the default step of 3 applied twice", got)
	}
	if got := readFile(t, src); got != strings.Repeat("o", 100) {
		t.Fatal("original was replaced although every retry was larger")
	}
	assertNoTemp(t, src)
}

func TestEncodeOneRetryHigherCRFLowersABRBitrateWithAFloor(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	w := NewWorker(st)
	enc := sizedEncoder(t, 500)
	useEncoder(w, enc)
	pid := newProfile(t, st, store.ProfileRow{RateControl: "abr", VideoBitrate: 500, BloatPolicy: "retry_higher_crf", BloatRetryMax: 3})
	src := mediaFile(t, 100)
	j := claimedJob(t, st, store.JobRow{Title: "t", FilePath: src, FileSize: 100, ProfileID: sql.NullInt64{Int64: pid, Valid: true}})

	w.encodeOne(ctx, ctx, j)

	var got []int
	for _, s := range enc.settings() {
		got = append(got, s.VideoBitrate)
	}
	if fmt.Sprint(got) != "[500 300 200 200]" {
		t.Fatalf("got bitrates %v, want the default 200 kbps step floored at 200", got)
	}
	if got := jobStatus(t, st, j.ID); got != string(StatusSkipped) {
		t.Fatalf("got %q, want skipped", got)
	}
}

func TestEncodeOneRetryHigherCRFWithZeroRetriesKeepsOriginal(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	w := NewWorker(st)
	enc := sizedEncoder(t, 500)
	useEncoder(w, enc)
	pid := newProfile(t, st, store.ProfileRow{Quality: 20, BloatPolicy: "retry_higher_crf", BloatRetryMax: 0})
	src := mediaFile(t, 100)
	j := claimedJob(t, st, store.JobRow{Title: "t", FilePath: src, FileSize: 100, ProfileID: sql.NullInt64{Int64: pid, Valid: true}})

	w.encodeOne(ctx, ctx, j)

	if n := len(enc.settings()); n != 1 {
		t.Fatalf("got %d encodes, want one when no retries are allowed", n)
	}
	if got := jobStatus(t, st, j.ID); got != string(StatusSkipped) {
		t.Fatalf("got %q, want skipped", got)
	}
}

type arrRecorder struct {
	mu     sync.Mutex
	bodies []map[string]any
	keys   []string
	paths  []string
}

func arrServer(t *testing.T, status int) (*httptest.Server, *arrRecorder) {
	t.Helper()
	rec := &arrRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		rec.mu.Lock()
		rec.bodies = append(rec.bodies, body)
		rec.keys = append(rec.keys, r.Header.Get("X-Api-Key"))
		rec.paths = append(rec.paths, r.Method+" "+r.URL.Path)
		rec.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func (r *arrRecorder) snapshot() ([]map[string]any, []string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]map[string]any(nil), r.bodies...), append([]string(nil), r.keys...), append([]string(nil), r.paths...)
}

func newArrInstance(t *testing.T, st *store.Store, kind, url string) int64 {
	t.Helper()
	id, err := st.CreateArrInstance(context.Background(), store.ArrInstanceRow{Kind: kind, Name: kind, URL: url, APIKey: "test-key", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRefreshArrSendsTheRightCommand(t *testing.T) {
	cases := []struct {
		kind string
		want string
	}{
		{"sonarr", `{"name":"RefreshSeries","seriesId":77}`},
		{"radarr", `{"movieIds":[77],"name":"RefreshMovie"}`},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			st := newTestStore(t)
			ctx := context.Background()
			srv, rec := arrServer(t, http.StatusCreated)
			inst := newArrInstance(t, st, tc.kind, srv.URL)
			id := insertJob(t, st, store.JobRow{ArrKind: tc.kind, ArrInstanceID: inst, ArrParentID: 77, Title: "t", FilePath: "/m/a.mkv", Status: string(StatusDone)})
			if err := st.SetRefreshError(ctx, id, "previous failure"); err != nil {
				t.Fatal(err)
			}

			NewWorker(st).refreshArr(ctx, *reload(t, st, id))

			bodies, keys, paths := rec.snapshot()
			if len(bodies) != 1 {
				t.Fatalf("got %d requests, want one", len(bodies))
			}
			got, _ := json.Marshal(bodies[0])
			if string(got) != tc.want {
				t.Fatalf("got body %s, want %s", got, tc.want)
			}
			if keys[0] != "test-key" || paths[0] != "POST /api/v3/command" {
				t.Fatalf("got key %q path %q", keys[0], paths[0])
			}
			if row := reload(t, st, id); row.RefreshError != "" {
				t.Fatalf("got refresh error %q, want it cleared after a successful refresh", row.RefreshError)
			}
		})
	}
}

func TestRefreshArrRecordsAFailedRefresh(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	srv, _ := arrServer(t, http.StatusInternalServerError)
	inst := newArrInstance(t, st, "sonarr", srv.URL)
	id := insertJob(t, st, store.JobRow{ArrKind: "sonarr", ArrInstanceID: inst, ArrParentID: 5, Title: "t", FilePath: "/m/a.mkv", Status: string(StatusDone)})

	NewWorker(st).refreshArr(ctx, *reload(t, st, id))

	row := reload(t, st, id)
	if !strings.Contains(row.RefreshError, "status=500") {
		t.Fatalf("got refresh error %q, want the upstream status recorded", row.RefreshError)
	}
	if row.Status != string(StatusDone) {
		t.Fatalf("a failed refresh changed the job status to %q", row.Status)
	}
}

func TestRefreshArrSkipsWhenThereIsNothingToRefresh(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	srv, rec := arrServer(t, http.StatusCreated)
	inst := newArrInstance(t, st, "sonarr", srv.URL)
	w := NewWorker(st)

	noParent := insertJob(t, st, store.JobRow{ArrKind: "sonarr", ArrInstanceID: inst, Title: "t", FilePath: "/m/a.mkv", Status: string(StatusDone)})
	w.refreshArr(ctx, *reload(t, st, noParent))

	otherKind := insertJob(t, st, store.JobRow{ArrKind: "lidarr", ArrInstanceID: inst, ArrParentID: 3, Title: "t", FilePath: "/m/b.mkv", Status: string(StatusDone)})
	w.refreshArr(ctx, *reload(t, st, otherKind))

	missing := insertJob(t, st, store.JobRow{ArrKind: "sonarr", ArrInstanceID: inst + 100, ArrParentID: 3, Title: "t", FilePath: "/m/c.mkv", Status: string(StatusDone)})
	w.refreshArr(ctx, *reload(t, st, missing))

	if bodies, _, _ := rec.snapshot(); len(bodies) != 0 {
		t.Fatalf("got %d refresh calls, want none", len(bodies))
	}
	for _, id := range []int64{noParent, otherKind, missing} {
		if row := reload(t, st, id); row.RefreshError != "" {
			t.Fatalf("job %d got refresh error %q, want none", id, row.RefreshError)
		}
	}
}

func TestEncodeOneRefreshesTheArrAfterADoneEncode(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	srv, rec := arrServer(t, http.StatusCreated)
	inst := newArrInstance(t, st, "sonarr", srv.URL)
	w := NewWorker(st)
	useEncoder(w, sizedEncoder(t, 10))
	pid := newProfile(t, st, store.ProfileRow{})
	j := claimedJob(t, st, store.JobRow{ArrInstanceID: inst, ArrParentID: 12, Title: "t", FilePath: mediaFile(t, 100), FileSize: 100, ProfileID: sql.NullInt64{Int64: pid, Valid: true}})

	w.encodeOne(ctx, ctx, j)

	if bodies, _, _ := rec.snapshot(); len(bodies) != 1 || bodies[0]["seriesId"] != float64(12) {
		t.Fatalf("got %+v, want one RefreshSeries for series 12", bodies)
	}
}

func TestEncodeOneDoesNotRefreshWhenTheOriginalIsKept(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	srv, rec := arrServer(t, http.StatusCreated)
	inst := newArrInstance(t, st, "sonarr", srv.URL)
	w := NewWorker(st)
	useEncoder(w, sizedEncoder(t, 500))
	pid := newProfile(t, st, store.ProfileRow{BloatPolicy: "keep_original"})
	j := claimedJob(t, st, store.JobRow{ArrInstanceID: inst, ArrParentID: 12, Title: "t", FilePath: mediaFile(t, 100), FileSize: 100, ProfileID: sql.NullInt64{Int64: pid, Valid: true}})

	w.encodeOne(ctx, ctx, j)

	if bodies, _, _ := rec.snapshot(); len(bodies) != 0 {
		t.Fatalf("got %d refresh calls, want none when nothing changed on disk", len(bodies))
	}
}

func blockingEncoder() *fakeEncoder {
	return newFakeEncoder(func(c encodeCall) (handbrake.RunResult, error) {
		<-c.ctx.Done()
		return handbrake.RunResult{Log: "interrupted"}, c.ctx.Err()
	})
}

func TestPauseRequeuesTheInFlightEncode(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	w := NewWorker(st)
	enc := blockingEncoder()
	useEncoder(w, enc)
	pid := newProfile(t, st, store.ProfileRow{})
	id := insertJob(t, st, store.JobRow{Title: "t", FilePath: mediaFile(t, 100), FileSize: 100, ProfileID: sql.NullInt64{Int64: pid, Valid: true}, Status: string(StatusReady)})

	w.runEncodes(ctx)
	waitStarted(t, enc)
	if got := jobStatus(t, st, id); got != string(StatusEncoding) {
		t.Fatalf("got %q, want encoding", got)
	}

	n, err := w.SetPaused(ctx, true)
	if err != nil || n != 1 {
		t.Fatalf("got %d (err %v), want one in-flight encode cancelled", n, err)
	}
	waitIdle(t, w)

	row := reload(t, st, id)
	if row.Status != string(StatusReady) {
		t.Fatalf("got %q error %q, want the paused encode requeued as ready", row.Status, row.Error)
	}
	if row.Attempts != 0 {
		t.Fatalf("got %d attempts, want the pause not to count as an attempt", row.Attempts)
	}
	if row.StartedAt.Valid {
		t.Fatal("started_at should be cleared on requeue")
	}
}

func TestUserCancelAfterAPauseRaceIsNotRequeued(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	w := NewWorker(st)
	enc := newFakeEncoder(func(c encodeCall) (handbrake.RunResult, error) {
		<-c.ctx.Done()
		if c.n == 1 {
			return writeTemp(t, c, 10), nil
		}
		return handbrake.RunResult{Log: "interrupted"}, c.ctx.Err()
	})
	useEncoder(w, enc)
	pid := newProfile(t, st, store.ProfileRow{})
	id := insertJob(t, st, store.JobRow{Title: "t", FilePath: mediaFile(t, 100), FileSize: 100, ProfileID: sql.NullInt64{Int64: pid, Valid: true}, Status: string(StatusReady)})

	w.runEncodes(ctx)
	waitStarted(t, enc)
	if _, err := w.SetPaused(ctx, true); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, w)
	if got := jobStatus(t, st, id); got != string(StatusDone) {
		t.Fatalf("got %q, want the encode that finished as the pause landed to be done", got)
	}

	if err := st.RetryJob(ctx, id); err != nil {
		t.Fatal(err)
	}
	w.transition(ctx, id, string(StatusWaitingForSeed), "retry")
	if _, err := w.SetPaused(ctx, false); err != nil {
		t.Fatal(err)
	}
	w.runEncodes(ctx)
	waitStarted(t, enc)
	if !w.CancelEncoding(id) {
		t.Fatal("want the retried encode to be cancellable")
	}
	waitIdle(t, w)

	row := reload(t, st, id)
	if row.Status != string(StatusFailed) || row.Error != "cancelled" {
		t.Fatalf("a user cancel was treated as a pause from an earlier run: got status %q error %q, want failed as cancelled", row.Status, row.Error)
	}
}
