package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/xsaveopt/recodarr/internal/agent"
	"github.com/xsaveopt/recodarr/internal/handbrake"
	"github.com/xsaveopt/recodarr/internal/health"
	"github.com/xsaveopt/recodarr/internal/store"
)

func addArr(t *testing.T, env *testEnv, kind, name, url, key string, enabled bool) int64 {
	t.Helper()
	id, err := env.store.CreateArrInstance(context.Background(), store.ArrInstanceRow{
		Kind: kind, Name: name, URL: url, APIKey: key, Enabled: enabled,
	})
	if err != nil {
		t.Fatalf("create arr instance: %v", err)
	}
	return id
}

func addQbit(t *testing.T, env *testEnv, url, password string) int64 {
	t.Helper()
	id, err := env.store.UpsertQbitInstance(context.Background(), store.QbitInstanceRow{
		Name: "box", URL: url, Username: "admin", Password: password,
	})
	if err != nil {
		t.Fatalf("create qbit instance: %v", err)
	}
	return id
}

func addProfile(t *testing.T, env *testEnv, name string) int64 {
	t.Helper()
	id, err := env.store.UpsertProfile(context.Background(), store.ProfileRow{Name: name, Encoder: "x265"})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	return id
}

func mapRecodeTag(t *testing.T, env *testEnv, profileID int64) {
	t.Helper()
	if _, err := env.store.CreateTagMapping(context.Background(), store.TagMappingRow{
		ArrKind: "sonarr", TagID: 1, TagLabel: "recode", ProfileID: profileID,
	}); err != nil {
		t.Fatalf("create mapping: %v", err)
	}
}

func addJob(t *testing.T, env *testEnv, r store.JobRow) int64 {
	t.Helper()
	if r.ArrKind == "" {
		r.ArrKind = "sonarr"
	}
	if r.Title == "" {
		r.Title = "t"
	}
	if r.FilePath == "" {
		r.FilePath = fmt.Sprintf("/m/%s-%d.mkv", r.Status, len(r.DownloadID)+int(r.FileSize))
	}
	id, err := env.store.InsertJob(context.Background(), r)
	if err != nil {
		t.Fatalf("insert job: %v", err)
	}
	return id
}

func TestHandbrakeCapsMatchesTheProbe(t *testing.T) {
	env := newTestEnv(t).login(t)
	w := env.do(t, http.MethodGet, "/api/handbrake/caps", nil)
	wantStatus(t, w, http.StatusOK)
	got := decodeJSON[handbrake.Caps](t, w)
	if len(got.Encoders) != len(handbrake.QueryCaps().Encoders) {
		t.Fatalf("got %+v, want the cached caps", got)
	}
}

func TestDebugReportsThePlatform(t *testing.T) {
	env := newTestEnv(t).login(t)
	w := env.do(t, http.MethodGet, "/api/debug", nil)
	wantStatus(t, w, http.StatusOK)
	got := decodeJSON[debugInfo](t, w)
	if got.Platform != runtime.GOOS || got.Arch != runtime.GOARCH {
		t.Fatalf("got %+v, want %s/%s", got, runtime.GOOS, runtime.GOARCH)
	}
	if got.HBVersion == "" {
		t.Fatal("the handbrake version is empty")
	}
	if got.HBFound == strings.HasPrefix(got.HBVersion, "(HandBrakeCLI not found)") {
		t.Fatalf("hbFound=%v disagrees with version %q", got.HBFound, got.HBVersion)
	}
}

func TestStatusReturnsTheHealthSnapshot(t *testing.T) {
	env := newTestEnv(t).login(t)
	w := env.do(t, http.MethodGet, "/api/status", nil)
	wantStatus(t, w, http.StatusOK)
	got := decodeJSON[health.Snapshot](t, w)
	if got.OK {
		t.Fatal("a fresh install with no mappings reported healthy")
	}
	found := false
	for _, iss := range got.Issues {
		if iss.Source == "config" {
			found = true
		}
	}
	if !found {
		t.Fatalf("got %+v, want the missing-mappings warning", got.Issues)
	}
}

func TestTestArrInstance(t *testing.T) {
	env := newTestEnv(t).login(t)
	fake := &fakeArr{apiKey: "good"}
	srv := fake.start(t)

	good := addArr(t, env, "sonarr", "ok", srv.URL, "good", true)
	bad := addArr(t, env, "radarr", "badkey", srv.URL, "wrong", true)

	w := env.do(t, http.MethodPost, fmt.Sprintf("/api/arr-instances/%d/test", good), nil)
	wantStatus(t, w, http.StatusOK)
	if got := decodeJSON[map[string]any](t, w); got["ok"] != true {
		t.Fatalf("got %v, want ok", got)
	}

	w = env.do(t, http.MethodPost, fmt.Sprintf("/api/arr-instances/%d/test", bad), nil)
	wantStatus(t, w, http.StatusOK)
	got := decodeJSON[map[string]any](t, w)
	if got["ok"] != false || !strings.Contains(fmt.Sprint(got["error"]), "401") {
		t.Fatalf("got %v, want a 401 failure", got)
	}

	wantStatus(t, env.do(t, http.MethodPost, "/api/arr-instances/999/test", nil), http.StatusNotFound)
	wantStatus(t, env.do(t, http.MethodPost, "/api/arr-instances/abc/test", nil), http.StatusBadRequest)
}

func TestListArrTags(t *testing.T) {
	env := newTestEnv(t).login(t)
	fake := &fakeArr{apiKey: "k", tags: []fakeArrTag{{1, "recode"}, {2, "keep"}}}
	srv := fake.start(t)
	id := addArr(t, env, "sonarr", "tv", srv.URL, "k", true)

	w := env.do(t, http.MethodGet, fmt.Sprintf("/api/arr-instances/%d/tags", id), nil)
	wantStatus(t, w, http.StatusOK)
	got := decodeJSON[[]tagDTO](t, w)
	if len(got) != 2 || got[0].Label != "recode" || got[1].ID != 2 {
		t.Fatalf("got %+v, want both tags", got)
	}

	fake.failTags = true
	wantStatus(t, env.do(t, http.MethodGet, fmt.Sprintf("/api/arr-instances/%d/tags", id), nil), http.StatusBadGateway)
	wantStatus(t, env.do(t, http.MethodGet, "/api/arr-instances/999/tags", nil), http.StatusNotFound)
	wantStatus(t, env.do(t, http.MethodGet, "/api/arr-instances/abc/tags", nil), http.StatusBadRequest)
}

func TestListAllArrTagsSkipsBrokenInstances(t *testing.T) {
	env := newTestEnv(t).login(t)
	tv := (&fakeArr{apiKey: "k", tags: []fakeArrTag{{1, "recode"}}}).start(t)
	films := (&fakeArr{apiKey: "k", tags: []fakeArrTag{{5, "films"}}}).start(t)
	broken := (&fakeArr{apiKey: "k", failTags: true}).start(t)

	tvID := addArr(t, env, "sonarr", "tv", tv.URL, "k", true)
	filmsID := addArr(t, env, "radarr", "films", films.URL, "k", false)
	addArr(t, env, "sonarr", "broken", broken.URL, "k", true)

	w := env.do(t, http.MethodGet, "/api/arr-instances/all-tags", nil)
	wantStatus(t, w, http.StatusOK)
	got := decodeJSON[[]instanceTagDTO](t, w)
	if len(got) != 2 {
		t.Fatalf("got %+v, want one tag from each reachable instance", got)
	}
	seen := map[int64]string{}
	for _, tg := range got {
		seen[tg.InstanceID] = tg.TagLabel
	}
	if seen[tvID] != "recode" || seen[filmsID] != "films" {
		t.Fatalf("got %+v", got)
	}
}

func TestListUnmappedTags(t *testing.T) {
	env := newTestEnv(t).login(t)
	fake := &fakeArr{
		apiKey: "k",
		tags:   []fakeArrTag{{1, "recode"}, {2, "anime"}, {3, "4k"}},
		series: []map[string]any{
			{"id": 10, "title": "A", "tags": []int{1, 2}, "statistics": map[string]any{"episodeFileCount": 3}},
			{"id": 11, "title": "B", "tags": []int{2, 3}, "statistics": map[string]any{"episodeFileCount": 1}},
			{"id": 12, "title": "Empty", "tags": []int{3}, "statistics": map[string]any{"episodeFileCount": 0}},
		},
	}
	srv := fake.start(t)
	id := addArr(t, env, "sonarr", "tv", srv.URL, "k", true)
	addArr(t, env, "sonarr", "off", srv.URL, "k", false)
	mapRecodeTag(t, env, addProfile(t, env, "p"))

	w := env.do(t, http.MethodGet, "/api/arr-instances/unmapped-tags", nil)
	wantStatus(t, w, http.StatusOK)
	got := decodeJSON[[]unmappedTagDTO](t, w)
	if len(got) != 2 {
		t.Fatalf("got %+v, want the two unmapped tags from the enabled instance", got)
	}
	counts := map[string]int{}
	for _, u := range got {
		if u.InstanceID != id {
			t.Fatalf("got a tag from instance %d", u.InstanceID)
		}
		counts[u.TagLabel] = u.ItemCount
	}
	if counts["anime"] != 2 || counts["4k"] != 1 {
		t.Fatalf("got counts %v, want anime=2 4k=1", counts)
	}
	if got[0].TagLabel != "4k" {
		t.Fatalf("got %+v, want the result sorted by label", got)
	}
}

func TestQbitCredentialTest(t *testing.T) {
	env := newTestEnv(t).login(t)
	srv := (&fakeQbit{password: "hunter2"}).start(t)

	w := env.do(t, http.MethodPost, "/api/qbit-instances/test",
		map[string]string{"url": srv.URL, "username": "admin", "password": "hunter2"})
	wantStatus(t, w, http.StatusOK)
	if got := decodeJSON[map[string]any](t, w); got["ok"] != true {
		t.Fatalf("got %v, want ok", got)
	}

	w = env.do(t, http.MethodPost, "/api/qbit-instances/test",
		map[string]string{"url": srv.URL, "username": "admin", "password": "nope"})
	if got := decodeJSON[map[string]any](t, w); got["ok"] != false || got["error"] == "" {
		t.Fatalf("got %v, want a rejected login", got)
	}
}

func TestQbitInstanceTestAndDelete(t *testing.T) {
	env := newTestEnv(t).login(t)
	srv := (&fakeQbit{password: "hunter2"}).start(t)
	good := addQbit(t, env, srv.URL, "hunter2")

	w := env.do(t, http.MethodPost, fmt.Sprintf("/api/qbit-instances/%d/test", good), nil)
	wantStatus(t, w, http.StatusOK)
	if got := decodeJSON[map[string]any](t, w); got["ok"] != true {
		t.Fatalf("got %v, want ok", got)
	}

	if _, err := env.store.UpsertQbitInstance(context.Background(), store.QbitInstanceRow{
		ID: good, Name: "box", URL: srv.URL, Username: "admin", Password: "stale",
	}); err != nil {
		t.Fatalf("update qbit: %v", err)
	}
	w = env.do(t, http.MethodPost, fmt.Sprintf("/api/qbit-instances/%d/test", good), nil)
	if got := decodeJSON[map[string]any](t, w); got["ok"] != false {
		t.Fatalf("got %v, want the stored bad password rejected", got)
	}

	wantStatus(t, env.do(t, http.MethodPost, "/api/qbit-instances/999/test", nil), http.StatusNotFound)
	wantStatus(t, env.do(t, http.MethodPost, "/api/qbit-instances/abc/test", nil), http.StatusBadRequest)

	wantStatus(t, env.do(t, http.MethodDelete, fmt.Sprintf("/api/qbit-instances/%d", good), nil), http.StatusNoContent)
	rows := decodeJSON[[]qbitInstanceDTO](t, env.do(t, http.MethodGet, "/api/qbit-instances/", nil))
	if len(rows) != 0 {
		t.Fatalf("got %+v, want the instance gone", rows)
	}
	wantStatus(t, env.do(t, http.MethodDelete, "/api/qbit-instances/abc", nil), http.StatusBadRequest)
}

func startAgent(t *testing.T, token string) *httptest.Server {
	t.Helper()
	st, err := agent.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open agent store: %v", err)
	}
	srv := httptest.NewServer(agent.NewServer(st, agent.NewRunner(st, 2, nil), token, true, nil).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func TestAgentTestWithExplicitCredentials(t *testing.T) {
	env := newTestEnv(t).login(t)
	srv := startAgent(t, "tok")

	w := env.do(t, http.MethodPost, "/api/agent/test", map[string]string{"url": " " + srv.URL + " ", "token": "tok"})
	wantStatus(t, w, http.StatusOK)
	got := decodeJSON[map[string]any](t, w)
	if got["ok"] != true || got["slots"] != float64(2) || got["localFs"] != true {
		t.Fatalf("got %v, want the agent's health", got)
	}

	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	w = env.do(t, http.MethodPost, "/api/agent/test", map[string]string{"url": down.URL, "token": "tok"})
	if got := decodeJSON[map[string]any](t, w); got["ok"] != false || got["error"] == "" {
		t.Fatalf("got %v, want an unreachable agent reported", got)
	}
}

func TestAgentTestFallsBackToStoredSettings(t *testing.T) {
	env := newTestEnv(t).login(t)
	ctx := context.Background()

	w := env.do(t, http.MethodPost, "/api/agent/test", nil)
	if got := decodeJSON[map[string]any](t, w); got["ok"] != false || !strings.Contains(fmt.Sprint(got["error"]), "URL") {
		t.Fatalf("got %v, want a missing url error", got)
	}

	srv := startAgent(t, "stored")
	if err := env.store.SetSetting(ctx, "agent_url", srv.URL); err != nil {
		t.Fatalf("set url: %v", err)
	}
	w = env.do(t, http.MethodPost, "/api/agent/test", map[string]string{})
	if got := decodeJSON[map[string]any](t, w); got["ok"] != false || !strings.Contains(fmt.Sprint(got["error"]), "token") {
		t.Fatalf("got %v, want a missing token error", got)
	}

	if err := env.store.SetSetting(ctx, "agent_token", "stored"); err != nil {
		t.Fatalf("set token: %v", err)
	}
	w = env.do(t, http.MethodPost, "/api/agent/test", map[string]string{})
	if got := decodeJSON[map[string]any](t, w); got["ok"] != true {
		t.Fatalf("got %v, want the stored settings used", got)
	}
}

func TestDebugJobRejectsBadIDs(t *testing.T) {
	env := newTestEnv(t).login(t)
	wantStatus(t, env.do(t, http.MethodGet, "/api/jobs/abc/debug", nil), http.StatusBadRequest)
	wantStatus(t, env.do(t, http.MethodGet, "/api/jobs/999/debug", nil), http.StatusNotFound)
}

func TestDebugJobWithoutQbit(t *testing.T) {
	env := newTestEnv(t).login(t)
	id := addJob(t, env, store.JobRow{Status: "waiting_for_seed", DownloadID: "abc"})

	got := decodeJSON[jobDebugDTO](t, env.do(t, http.MethodGet, fmt.Sprintf("/api/jobs/%d/debug", id), nil))
	if got.JobID != id || got.DownloadIDLength != 3 || got.WaitingForSeed != 1 {
		t.Fatalf("got %+v", got)
	}
	if got.Qbit.Configured || !strings.Contains(got.StalledReason, "not configured") {
		t.Fatalf("got %+v, want the missing qbit called out", got)
	}
	if got.Encode != nil {
		t.Fatalf("got encode details %+v for a job that never ran", got.Encode)
	}
}

func TestDebugJobReportsEncodeDetails(t *testing.T) {
	env := newTestEnv(t).login(t)
	ctx := context.Background()
	pid := addProfile(t, env, "debugged")
	id := addJob(t, env, store.JobRow{
		Status: "ready", FileSize: 1000, ProfileID: sql.NullInt64{Int64: pid, Valid: true},
	})
	if ok, err := env.store.MarkJobEncoding(ctx, id); err != nil || !ok {
		t.Fatalf("mark encoding: %v %v", ok, err)
	}
	if err := env.store.MarkJobDone(ctx, id, 250); err != nil {
		t.Fatalf("mark done: %v", err)
	}

	got := decodeJSON[jobDebugDTO](t, env.do(t, http.MethodGet, fmt.Sprintf("/api/jobs/%d/debug", id), nil))
	enc := got.Encode
	if enc == nil {
		t.Fatal("got no encode details for a finished job")
	}
	if enc.ProfileName != "debugged" || enc.ProfileEncoder != "x265" {
		t.Fatalf("got %+v, want the profile resolved", enc)
	}
	if enc.OriginalBytes == nil || *enc.OriginalBytes != 1000 || enc.FinalBytes == nil || *enc.FinalBytes != 250 {
		t.Fatalf("got %+v, want the sizes", enc)
	}
	if enc.SavedBytes == nil || *enc.SavedBytes != 750 || enc.SavedPercent == nil || *enc.SavedPercent != 75 {
		t.Fatalf("got %+v, want 750 bytes / 75%% saved", enc)
	}
	if enc.StartedAt == "" || enc.FinishedAt == "" || enc.DurationSeconds == nil {
		t.Fatalf("got %+v, want the timing filled in", enc)
	}
}

func TestDebugJobExplainsAHardlinkWait(t *testing.T) {
	env := newTestEnv(t).login(t)
	dir := t.TempDir()
	single := filepath.Join(dir, "single.mkv")
	if err := os.WriteFile(single, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	linked := filepath.Join(dir, "linked.mkv")
	if err := os.WriteFile(linked, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Link(linked, filepath.Join(dir, "download.mkv")); err != nil {
		t.Fatalf("link: %v", err)
	}

	for path, want := range map[string]string{
		single:                         "single hardlink",
		linked:                         "2 hardlinks",
		filepath.Join(dir, "gone.mkv"): "Can't stat",
	} {
		id := addJob(t, env, store.JobRow{Status: "waiting_for_hardlink", FilePath: path})
		got := decodeJSON[jobDebugDTO](t, env.do(t, http.MethodGet, fmt.Sprintf("/api/jobs/%d/debug", id), nil))
		if !strings.Contains(got.StalledReason, want) {
			t.Fatalf("%s: got %q, want it to mention %q", filepath.Base(path), got.StalledReason, want)
		}
	}
}

func TestDebugJobLooksTheTorrentUpInQbit(t *testing.T) {
	env := newTestEnv(t).login(t)
	fake := &fakeQbit{password: "pw", torrents: []map[string]any{
		{"hash": "aaaa", "name": "Show.S01", "state": "uploading", "category": "tv", "progress": 1},
	}}
	srv := fake.start(t)
	addQbit(t, env, srv.URL, "pw")

	held := addJob(t, env, store.JobRow{Status: "waiting_for_seed", DownloadID: "AAAA"})
	got := decodeJSON[jobDebugDTO](t, env.do(t, http.MethodGet, fmt.Sprintf("/api/jobs/%d/debug", held), nil))
	if !got.Qbit.Configured || !got.Qbit.Reachable || got.Qbit.URL != srv.URL {
		t.Fatalf("got %+v, want a reachable qbit", got.Qbit)
	}
	if got.Qbit.Lookup == nil || !got.Qbit.Lookup.Found || got.Qbit.Lookup.Name != "Show.S01" {
		t.Fatalf("got %+v, want the torrent found", got.Qbit.Lookup)
	}
	if !strings.Contains(got.StalledReason, "still holds this torrent") {
		t.Fatalf("got %q", got.StalledReason)
	}

	gone := addJob(t, env, store.JobRow{Status: "waiting_for_seed", DownloadID: "bbbb"})
	got = decodeJSON[jobDebugDTO](t, env.do(t, http.MethodGet, fmt.Sprintf("/api/jobs/%d/debug", gone), nil))
	if got.Qbit.Lookup == nil || got.Qbit.Lookup.Found {
		t.Fatalf("got %+v, want a not-found lookup", got.Qbit.Lookup)
	}
	if !strings.Contains(got.StalledReason, "does not have this hash") {
		t.Fatalf("got %q", got.StalledReason)
	}

	noHash := addJob(t, env, store.JobRow{Status: "waiting_for_seed", FileSize: 1})
	got = decodeJSON[jobDebugDTO](t, env.do(t, http.MethodGet, fmt.Sprintf("/api/jobs/%d/debug", noHash), nil))
	if !strings.Contains(got.StalledReason, "no downloadId") {
		t.Fatalf("got %q", got.StalledReason)
	}

	fake.infoCode = http.StatusInternalServerError
	got = decodeJSON[jobDebugDTO](t, env.do(t, http.MethodGet, fmt.Sprintf("/api/jobs/%d/debug", held), nil))
	if got.Qbit.LookupError == "" || !strings.Contains(got.StalledReason, "lookup failed") {
		t.Fatalf("got %+v, want the lookup failure surfaced", got)
	}
}

func TestDebugJobReportsAQbitLoginFailure(t *testing.T) {
	env := newTestEnv(t).login(t)
	srv := (&fakeQbit{password: "right"}).start(t)
	addQbit(t, env, srv.URL, "wrong")
	id := addJob(t, env, store.JobRow{Status: "waiting_for_seed", DownloadID: "cccc"})

	got := decodeJSON[jobDebugDTO](t, env.do(t, http.MethodGet, fmt.Sprintf("/api/jobs/%d/debug", id), nil))
	if !got.Qbit.Configured || got.Qbit.Reachable || got.Qbit.LoginError == "" {
		t.Fatalf("got %+v, want the login error", got.Qbit)
	}
}

func jobStatus(t *testing.T, env *testEnv, id int64) (store.JobRow, bool) {
	t.Helper()
	row, err := env.store.GetJob(context.Background(), id)
	if err != nil {
		return store.JobRow{}, false
	}
	return *row, true
}

func TestBulkDeleteSkipsEncodingJobs(t *testing.T) {
	env := newTestEnv(t).login(t)
	done := addJob(t, env, store.JobRow{Status: "done", FileSize: 1})
	failed := addJob(t, env, store.JobRow{Status: "failed", FileSize: 2})
	encoding := addJob(t, env, store.JobRow{Status: "encoding", FileSize: 3})
	untouched := addJob(t, env, store.JobRow{Status: "ready", FileSize: 4})

	w := env.do(t, http.MethodPost, "/api/jobs/bulk-delete", map[string][]int64{"ids": {done, failed, encoding}})
	wantStatus(t, w, http.StatusOK)
	if got := decodeJSON[map[string]int64](t, w); got["deleted"] != 2 {
		t.Fatalf("got %v, want 2 deleted", got)
	}
	for _, id := range []int64{done, failed} {
		if _, ok := jobStatus(t, env, id); ok {
			t.Fatalf("job %d survived the bulk delete", id)
		}
	}
	for _, id := range []int64{encoding, untouched} {
		if _, ok := jobStatus(t, env, id); !ok {
			t.Fatalf("job %d was deleted", id)
		}
	}

	w = env.do(t, http.MethodPost, "/api/jobs/bulk-delete", map[string][]int64{"ids": {}})
	if got := decodeJSON[map[string]int64](t, w); got["deleted"] != 0 {
		t.Fatalf("got %v, want nothing deleted", got)
	}
	wantStatus(t, env.do(t, http.MethodPost, "/api/jobs/bulk-delete", "nope"), http.StatusBadRequest)
}

func TestBulkRetryOnlyTouchesFinishedJobs(t *testing.T) {
	env := newTestEnv(t).login(t)
	failed := addJob(t, env, store.JobRow{Status: "failed", FileSize: 1})
	done := addJob(t, env, store.JobRow{Status: "done", FileSize: 2})
	ready := addJob(t, env, store.JobRow{Status: "ready", FileSize: 3})

	w := env.do(t, http.MethodPost, "/api/jobs/bulk-retry", map[string][]int64{"ids": {failed, done, ready}})
	wantStatus(t, w, http.StatusOK)
	if got := decodeJSON[map[string]int64](t, w); got["retried"] != 2 {
		t.Fatalf("got %v, want 2 retried", got)
	}
	for _, id := range []int64{failed, done} {
		if row, _ := jobStatus(t, env, id); row.Status != "waiting_for_seed" {
			t.Fatalf("job %d is %s, want waiting_for_seed", id, row.Status)
		}
	}
	if row, _ := jobStatus(t, env, ready); row.Status != "ready" {
		t.Fatalf("the ready job moved to %s", row.Status)
	}
	wantStatus(t, env.do(t, http.MethodPost, "/api/jobs/bulk-retry", "nope"), http.StatusBadRequest)
}

func TestBulkSetProfile(t *testing.T) {
	env := newTestEnv(t).login(t)
	pid := addProfile(t, env, "bulk")
	ready := addJob(t, env, store.JobRow{Status: "ready", FileSize: 1})
	encoding := addJob(t, env, store.JobRow{Status: "encoding", FileSize: 2})

	w := env.do(t, http.MethodPost, "/api/jobs/bulk-set-profile",
		map[string]any{"ids": []int64{ready, encoding}, "profileId": pid})
	wantStatus(t, w, http.StatusOK)
	if got := decodeJSON[map[string]int64](t, w); got["updated"] != 1 {
		t.Fatalf("got %v, want 1 updated", got)
	}
	if row, _ := jobStatus(t, env, ready); !row.ProfileID.Valid || row.ProfileID.Int64 != pid {
		t.Fatalf("got profile %+v, want %d", row.ProfileID, pid)
	}
	if row, _ := jobStatus(t, env, encoding); row.ProfileID.Valid {
		t.Fatal("an encoding job had its profile swapped")
	}

	w = env.do(t, http.MethodPost, "/api/jobs/bulk-set-profile",
		map[string]any{"ids": []int64{ready}, "profileId": 0})
	wantStatus(t, w, http.StatusOK)
	if row, _ := jobStatus(t, env, ready); row.ProfileID.Valid {
		t.Fatal("profileId 0 did not clear the profile")
	}
	wantStatus(t, env.do(t, http.MethodPost, "/api/jobs/bulk-set-profile", "nope"), http.StatusBadRequest)
}
