package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/xsaveopt/recodarr/internal/store"
)

func TestIsValidHHMM(t *testing.T) {
	for _, s := range []string{"00:00", "09:30", "23:59", "12:00"} {
		if !isValidHHMM(s) {
			t.Fatalf("isValidHHMM(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "9:30", "24:00", "23:60", "1230", "ab:cd", "23:59:59", "-1:00"} {
		if isValidHHMM(s) {
			t.Fatalf("isValidHHMM(%q) = true, want false", s)
		}
	}
}

func TestIsValidOutputSuffix(t *testing.T) {
	for _, s := range []string{"recodarr", "x", "re-encoded_v2", strings.Repeat("a", 32)} {
		if !isValidOutputSuffix(s) {
			t.Fatalf("isValidOutputSuffix(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "has space", "dot.ted", "slash/ed", "back\\slash", strings.Repeat("a", 33)} {
		if isValidOutputSuffix(s) {
			t.Fatalf("isValidOutputSuffix(%q) = true, want false", s)
		}
	}
}

func TestNormalizeBloatPolicyAndClamp(t *testing.T) {
	for _, s := range []string{"keep_original", "retry_higher_crf"} {
		if got := normalizeBloatPolicy(s); got != s {
			t.Fatalf("normalizeBloatPolicy(%q) = %q, want it preserved", s, got)
		}
	}
	for _, s := range []string{"", "off", "nonsense"} {
		if got := normalizeBloatPolicy(s); got != "off" {
			t.Fatalf("normalizeBloatPolicy(%q) = %q, want off", s, got)
		}
	}
	for _, tc := range []struct{ v, lo, hi, want int }{
		{5, 0, 10, 5}, {-1, 0, 10, 0}, {99, 0, 10, 10}, {0, 1, 20, 1},
	} {
		if got := clamp(tc.v, tc.lo, tc.hi); got != tc.want {
			t.Fatalf("clamp(%d, %d, %d) = %d, want %d", tc.v, tc.lo, tc.hi, got, tc.want)
		}
	}
}

func TestSplitNonEmpty(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a", []string{"a"}},
		{"a,b", []string{"a", "b"}},
		{"a,,b", []string{"a", "b"}},
		{" a , b ", []string{"a", "b"}},
		{",,,", nil},
	} {
		got := splitNonEmpty(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("splitNonEmpty(%q) = %v, want %v", tc.in, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("splitNonEmpty(%q) = %v, want %v", tc.in, got, tc.want)
			}
		}
	}
}

func TestGetSettingsHidesTheAgentToken(t *testing.T) {
	env := newTestEnv(t).login(t)
	if err := env.store.SetSetting(context.Background(), "agent_token", "s3cret"); err != nil {
		t.Fatalf("set agent token: %v", err)
	}
	w := env.do(t, http.MethodGet, "/api/settings/", nil)
	wantStatus(t, w, http.StatusOK)
	if strings.Contains(w.Body.String(), "s3cret") {
		t.Fatalf("the agent token leaked into %s", w.Body.String())
	}
	got := decodeJSON[map[string]string](t, w)
	if _, ok := got["agent_token"]; ok {
		t.Fatal("agent_token was returned to the client")
	}
	if got["hasAgentToken"] != "true" {
		t.Fatalf("got %v, want hasAgentToken=true", got["hasAgentToken"])
	}
}

func TestPutSettingsPersistsAndValidates(t *testing.T) {
	env := newTestEnv(t).login(t)

	w := env.do(t, http.MethodPut, "/api/settings/", map[string]string{
		"encoding_window_start": "01:00",
		"encoding_window_end":   "05:30",
		"log_app_level":         "debug",
	})
	wantStatus(t, w, http.StatusNoContent)
	if env.logs.level != slog.LevelDebug {
		t.Fatalf("got log level %v, want debug to be applied live", env.logs.level)
	}
	got := decodeJSON[map[string]string](t, env.do(t, http.MethodGet, "/api/settings/", nil))
	if got["encoding_window_start"] != "01:00" || got["encoding_window_end"] != "05:30" {
		t.Fatalf("the window did not persist: %v", got)
	}
	if got["log_app_level"] != "DEBUG" {
		t.Fatalf("got %q, want the level upper-cased", got["log_app_level"])
	}
}

func TestPutSettingsRejectsBadValues(t *testing.T) {
	env := newTestEnv(t).login(t)
	for name, body := range map[string]map[string]string{
		"bad window":        {"encoding_window_start": "25:00"},
		"bad level":         {"log_app_level": "LOUD"},
		"bad bool":          {"log_rotate_enabled": "yes"},
		"bad agent bool":    {"agent_enabled": "1"},
		"bad agent url":     {"agent_url": "ftp://box"},
		"negative log size": {"log_max_size_mb": "-1"},
		"zero log size":     {"log_max_size_mb": "0"},
		"bad parallel":      {"max_parallel_encodes": "0"},
		"bad reconcile":     {"reconcile_interval_seconds": "30"},
		"bad paused":        {"encoding_paused": "maybe"},
		"bad suffix":        {"output_suffix": "has space"},
	} {
		w := env.do(t, http.MethodPut, "/api/settings/", body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: got %d, want 400", name, w.Code)
		}
	}
}

func TestPutSettingsNormalizesTheAgentURLAndDropsABlankToken(t *testing.T) {
	env := newTestEnv(t).login(t)
	if err := env.store.SetSetting(context.Background(), "agent_token", "keepme"); err != nil {
		t.Fatalf("seed token: %v", err)
	}
	w := env.do(t, http.MethodPut, "/api/settings/", map[string]string{
		"agent_url":   "https://box.example/  ",
		"agent_token": "   ",
	})
	wantStatus(t, w, http.StatusNoContent)

	url, _, err := env.store.GetSetting(context.Background(), "agent_url")
	if err != nil {
		t.Fatalf("get agent_url: %v", err)
	}
	if url != "https://box.example" {
		t.Fatalf("got %q, want the trailing slash and spaces trimmed", url)
	}
	tok, _, err := env.store.GetSetting(context.Background(), "agent_token")
	if err != nil {
		t.Fatalf("get agent_token: %v", err)
	}
	if tok != "keepme" {
		t.Fatalf("got %q, want a blank token to leave the stored one alone", tok)
	}
}

func TestArrInstanceApiKeyIsWriteOnly(t *testing.T) {
	env := newTestEnv(t).login(t)

	w := env.do(t, http.MethodPost, "/api/arr-instances/", arrInstanceDTO{
		Kind: "sonarr", Name: "main", URL: "http://sonarr:8989", APIKey: "topsecret", Enabled: true,
	})
	wantStatus(t, w, http.StatusCreated)
	if strings.Contains(w.Body.String(), "topsecret") {
		t.Fatalf("the api key leaked into the create response: %s", w.Body.String())
	}
	created := decodeJSON[arrInstanceDTO](t, w)
	if !created.HasAPIKey {
		t.Fatal("got hasApiKey=false after storing a key")
	}

	list := env.do(t, http.MethodGet, "/api/arr-instances/", nil)
	wantStatus(t, list, http.StatusOK)
	if strings.Contains(list.Body.String(), "topsecret") {
		t.Fatalf("the api key leaked into the list response: %s", list.Body.String())
	}
}

func TestArrInstanceBlankKeyOnUpdateKeepsTheStoredOne(t *testing.T) {
	env := newTestEnv(t).login(t)
	created := decodeJSON[arrInstanceDTO](t, env.do(t, http.MethodPost, "/api/arr-instances/",
		arrInstanceDTO{Kind: "sonarr", Name: "main", URL: "http://sonarr:8989", APIKey: "topsecret", Enabled: true}))

	w := env.do(t, http.MethodPut, fmt.Sprintf("/api/arr-instances/%d", created.ID), arrInstanceDTO{
		Kind: "sonarr", Name: "renamed", URL: "http://sonarr:8989", APIKey: "", Enabled: true,
	})
	wantStatus(t, w, http.StatusOK)
	updated := decodeJSON[arrInstanceDTO](t, w)
	if updated.Name != "renamed" {
		t.Fatalf("got name %q, want renamed", updated.Name)
	}
	if !updated.HasAPIKey {
		t.Fatal("a blank key on update wiped the stored key")
	}

	row, err := env.store.GetArrInstance(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("get instance: %v", err)
	}
	if row.APIKey != "topsecret" {
		t.Fatalf("got stored key %q, want it preserved", row.APIKey)
	}
}

func TestCreateArrInstanceRejectsAnUnknownKind(t *testing.T) {
	env := newTestEnv(t).login(t)
	w := env.do(t, http.MethodPost, "/api/arr-instances/",
		arrInstanceDTO{Kind: "lidarr", Name: "x", URL: "http://x", Enabled: true})
	wantStatus(t, w, http.StatusBadRequest)
}

func TestArrInstanceIdMustBeNumeric(t *testing.T) {
	env := newTestEnv(t).login(t)
	wantStatus(t, env.do(t, http.MethodPut, "/api/arr-instances/abc",
		arrInstanceDTO{Kind: "sonarr"}), http.StatusBadRequest)
	wantStatus(t, env.do(t, http.MethodDelete, "/api/arr-instances/abc", nil), http.StatusBadRequest)
}

func TestDeleteArrInstanceIsASoftDelete(t *testing.T) {
	env := newTestEnv(t).login(t)
	created := decodeJSON[arrInstanceDTO](t, env.do(t, http.MethodPost, "/api/arr-instances/",
		arrInstanceDTO{Kind: "radarr", Name: "films", URL: "http://radarr:7878", APIKey: "k", Enabled: true}))

	wantStatus(t, env.do(t, http.MethodDelete, fmt.Sprintf("/api/arr-instances/%d", created.ID), nil),
		http.StatusNoContent)

	live := decodeJSON[[]arrInstanceDTO](t, env.do(t, http.MethodGet, "/api/arr-instances/", nil))
	if len(live) != 0 {
		t.Fatalf("got %d live instances, want none", len(live))
	}
	all := decodeJSON[[]arrInstanceDTO](t, env.do(t, http.MethodGet, "/api/arr-instances/?includeDeleted=true", nil))
	if len(all) != 1 || !all[0].Deleted {
		t.Fatalf("got %+v, want one row flagged deleted", all)
	}
}

func TestQbitPasswordIsWriteOnly(t *testing.T) {
	env := newTestEnv(t).login(t)
	w := env.do(t, http.MethodPost, "/api/qbit-instances/", qbitInstanceDTO{
		Name: "box", URL: "http://qbit:8080", Username: "admin", Password: "hunter2",
	})
	wantStatus(t, w, http.StatusOK)
	if strings.Contains(w.Body.String(), "hunter2") {
		t.Fatalf("the password leaked into the upsert response: %s", w.Body.String())
	}

	list := env.do(t, http.MethodGet, "/api/qbit-instances/", nil)
	wantStatus(t, list, http.StatusOK)
	if strings.Contains(list.Body.String(), "hunter2") {
		t.Fatalf("the password leaked into the list: %s", list.Body.String())
	}
	rows := decodeJSON[[]qbitInstanceDTO](t, list)
	if len(rows) != 1 || !rows[0].HasPassword {
		t.Fatalf("got %+v, want one row with hasPassword", rows)
	}

	created := decodeJSON[qbitInstanceDTO](t, w)
	blank := env.do(t, http.MethodPost, "/api/qbit-instances/", qbitInstanceDTO{
		ID: created.ID, Name: "box", URL: "http://qbit:8080", Username: "admin", Password: "",
	})
	wantStatus(t, blank, http.StatusOK)
	after := decodeJSON[[]qbitInstanceDTO](t, env.do(t, http.MethodGet, "/api/qbit-instances/", nil))
	if len(after) != 1 || !after[0].HasPassword {
		t.Fatalf("a blank password on update wiped the stored one: %+v", after)
	}
}

func TestProfilesRoundTripThroughTheApi(t *testing.T) {
	env := newTestEnv(t).login(t)

	w := env.do(t, http.MethodPost, "/api/profiles/", profileDTO{
		Name: "av1 qsv", Encoder: "qsv_av1", RateControl: "  CQ  ",
		Quality: 28, SkipCodecs: " AV1, HEVC ",
		AudioBitratesByChannels: map[string]int{"2": 128, "6": 0},
		BloatPolicy:             "nonsense", BloatRetryMax: 99, BloatRetryStep: 0, BloatMinSavingsPercent: 80,
	})
	wantStatus(t, w, http.StatusOK)
	created := decodeJSON[profileDTO](t, w)
	if created.ID == 0 {
		t.Fatal("the upsert returned no id")
	}

	rows := decodeJSON[[]profileDTO](t, env.do(t, http.MethodGet, "/api/profiles/", nil))
	var got *profileDTO
	for i := range rows {
		if rows[i].ID == created.ID {
			got = &rows[i]
		}
	}
	if got == nil {
		t.Fatalf("the new profile is missing from %+v", rows)
	}
	if got.RateControl != "cq" {
		t.Fatalf("got rate control %q, want it lower-cased and trimmed", got.RateControl)
	}
	if got.SkipCodecs != "av1, hevc" {
		t.Fatalf("got skip codecs %q, want them lower-cased and trimmed", got.SkipCodecs)
	}
	if got.BloatPolicy != "off" {
		t.Fatalf("got bloat policy %q, want an unknown policy normalized to off", got.BloatPolicy)
	}
	if got.BloatRetryMax != 10 || got.BloatRetryStep != 1 || got.BloatMinSavingsPercent != 50 {
		t.Fatalf("bloat knobs were not clamped: %+v", got)
	}
	if got.AudioBitratesByChannels["2"] != 128 {
		t.Fatalf("got bitrates %v, want the 2-channel entry kept", got.AudioBitratesByChannels)
	}
	if _, ok := got.AudioBitratesByChannels["6"]; ok {
		t.Fatalf("got bitrates %v, want the zero entry dropped", got.AudioBitratesByChannels)
	}
}

func TestDeleteProfileHidesItFromTheList(t *testing.T) {
	env := newTestEnv(t).login(t)
	created := decodeJSON[profileDTO](t, env.do(t, http.MethodPost, "/api/profiles/",
		profileDTO{Name: "throwaway", Encoder: "x264"}))
	before := decodeJSON[[]profileDTO](t, env.do(t, http.MethodGet, "/api/profiles/", nil))

	wantStatus(t, env.do(t, http.MethodDelete, fmt.Sprintf("/api/profiles/%d", created.ID), nil),
		http.StatusNoContent)

	after := decodeJSON[[]profileDTO](t, env.do(t, http.MethodGet, "/api/profiles/", nil))
	if len(after) >= len(before) {
		t.Fatalf("got %d profiles after the delete, want fewer than %d", len(after), len(before))
	}
	for _, p := range after {
		if p.ID == created.ID && !p.Deleted {
			t.Fatal("the deleted profile is still listed as live")
		}
	}
}

func TestTagMappingsCrud(t *testing.T) {
	env := newTestEnv(t).login(t)
	profile := decodeJSON[profileDTO](t, env.do(t, http.MethodPost, "/api/profiles/",
		profileDTO{Name: "mapped", Encoder: "x265"}))

	bad := env.do(t, http.MethodPost, "/api/tag-mappings/",
		tagMappingDTO{ArrKind: "lidarr", TagID: 1, ProfileID: profile.ID})
	wantStatus(t, bad, http.StatusBadRequest)

	for _, kind := range []string{"sonarr", "radarr", "both"} {
		w := env.do(t, http.MethodPost, "/api/tag-mappings/",
			tagMappingDTO{ArrKind: kind, TagID: 7, TagLabel: "recode", ProfileID: profile.ID})
		wantStatus(t, w, http.StatusCreated)
	}

	rows := decodeJSON[[]tagMappingDTO](t, env.do(t, http.MethodGet, "/api/tag-mappings/", nil))
	if len(rows) != 3 {
		t.Fatalf("got %d mappings, want 3", len(rows))
	}

	wantStatus(t, env.do(t, http.MethodDelete, fmt.Sprintf("/api/tag-mappings/%d", rows[0].ID), nil),
		http.StatusNoContent)
	after := decodeJSON[[]tagMappingDTO](t, env.do(t, http.MethodGet, "/api/tag-mappings/", nil))
	if len(after) != 2 {
		t.Fatalf("got %d mappings after the delete, want 2", len(after))
	}
	wantStatus(t, env.do(t, http.MethodDelete, "/api/tag-mappings/abc", nil), http.StatusBadRequest)
}

func TestStatsReflectsTheJobTable(t *testing.T) {
	env := newTestEnv(t).login(t)
	ctx := context.Background()
	for _, status := range []string{"waiting_for_seed", "waiting_for_seed", "ready", "done", "failed"} {
		if _, err := env.store.InsertJob(ctx, store.JobRow{
			ArrKind: "sonarr", Title: "t", FilePath: "/m/" + status + ".mkv", Status: status,
		}); err != nil {
			t.Fatalf("insert job: %v", err)
		}
	}
	got := decodeJSON[statsDTO](t, env.do(t, http.MethodGet, "/api/stats", nil))
	if got.WaitingForSeed != 2 || got.Ready != 1 || got.Done != 1 || got.Failed != 1 {
		t.Fatalf("got %+v, want the seeded counts", got)
	}
}

func TestListJobsPagesAndFilters(t *testing.T) {
	env := newTestEnv(t).login(t)
	ctx := context.Background()
	for i := range 5 {
		status := "done"
		if i%2 == 0 {
			status = "failed"
		}
		if _, err := env.store.InsertJob(ctx, store.JobRow{
			ArrKind: "sonarr", Title: fmt.Sprintf("Show %d", i),
			FilePath: fmt.Sprintf("/m/s%d.mkv", i), Status: status,
		}); err != nil {
			t.Fatalf("insert job: %v", err)
		}
	}

	all := decodeJSON[jobsPageDTO](t, env.do(t, http.MethodGet, "/api/jobs", nil))
	if all.Total != 5 || len(all.Jobs) != 5 {
		t.Fatalf("got total=%d len=%d, want 5", all.Total, len(all.Jobs))
	}
	if all.Limit != 50 {
		t.Fatalf("got limit %d, want the default 50", all.Limit)
	}

	page := decodeJSON[jobsPageDTO](t, env.do(t, http.MethodGet, "/api/jobs?limit=2&offset=2", nil))
	if page.Total != 5 || len(page.Jobs) != 2 || page.Offset != 2 {
		t.Fatalf("got %+v, want a 2-row window into 5", page)
	}

	failed := decodeJSON[jobsPageDTO](t, env.do(t, http.MethodGet, "/api/jobs?status=failed", nil))
	if failed.Total != 3 {
		t.Fatalf("got %d failed jobs, want 3", failed.Total)
	}
	for _, j := range failed.Jobs {
		if j.Status != "failed" {
			t.Fatalf("the status filter returned a %s job", j.Status)
		}
	}

	capped := decodeJSON[jobsPageDTO](t, env.do(t, http.MethodGet, "/api/jobs?limit=9999", nil))
	if capped.Limit != 500 {
		t.Fatalf("got limit %d, want it capped at 500", capped.Limit)
	}
}

func TestRetryAndDeleteJobEndpoints(t *testing.T) {
	env := newTestEnv(t).login(t)
	ctx := context.Background()
	id, err := env.store.InsertJob(ctx, store.JobRow{
		ArrKind: "sonarr", Title: "Show", FilePath: "/m/a.mkv", Status: "failed",
	})
	if err != nil {
		t.Fatalf("insert job: %v", err)
	}

	w := env.do(t, http.MethodPost, fmt.Sprintf("/api/jobs/%d/retry", id), nil)
	wantStatus(t, w, http.StatusOK)
	retried := decodeJSON[jobDTO](t, w)
	if retried.Status == "failed" {
		t.Fatal("the job is still failed after a retry")
	}
	if retried.Attempts != 0 {
		t.Fatalf("got %d attempts, want the counter reset", retried.Attempts)
	}

	wantStatus(t, env.do(t, http.MethodPost, "/api/jobs/abc/retry", nil), http.StatusBadRequest)
	wantStatus(t, env.do(t, http.MethodDelete, "/api/jobs/abc", nil), http.StatusBadRequest)
}

func TestRetryAllFailedReportsACount(t *testing.T) {
	env := newTestEnv(t).login(t)
	ctx := context.Background()
	for i := range 3 {
		if _, err := env.store.InsertJob(ctx, store.JobRow{
			ArrKind: "sonarr", Title: "t", FilePath: fmt.Sprintf("/m/f%d.mkv", i), Status: "failed",
		}); err != nil {
			t.Fatalf("insert job: %v", err)
		}
	}
	w := env.do(t, http.MethodPost, "/api/jobs/retry-failed", nil)
	wantStatus(t, w, http.StatusOK)
	got := decodeJSON[map[string]int64](t, w)
	if got["retried"] != 3 {
		t.Fatalf("got %v, want 3 retried", got)
	}
}

func TestCancelJobOnlyAppliesToEncodingJobs(t *testing.T) {
	env := newTestEnv(t).login(t)
	ctx := context.Background()

	wantStatus(t, env.do(t, http.MethodPost, "/api/jobs/999/cancel", nil), http.StatusNotFound)

	ready, err := env.store.InsertJob(ctx, store.JobRow{
		ArrKind: "sonarr", Title: "t", FilePath: "/m/ready.mkv", Status: "ready",
	})
	if err != nil {
		t.Fatalf("insert job: %v", err)
	}
	wantStatus(t, env.do(t, http.MethodPost, fmt.Sprintf("/api/jobs/%d/cancel", ready), nil),
		http.StatusConflict)

	encoding, err := env.store.InsertJob(ctx, store.JobRow{
		ArrKind: "sonarr", Title: "t", FilePath: "/m/enc.mkv", Status: "encoding",
	})
	if err != nil {
		t.Fatalf("insert job: %v", err)
	}
	env.worker.cancelOK = false
	wantStatus(t, env.do(t, http.MethodPost, fmt.Sprintf("/api/jobs/%d/cancel", encoding), nil),
		http.StatusConflict)

	env.worker.cancelOK = true
	w := env.do(t, http.MethodPost, fmt.Sprintf("/api/jobs/%d/cancel", encoding), nil)
	wantStatus(t, w, http.StatusOK)
	if got := decodeJSON[map[string]string](t, w); got["status"] != "cancelling" {
		t.Fatalf("got %v, want a cancelling acknowledgement", got)
	}
	if len(env.worker.cancelled) == 0 || env.worker.cancelled[len(env.worker.cancelled)-1] != encoding {
		t.Fatalf("the worker was asked to cancel %v, want %d", env.worker.cancelled, encoding)
	}
}

func TestDeleteTerminalJobsOnlyRemovesTerminalOnes(t *testing.T) {
	env := newTestEnv(t).login(t)
	ctx := context.Background()
	for _, status := range []string{"done", "failed", "ready", "encoding"} {
		if _, err := env.store.InsertJob(ctx, store.JobRow{
			ArrKind: "sonarr", Title: "t", FilePath: "/m/" + status + ".mkv", Status: status,
		}); err != nil {
			t.Fatalf("insert job: %v", err)
		}
	}
	wantStatus(t, env.do(t, http.MethodDelete, "/api/jobs?status=done,failed", nil), http.StatusOK)

	left := decodeJSON[jobsPageDTO](t, env.do(t, http.MethodGet, "/api/jobs", nil))
	for _, j := range left.Jobs {
		if j.Status == "done" || j.Status == "failed" {
			t.Fatalf("a %s job survived the purge", j.Status)
		}
	}
	if left.Total != 2 {
		t.Fatalf("got %d jobs left, want the two non-terminal ones", left.Total)
	}
}

func TestWorkerStatusAndPause(t *testing.T) {
	env := newTestEnv(t).login(t)
	env.worker.encodingIDs = []int64{42}

	got := decodeJSON[map[string]any](t, env.do(t, http.MethodGet, "/api/worker/status", nil))
	if got["isEncoding"] != true {
		t.Fatalf("got %v, want isEncoding true", got["isEncoding"])
	}
	if got["encodingJobId"].(float64) != 42 {
		t.Fatalf("got %v, want job 42", got["encodingJobId"])
	}
	if got["lastTickAt"] != nil {
		t.Fatalf("got %v, want a null tick before the worker has run", got["lastTickAt"])
	}

	w := env.do(t, http.MethodPost, "/api/worker/pause", map[string]bool{"paused": true})
	wantStatus(t, w, http.StatusOK)
	if !env.worker.paused {
		t.Fatal("the pause request did not reach the worker")
	}
	res := decodeJSON[map[string]any](t, w)
	if res["paused"] != true {
		t.Fatalf("got %v, want paused true", res["paused"])
	}
}

func TestUnknownAdminRouteIs404(t *testing.T) {
	env := newTestEnv(t).login(t)
	w := env.do(t, http.MethodGet, "/api/nope", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", w.Code)
	}
}
