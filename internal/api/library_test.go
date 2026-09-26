package api

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

func mediaInfo(codec, res, runTime string) map[string]any {
	return map[string]any{"videoCodec": codec, "resolution": res, "runTime": runTime}
}

func libraryFixture() *fakeArr {
	return &fakeArr{
		apiKey: "k",
		tags:   []fakeArrTag{{1, "recode"}, {2, "anime"}},
		series: []map[string]any{
			{
				"id": 10, "title": "Big", "year": 2020, "path": "/tv/Big", "tags": []int{1, 99}, "runtime": 60,
				"statistics": map[string]any{"episodeFileCount": 2, "sizeOnDisk": 2_000_000_000},
			},
			{
				"id": 11, "title": "Small", "year": 2021, "path": "/tv/Small", "tags": []int{2}, "runtime": 30,
				"statistics": map[string]any{"episodeFileCount": 1, "sizeOnDisk": 100_000_000},
			},
		},
		files: map[string][]map[string]any{
			"10": {
				{"id": 100, "seriesId": 10, "path": "/tv/Big/e1.mkv", "relativePath": "e1.mkv", "size": 900_000_000,
					"mediaInfo": mediaInfo("h264", "1920x1080", "01:00:00"), "quality": map[string]any{"quality": map[string]any{"name": "HDTV-1080p"}}},
				{"id": 101, "seriesId": 10, "path": "/tv/Big/e2.mkv", "relativePath": "e2.mkv", "size": 1_100_000_000,
					"mediaInfo": mediaInfo("h264", "1920x1080", "01:00:00")},
				{"id": 102, "seriesId": 10, "path": "", "size": 5},
			},
			"11": {
				{"id": 110, "seriesId": 11, "path": "/tv/Small/e1.mkv", "size": 100_000_000,
					"mediaInfo": mediaInfo("hevc", "1280x720", "")},
			},
		},
	}
}

func TestLibraryScanRejectsAnUnknownKind(t *testing.T) {
	env := newTestEnv(t).login(t)
	wantStatus(t, env.do(t, http.MethodGet, "/api/library/lidarr", nil), http.StatusBadRequest)
}

func TestLibraryScanBuildsItemsAndMappableTags(t *testing.T) {
	env := newTestEnv(t).login(t)
	srv := libraryFixture().start(t)
	id := addArr(t, env, "sonarr", "tv", srv.URL, "k", true)
	addArr(t, env, "radarr", "films", srv.URL, "k", true)
	addArr(t, env, "sonarr", "off", srv.URL, "k", false)
	mapRecodeTag(t, env, addProfile(t, env, "lib"))

	w := env.do(t, http.MethodGet, "/api/library/sonarr", nil)
	wantStatus(t, w, http.StatusOK)
	got := decodeJSON[libraryScanDTO](t, w)

	if got.Kind != "sonarr" || got.Deep {
		t.Fatalf("got kind=%s deep=%v", got.Kind, got.Deep)
	}
	if len(got.Instances) != 1 || got.Instances[0].ID != id || got.Instances[0].TagCount != 2 {
		t.Fatalf("got instances %+v, want only the enabled sonarr instance", got.Instances)
	}
	mt := got.Instances[0].MappableTags
	if len(mt) != 1 || mt[0].TagLabel != "recode" || mt[0].ProfileName != "lib" {
		t.Fatalf("got mappable tags %+v", mt)
	}
	if len(got.Items) != 2 {
		t.Fatalf("got %d items, want 2", len(got.Items))
	}
	big := got.Items[0]
	if big.Title != "Big" {
		t.Fatalf("got %+v first, want items sorted by bitrate", big)
	}
	if !big.Mapped || !slices.Equal(big.MappedTags, []string{"recode"}) {
		t.Fatalf("got %+v, want Big mapped via recode", big)
	}
	if !slices.Equal(big.TagLabels, []string{"99", "recode"}) {
		t.Fatalf("got labels %v, want unknown ids shown as numbers", big.TagLabels)
	}
	if big.RuntimeSeconds != 2*60*60 || big.BitrateBps != 2_000_000_000*8/7200 || big.BitrateExact {
		t.Fatalf("got %+v, want an estimated bitrate from the series stats", big)
	}
	if got.Items[1].Mapped {
		t.Fatal("Small has no mapped tag but was marked mapped")
	}
}

func TestLibraryScanDeepUsesFileDetails(t *testing.T) {
	env := newTestEnv(t).login(t)
	srv := libraryFixture().start(t)
	addArr(t, env, "sonarr", "tv", srv.URL, "k", true)

	got := decodeJSON[libraryScanDTO](t, env.do(t, http.MethodGet, "/api/library/sonarr?deep=true", nil))
	if !got.Deep {
		t.Fatal("deep was not reported")
	}
	var big, small libraryItemDTO
	for _, it := range got.Items {
		switch it.Title {
		case "Big":
			big = it
		case "Small":
			small = it
		}
	}
	if !big.BitrateExact || big.VideoCodec != "h264" || big.Resolution != "1920x1080" {
		t.Fatalf("got %+v, want exact details from the files", big)
	}
	if big.FileCount != 2 || big.TotalSize != 2_000_000_000 || big.RuntimeSeconds != 7200 {
		t.Fatalf("got %+v, want the file sums", big)
	}
	if small.BitrateExact || small.VideoCodec != "hevc" {
		t.Fatalf("got %+v, want codec filled but no exact bitrate without a runtime", small)
	}
}

func TestLibraryScanIsCachedUntilRefreshed(t *testing.T) {
	env := newTestEnv(t).login(t)
	fake := libraryFixture()
	srv := fake.start(t)
	addArr(t, env, "sonarr", "tv", srv.URL, "k", true)

	first := decodeJSON[libraryScanDTO](t, env.do(t, http.MethodGet, "/api/library/sonarr", nil))
	fake.series = fake.series[:1]
	time.Sleep(5 * time.Millisecond)

	cached := decodeJSON[libraryScanDTO](t, env.do(t, http.MethodGet, "/api/library/sonarr", nil))
	if len(cached.Items) != 2 || !cached.ScannedAt.Equal(first.ScannedAt) {
		t.Fatalf("got %d items at %v, want the cached scan", len(cached.Items), cached.ScannedAt)
	}
	fresh := decodeJSON[libraryScanDTO](t, env.do(t, http.MethodGet, "/api/library/sonarr?refresh=true", nil))
	if len(fresh.Items) != 1 {
		t.Fatalf("got %d items, want the refresh to rescan", len(fresh.Items))
	}
}

func TestLibraryScanReportsAnUnreachableInstance(t *testing.T) {
	env := newTestEnv(t).login(t)
	srv := libraryFixture().start(t)
	addArr(t, env, "sonarr", "wrongkey", srv.URL, "nope", true)

	got := decodeJSON[libraryScanDTO](t, env.do(t, http.MethodGet, "/api/library/sonarr", nil))
	if len(got.Instances) != 1 || got.Instances[0].Error == "" || len(got.Items) != 0 {
		t.Fatalf("got %+v, want the instance listed with its error", got)
	}
}

func TestLibraryFiles(t *testing.T) {
	env := newTestEnv(t).login(t)
	srv := libraryFixture().start(t)
	id := addArr(t, env, "sonarr", "tv", srv.URL, "k", true)

	w := env.do(t, http.MethodGet, fmt.Sprintf("/api/library/sonarr/%d/10/files", id), nil)
	wantStatus(t, w, http.StatusOK)
	got := decodeJSON[[]libraryFileDTO](t, w)
	if len(got) != 2 {
		t.Fatalf("got %+v, want the two files with a path", got)
	}
	if got[0].FileID != 101 || !got[0].BitrateExact || got[0].BitrateBps != 1_100_000_000*8/3600 {
		t.Fatalf("got %+v first, want the higher bitrate file", got[0])
	}
	if got[1].Quality != "HDTV-1080p" || got[1].VideoCodec != "h264" {
		t.Fatalf("got %+v, want media info carried through", got[1])
	}
}

func TestLibraryTargetValidation(t *testing.T) {
	env := newTestEnv(t).login(t)
	srv := libraryFixture().start(t)
	id := addArr(t, env, "sonarr", "tv", srv.URL, "k", true)

	for path, want := range map[string]int{
		"/api/library/lidarr/1/10/files":                    http.StatusBadRequest,
		"/api/library/sonarr/abc/10/files":                  http.StatusBadRequest,
		fmt.Sprintf("/api/library/sonarr/%d/abc/files", id): http.StatusBadRequest,
		"/api/library/sonarr/999/10/files":                  http.StatusNotFound,
		fmt.Sprintf("/api/library/radarr/%d/10/files", id):  http.StatusBadRequest,
	} {
		w := env.do(t, http.MethodGet, path, nil)
		if w.Code != want {
			t.Fatalf("%s: got %d, want %d", path, w.Code, want)
		}
	}

	bad := addArr(t, env, "sonarr", "bad", srv.URL, "nope", true)
	wantStatus(t, env.do(t, http.MethodGet, fmt.Sprintf("/api/library/sonarr/%d/10/files", bad), nil), http.StatusBadGateway)
}

func TestLibraryAddTagTagsTheItemAndUpdatesTheCache(t *testing.T) {
	env := newTestEnv(t).login(t)
	fake := libraryFixture()
	srv := fake.start(t)
	id := addArr(t, env, "sonarr", "tv", srv.URL, "k", true)
	pid := addProfile(t, env, "tagger")
	mapRecodeTag(t, env, pid)

	before := decodeJSON[libraryScanDTO](t, env.do(t, http.MethodGet, "/api/library/sonarr", nil))
	for _, it := range before.Items {
		if it.Title == "Small" && it.Mapped {
			t.Fatal("Small started out mapped")
		}
	}

	w := env.do(t, http.MethodPost, fmt.Sprintf("/api/library/sonarr/%d/11/tags", id), map[string]int64{"tagId": 1})
	wantStatus(t, w, http.StatusOK)
	got := decodeJSON[map[string]any](t, w)
	if got["tagLabel"] != "recode" || got["profileId"] != float64(pid) {
		t.Fatalf("got %v", got)
	}
	if fake.editCount() != 1 {
		t.Fatalf("got %d editor calls, want 1", fake.editCount())
	}
	edit := fake.edits[0]
	if fmt.Sprint(edit["seriesIds"]) != "[11]" || edit["applyTags"] != "add" {
		t.Fatalf("got editor body %v", edit)
	}

	after := decodeJSON[libraryScanDTO](t, env.do(t, http.MethodGet, "/api/library/sonarr", nil))
	for _, it := range after.Items {
		if it.Title != "Small" {
			continue
		}
		if !it.Mapped || !slices.Contains(it.TagLabels, "recode") || !slices.Contains(it.MappedTags, "recode") {
			t.Fatalf("got %+v, want the cached item marked as tagged", it)
		}
	}
}

func TestLibraryAddTagRejections(t *testing.T) {
	env := newTestEnv(t).login(t)
	fake := libraryFixture()
	srv := fake.start(t)
	id := addArr(t, env, "sonarr", "tv", srv.URL, "k", true)
	mapRecodeTag(t, env, addProfile(t, env, "doomed"))
	path := fmt.Sprintf("/api/library/sonarr/%d/11/tags", id)

	wantStatus(t, env.do(t, http.MethodPost, path, map[string]int64{"tagId": 0}), http.StatusBadRequest)
	wantStatus(t, env.do(t, http.MethodPost, path, map[string]int64{"tagId": 42}), http.StatusBadRequest)

	w := env.do(t, http.MethodPost, path, map[string]int64{"tagId": 2})
	wantStatus(t, w, http.StatusBadRequest)
	if !strings.Contains(w.Body.String(), "not mapped") {
		t.Fatalf("got %q, want an unmapped tag error", w.Body.String())
	}

	fake.editErr = true
	wantStatus(t, env.do(t, http.MethodPost, path, map[string]int64{"tagId": 1}), http.StatusBadGateway)
	fake.editErr = false

	fake.tags = nil
	w = env.do(t, http.MethodPost, path, map[string]int64{"tagId": 1})
	wantStatus(t, w, http.StatusConflict)
	if !strings.Contains(w.Body.String(), "has no tags") {
		t.Fatalf("got %q", w.Body.String())
	}

	fake.failTags = true
	wantStatus(t, env.do(t, http.MethodPost, path, map[string]int64{"tagId": 1}), http.StatusBadGateway)
	if fake.editCount() != 0 {
		t.Fatalf("got %d editor calls from rejected requests", fake.editCount())
	}
}

func TestBitrateAndMostCommon(t *testing.T) {
	if bitrate(0, 10) != 0 || bitrate(10, 0) != 0 {
		t.Fatal("bitrate did not guard zero inputs")
	}
	if got := bitrate(1000, 8); got != 1000 {
		t.Fatalf("got %d, want 1000", got)
	}
	if got := mostCommon(map[string]int{"hevc": 2, "h264": 2, "av1": 1}); got != "h264" {
		t.Fatalf("got %q, want the alphabetical tie-break", got)
	}
	if got := mostCommon(nil); got != "" {
		t.Fatalf("got %q from an empty map", got)
	}
}
