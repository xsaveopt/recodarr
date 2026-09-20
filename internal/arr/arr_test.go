package arr

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type capture struct {
	method string
	path   string
	query  string
	apiKey string
	body   string
}

func serve(t *testing.T, routes map[string]any) (*httptest.Server, *[]capture) {
	t.Helper()
	var seen []capture
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = append(seen, capture{
			method: r.Method,
			path:   r.URL.Path,
			query:  r.URL.RawQuery,
			apiKey: r.Header.Get("X-Api-Key"),
			body:   string(body),
		})
		v, ok := routes[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if code, isCode := v.(int); isCode {
			w.WriteHeader(code)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestParseRunTime(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"", 0},
		{"45", 45},
		{"01:30", 90},
		{"1:02:03", 3723},
		{"00:45:00.5000000", 2700},
		{"garbage", 0},
		{"1:xx", 0},
	} {
		if got := parseRunTime(tc.in); got != tc.want {
			t.Fatalf("parseRunTime(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestNewTrimsTheTrailingSlash(t *testing.T) {
	c := New(KindSonarr, "http://sonarr:8989/", "key")
	if c.baseURL != "http://sonarr:8989" {
		t.Fatalf("got %q, want the trailing slash trimmed", c.baseURL)
	}
	if c.Kind() != KindSonarr {
		t.Fatalf("got kind %q", c.Kind())
	}
}

func TestTagsSendsTheApiKey(t *testing.T) {
	srv, seen := serve(t, map[string]any{
		"/api/v3/tag": []Tag{{ID: 1, Label: "recode"}, {ID: 2, Label: "skip"}},
	})
	tags, err := New(KindSonarr, srv.URL, "s3cret").Tags(context.Background())
	if err != nil {
		t.Fatalf("tags: %v", err)
	}
	if len(tags) != 2 || tags[0].Label != "recode" {
		t.Fatalf("got %+v", tags)
	}
	if (*seen)[0].apiKey != "s3cret" {
		t.Fatalf("got api key %q, want it on the request", (*seen)[0].apiKey)
	}
}

func TestSonarrLibrarySkipsSeriesWithNoFiles(t *testing.T) {
	srv, _ := serve(t, map[string]any{
		"/api/v3/series": []map[string]any{
			{
				"id": 1, "title": "Kept", "path": "/tv/Kept", "tags": []int64{7},
				"runtime": 45, "year": 2020,
				"statistics": map[string]any{"episodeFileCount": 3, "sizeOnDisk": 900},
			},
			{
				"id": 2, "title": "Empty", "path": "/tv/Empty", "tags": []int64{7},
				"statistics": map[string]any{"episodeFileCount": 0, "sizeOnDisk": 0},
			},
		},
	})
	items, err := New(KindSonarr, srv.URL, "k").Library(context.Background())
	if err != nil {
		t.Fatalf("library: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want only the series with files", len(items))
	}
	got := items[0]
	if got.ID != 1 || got.Title != "Kept" || got.FileCount != 3 || got.TotalSize != 900 {
		t.Fatalf("got %+v", got)
	}
	if len(got.TagIDs) != 1 || got.TagIDs[0] != 7 {
		t.Fatalf("got tags %v, want the integer ids", got.TagIDs)
	}
}

func TestRadarrLibrarySkipsMoviesWithoutAFile(t *testing.T) {
	srv, _ := serve(t, map[string]any{
		"/api/v3/movie": []map[string]any{
			{
				"id": 10, "title": "Kept", "path": "/films/Kept", "tags": []int64{3},
				"hasFile": true, "sizeOnDisk": 0, "year": 1999,
				"movieFile": map[string]any{"id": 99, "path": "/films/Kept/k.mkv", "size": 4242},
			},
			{"id": 11, "title": "Missing", "hasFile": false},
			{"id": 12, "title": "NoFileObject", "hasFile": true},
		},
	})
	items, err := New(KindRadarr, srv.URL, "k").Library(context.Background())
	if err != nil {
		t.Fatalf("library: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want only the movie with a file", len(items))
	}
	if items[0].TotalSize != 4242 {
		t.Fatalf("got size %d, want the fallback to the file size", items[0].TotalSize)
	}
	if items[0].FileCount != 1 {
		t.Fatalf("got file count %d, want 1", items[0].FileCount)
	}
}

func TestLibraryRejectsAnUnknownKind(t *testing.T) {
	if _, err := New(Kind("lidarr"), "http://x", "k").Library(context.Background()); err == nil {
		t.Fatal("an unknown kind was accepted")
	}
	if _, err := New(Kind("lidarr"), "http://x", "k").Files(context.Background(), 1); err == nil {
		t.Fatal("an unknown kind was accepted by Files")
	}
	if _, err := New(Kind("lidarr"), "http://x", "k").ImportHistory(context.Background(), 1); err == nil {
		t.Fatal("an unknown kind was accepted by ImportHistory")
	}
}

func TestSonarrFilesCarriesMediaInfo(t *testing.T) {
	srv, seen := serve(t, map[string]any{
		"/api/v3/episodefile": []map[string]any{
			{
				"id": 5, "seriesId": 1, "path": "/tv/Show/S01E01.mkv",
				"relativePath": "Season 1/S01E01.mkv", "size": 1000,
				"quality":   map[string]any{"quality": map[string]any{"name": "WEBDL-1080p"}},
				"mediaInfo": map[string]any{"runTime": "00:45:00", "videoCodec": "h264", "audioCodec": "EAC3", "resolution": "1920x1080", "videoBitrate": 5000},
			},
			{"id": 6, "seriesId": 1, "path": "", "size": 10},
		},
	})
	files, err := New(KindSonarr, srv.URL, "k").Files(context.Background(), 1)
	if err != nil {
		t.Fatalf("files: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("got %d files, want the empty path dropped", len(files))
	}
	f := files[0]
	if f.RuntimeSeconds != 2700 {
		t.Fatalf("got runtime %d, want 2700", f.RuntimeSeconds)
	}
	if f.Quality != "WEBDL-1080p" || f.VideoCodec != "h264" || f.Resolution != "1920x1080" {
		t.Fatalf("media info was not applied: %+v", f)
	}
	if !strings.Contains((*seen)[0].query, "seriesId=1") {
		t.Fatalf("got query %q, want the series id", (*seen)[0].query)
	}
}

func TestRadarrFilesReturnsNothingWhenThereIsNoFile(t *testing.T) {
	srv, _ := serve(t, map[string]any{
		"/api/v3/movie/10": map[string]any{"id": 10, "hasFile": false},
	})
	files, err := New(KindRadarr, srv.URL, "k").Files(context.Background(), 10)
	if err != nil {
		t.Fatalf("files: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("got %d files, want none", len(files))
	}
}

func TestImportHistoryKeepsOnlyImportsAndSortsNewestFirst(t *testing.T) {
	older := time.Now().Add(-2 * time.Hour)
	newer := time.Now().Add(-time.Hour)
	srv, seen := serve(t, map[string]any{
		"/api/v3/history/series": []map[string]any{
			{"downloadId": "OLD", "eventType": "downloadFolderImported", "date": older,
				"data": map[string]any{"importedPath": "/tv/Show/old.mkv"}},
			{"downloadId": "GRAB", "eventType": "grabbed", "date": newer},
			{"downloadId": "", "eventType": "downloadFolderImported", "date": newer},
			{"downloadId": "NEW", "eventType": "downloadFolderImported", "date": newer,
				"data": map[string]any{"importedPath": "/tv/Show/new.mkv"}},
		},
	})
	events, err := New(KindSonarr, srv.URL, "k").ImportHistory(context.Background(), 1)
	if err != nil {
		t.Fatalf("import history: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want only the two real imports", len(events))
	}
	if events[0].DownloadID != "NEW" {
		t.Fatalf("got %q first, want the newest import", events[0].DownloadID)
	}
	if !strings.Contains((*seen)[0].query, "seriesId=1") {
		t.Fatalf("got query %q", (*seen)[0].query)
	}
}

func TestImportHistoryUsesTheRadarrRoute(t *testing.T) {
	srv, seen := serve(t, map[string]any{
		"/api/v3/history/movie": []map[string]any{},
	})
	if _, err := New(KindRadarr, srv.URL, "k").ImportHistory(context.Background(), 4); err != nil {
		t.Fatalf("import history: %v", err)
	}
	if (*seen)[0].path != "/api/v3/history/movie" {
		t.Fatalf("got path %q, want the radarr history route", (*seen)[0].path)
	}
	if !strings.Contains((*seen)[0].query, "movieId=4") {
		t.Fatalf("got query %q", (*seen)[0].query)
	}
}

func TestMatchImportDownloadID(t *testing.T) {
	events := []ImportEvent{
		{DownloadID: "AAA", ImportedPath: "/tv/Show/Season 1/S01E01.mkv"},
		{DownloadID: "BBB", ImportedPath: `C:\tv\Show\Season 1\S01E02.mkv`},
	}

	if got := MatchImportDownloadID(events, KindSonarr, "/tv/Show/Season 1/S01E01.mkv", ""); got != "AAA" {
		t.Fatalf("got %q, want an exact absolute match", got)
	}
	if got := MatchImportDownloadID(events, KindSonarr, "/other/path.mkv", "Season 1/S01E02.mkv"); got != "BBB" {
		t.Fatalf("got %q, want the relative suffix to match across separators", got)
	}
	if got := MatchImportDownloadID(events, KindSonarr, "/other/path.mkv", `Season 1\S01E02.mkv`); got != "BBB" {
		t.Fatalf("got %q, want backslashes in the relative path normalized", got)
	}
	if got := MatchImportDownloadID(events, KindSonarr, "/nope.mkv", "nope.mkv"); got != "" {
		t.Fatalf("got %q, want no sonarr guess when nothing matches", got)
	}
	if got := MatchImportDownloadID(events, KindRadarr, "/nope.mkv", "nope.mkv"); got != "AAA" {
		t.Fatalf("got %q, want radarr to fall back to the newest event", got)
	}
	if got := MatchImportDownloadID(nil, KindRadarr, "/nope.mkv", ""); got != "" {
		t.Fatalf("got %q, want no fallback with no events", got)
	}
}

func TestMatchImportDownloadIDDoesNotMatchAPartialFilename(t *testing.T) {
	events := []ImportEvent{{DownloadID: "AAA", ImportedPath: "/tv/Show/S01E011.mkv"}}
	if got := MatchImportDownloadID(events, KindSonarr, "/x.mkv", "S01E01.mkv"); got != "" {
		t.Fatalf("got %q, want no match on a filename that is only a prefix", got)
	}
}

func TestPingClassifiesStatuses(t *testing.T) {
	for status, wantErr := range map[int]string{
		http.StatusOK:           "",
		http.StatusUnauthorized: "invalid API key",
		http.StatusBadGateway:   "unexpected status 502",
	} {
		srv, _ := serve(t, map[string]any{"/api/v3/system/status": status})
		err := New(KindSonarr, srv.URL, "k").Ping(context.Background())
		if wantErr == "" {
			if err != nil {
				t.Fatalf("status %d: got %v, want success", status, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Fatalf("status %d: got %v, want %q", status, err, wantErr)
		}
	}
}

func TestRefreshUsesTheRightCommandPerKind(t *testing.T) {
	for kind, want := range map[Kind]string{
		KindSonarr: "RefreshSeries",
		KindRadarr: "RefreshMovie",
	} {
		srv, seen := serve(t, map[string]any{"/api/v3/command": http.StatusCreated})
		if err := New(kind, srv.URL, "k").Refresh(context.Background(), 12); err != nil {
			t.Fatalf("%s refresh: %v", kind, err)
		}
		c := (*seen)[0]
		if c.method != http.MethodPost || c.path != "/api/v3/command" {
			t.Fatalf("got %s %s", c.method, c.path)
		}
		if !strings.Contains(c.body, want) {
			t.Fatalf("%s sent %q, want %q", kind, c.body, want)
		}
		if !strings.Contains(c.body, "12") {
			t.Fatalf("%s sent %q, want the parent id", kind, c.body)
		}
	}
}

func TestRefreshReportsAServerError(t *testing.T) {
	srv, _ := serve(t, map[string]any{"/api/v3/command": http.StatusInternalServerError})
	if err := New(KindSonarr, srv.URL, "k").Refresh(context.Background(), 1); err == nil {
		t.Fatal("a 500 was reported as a successful refresh")
	}
}

func TestAddTagUsesTheEditorEndpoint(t *testing.T) {
	for kind, want := range map[Kind]string{
		KindSonarr: "seriesIds",
		KindRadarr: "movieIds",
	} {
		path := "/api/v3/series/editor"
		if kind == KindRadarr {
			path = "/api/v3/movie/editor"
		}
		srv, seen := serve(t, map[string]any{path: http.StatusAccepted})
		if err := New(kind, srv.URL, "k").AddTag(context.Background(), 3, 9); err != nil {
			t.Fatalf("%s add tag: %v", kind, err)
		}
		c := (*seen)[0]
		if c.method != http.MethodPut {
			t.Fatalf("got %s, want PUT", c.method)
		}
		if !strings.Contains(c.body, want) || !strings.Contains(c.body, `"applyTags":"add"`) {
			t.Fatalf("%s sent %q", kind, c.body)
		}
	}
	if err := New(Kind("lidarr"), "http://x", "k").AddTag(context.Background(), 1, 1); err == nil {
		t.Fatal("an unknown kind was accepted by AddTag")
	}
}

func TestGetJSONSurfacesANonOkStatus(t *testing.T) {
	srv, _ := serve(t, map[string]any{"/api/v3/tag": http.StatusForbidden})
	_, err := New(KindSonarr, srv.URL, "k").Tags(context.Background())
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("got %v, want the status surfaced", err)
	}
}

func TestClientFailsOnAnUnreachableHost(t *testing.T) {
	srv, _ := serve(t, map[string]any{})
	url := srv.URL
	srv.Close()
	if _, err := New(KindSonarr, url, "k").Tags(context.Background()); err == nil {
		t.Fatal("a closed server was reported as success")
	}
}
