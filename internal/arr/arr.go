// Package arr is a unified client for Sonarr and Radarr. They share the same /api/v3
// surface for tags, system status, and command dispatch — the only meaningful divergence
// is the webhook payload shape and the refresh command name. A Kind discriminator handles both.
package arr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

type Kind string

const (
	KindSonarr Kind = "sonarr"
	KindRadarr Kind = "radarr"
)

// HTTPTransport is the http.RoundTripper used by every *arr client created
// after this is set. Defaults to http.DefaultTransport. The logging package
// swaps this for an outbound-logging wrapper at startup so Sonarr/Radarr
// calls land in outbound.log instead of stdout.
var HTTPTransport http.RoundTripper = http.DefaultTransport

type Client struct {
	kind    Kind
	baseURL string
	apiKey  string
	http    *http.Client
}

func New(kind Kind, baseURL, apiKey string) *Client {
	return &Client{
		kind:    kind,
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 15 * time.Second, Transport: HTTPTransport},
	}
}

func (c *Client) Kind() Kind { return c.kind }

type Tag struct {
	ID    int64  `json:"id"`
	Label string `json:"label"`
}

// Item is the normalized form of a Sonarr/Radarr webhook payload — only the fields
// Recodarr actually consumes.
//
// ParentTags holds tag *labels* (strings), not IDs. *arr's webhook payloads
// serialize series.tags / movie.tags as a list of labels regardless of how the
// tags are stored internally; matching is therefore done by label.
type Item struct {
	EventType    string
	ParentID     int64    // seriesId or movieId
	ParentTitle  string
	ParentPath   string
	ParentTags   []string
	FileID       int64    // episodeFileId or movieFileId
	FilePath     string   // absolute, when present
	RelativePath string   // path relative to ParentPath
	Size         int64
	DownloadID   string
}

func (c *Client) Tags(ctx context.Context) ([]Tag, error) {
	var tags []Tag
	if err := c.getJSON(ctx, "/api/v3/tag", &tags); err != nil {
		return nil, fmt.Errorf("%s tags: %w", c.kind, err)
	}
	return tags, nil
}

// LibraryItem is a single series or movie discovered via the REST API (not the
// webhook). One Item per series/movie regardless of how many files it has;
// FileCount and TotalSize are aggregates for display.
//
// TagIDs are integer IDs as returned by /api/v3/series and /api/v3/movie.
// (Note: this is unlike *arr webhook payloads, which serialize the same field
// as label strings — see Item.ParentTags.)
type LibraryItem struct {
	ID        int64
	Title     string
	Path      string
	TagIDs    []int64
	FileCount int
	TotalSize int64
}

// LibraryFile is a single playable file under a series/movie. ParentID is the
// seriesId / movieId; ID is the episodeFileId / movieFileId.
type LibraryFile struct {
	ID           int64
	ParentID     int64
	Path         string
	RelativePath string
	Size         int64
}

// sonarrSeries is the subset of SeriesResource we need.
type sonarrSeries struct {
	ID         int64   `json:"id"`
	Title      string  `json:"title"`
	Path       string  `json:"path"`
	Tags       []int64 `json:"tags"`
	Statistics struct {
		EpisodeFileCount int   `json:"episodeFileCount"`
		SizeOnDisk       int64 `json:"sizeOnDisk"`
	} `json:"statistics"`
}

// sonarrEpisodeFile is the subset of EpisodeFileResource we need.
type sonarrEpisodeFile struct {
	ID           int64  `json:"id"`
	SeriesID     int64  `json:"seriesId"`
	Path         string `json:"path"`
	RelativePath string `json:"relativePath"`
	Size         int64  `json:"size"`
}

// radarrMovie is the subset of MovieResource we need.
type radarrMovie struct {
	ID         int64   `json:"id"`
	Title      string  `json:"title"`
	Path       string  `json:"path"`
	Tags       []int64 `json:"tags"`
	HasFile    bool    `json:"hasFile"`
	SizeOnDisk int64   `json:"sizeOnDisk"`
	MovieFile  *struct {
		ID           int64  `json:"id"`
		Path         string `json:"path"`
		RelativePath string `json:"relativePath"`
		Size         int64  `json:"size"`
	} `json:"movieFile,omitempty"`
}

// Library returns the full library as normalized LibraryItems. For Radarr,
// movies without a file (hasFile=false) are skipped — there's nothing to
// re-encode. The full library is fetched in one call; both *arr APIs return
// the entire list with no pagination.
func (c *Client) Library(ctx context.Context) ([]LibraryItem, error) {
	switch c.kind {
	case KindSonarr:
		var series []sonarrSeries
		if err := c.getJSON(ctx, "/api/v3/series", &series); err != nil {
			return nil, fmt.Errorf("sonarr series: %w", err)
		}
		out := make([]LibraryItem, 0, len(series))
		for _, s := range series {
			if s.Statistics.EpisodeFileCount == 0 {
				continue
			}
			out = append(out, LibraryItem{
				ID:        s.ID,
				Title:     s.Title,
				Path:      s.Path,
				TagIDs:    s.Tags,
				FileCount: s.Statistics.EpisodeFileCount,
				TotalSize: s.Statistics.SizeOnDisk,
			})
		}
		return out, nil
	case KindRadarr:
		var movies []radarrMovie
		if err := c.getJSON(ctx, "/api/v3/movie", &movies); err != nil {
			return nil, fmt.Errorf("radarr movies: %w", err)
		}
		out := make([]LibraryItem, 0, len(movies))
		for _, m := range movies {
			if !m.HasFile || m.MovieFile == nil {
				continue
			}
			size := m.SizeOnDisk
			if size == 0 {
				size = m.MovieFile.Size
			}
			out = append(out, LibraryItem{
				ID:        m.ID,
				Title:     m.Title,
				Path:      m.Path,
				TagIDs:    m.Tags,
				FileCount: 1,
				TotalSize: size,
			})
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unknown arr kind %q", c.kind)
	}
}

// Files returns the playable files for a single parent (series or movie).
// For Radarr there's at most one file per movie; for Sonarr there may be many.
func (c *Client) Files(ctx context.Context, parentID int64) ([]LibraryFile, error) {
	switch c.kind {
	case KindSonarr:
		var efs []sonarrEpisodeFile
		path := fmt.Sprintf("/api/v3/episodefile?seriesId=%d", parentID)
		if err := c.getJSON(ctx, path, &efs); err != nil {
			return nil, fmt.Errorf("sonarr episodefile: %w", err)
		}
		out := make([]LibraryFile, 0, len(efs))
		for _, ef := range efs {
			if ef.Path == "" {
				continue
			}
			out = append(out, LibraryFile{
				ID:           ef.ID,
				ParentID:     ef.SeriesID,
				Path:         ef.Path,
				RelativePath: ef.RelativePath,
				Size:         ef.Size,
			})
		}
		return out, nil
	case KindRadarr:
		// Radarr exposes the moviefile inline on the MovieResource; fetch the
		// single movie and read its movieFile.
		var m radarrMovie
		if err := c.getJSON(ctx, fmt.Sprintf("/api/v3/movie/%d", parentID), &m); err != nil {
			return nil, fmt.Errorf("radarr movie %d: %w", parentID, err)
		}
		if !m.HasFile || m.MovieFile == nil || m.MovieFile.Path == "" {
			return nil, nil
		}
		return []LibraryFile{{
			ID:           m.MovieFile.ID,
			ParentID:     m.ID,
			Path:         m.MovieFile.Path,
			RelativePath: m.MovieFile.RelativePath,
			Size:         m.MovieFile.Size,
		}}, nil
	default:
		return nil, fmt.Errorf("unknown arr kind %q", c.kind)
	}
}

// ImportEvent is a single downloadFolderImported history record, reduced to the
// fields backfill needs: where the file landed and which download-client item
// (for qBittorrent, the torrent infohash) produced it.
type ImportEvent struct {
	DownloadID   string
	ImportedPath string
	Date         time.Time
}

// historyRecord is the subset of a Sonarr/Radarr history record we decode.
type historyRecord struct {
	DownloadID string    `json:"downloadId"`
	EventType  string    `json:"eventType"`
	Date       time.Time `json:"date"`
	Data       struct {
		ImportedPath string `json:"importedPath"`
	} `json:"data"`
}

// ImportHistory returns this parent's downloadFolderImported events (those that
// carry a download id), newest first. *arr's library API doesn't expose the
// download-client hash, but its import history does — this is how a backfilled
// library file recovers a torrent hash to poll qBit with. A series response
// covers every episode; callers match the right event by path.
func (c *Client) ImportHistory(ctx context.Context, parentID int64) ([]ImportEvent, error) {
	var path string
	switch c.kind {
	case KindSonarr:
		path = fmt.Sprintf("/api/v3/history/series?seriesId=%d", parentID)
	case KindRadarr:
		path = fmt.Sprintf("/api/v3/history/movie?movieId=%d", parentID)
	default:
		return nil, fmt.Errorf("unknown arr kind %q", c.kind)
	}
	var records []historyRecord
	if err := c.getJSON(ctx, path, &records); err != nil {
		return nil, fmt.Errorf("%s import history: %w", c.kind, err)
	}
	out := make([]ImportEvent, 0, len(records))
	for _, r := range records {
		if r.EventType != "downloadFolderImported" || r.DownloadID == "" {
			continue
		}
		out = append(out, ImportEvent{DownloadID: r.DownloadID, ImportedPath: r.Data.ImportedPath, Date: r.Date})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.After(out[j].Date) })
	return out, nil
}

// MatchImportDownloadID picks the download id for the file at absPath from a
// parent's import events (as returned by ImportHistory, newest first). It
// matches on the imported path — exact, or by relative-path suffix to tolerate
// the OS path-separator differing between the *arr host and Recodarr. For
// Radarr (one file per movie) it falls back to the most recent import when no
// path matches, since any import for that movie refers to the same file; for
// Sonarr it never cross-matches episodes. Returns "" when nothing matches.
func MatchImportDownloadID(events []ImportEvent, kind Kind, absPath, relativePath string) string {
	relNorm := strings.ReplaceAll(relativePath, "\\", "/")
	for _, e := range events {
		imp := e.ImportedPath
		if imp == absPath {
			return e.DownloadID
		}
		if relNorm != "" && strings.HasSuffix(strings.ReplaceAll(imp, "\\", "/"), "/"+relNorm) {
			return e.DownloadID
		}
	}
	if kind == KindRadarr && len(events) > 0 {
		return events[0].DownloadID
	}
	return ""
}

// Ping verifies connectivity and API key, distinguishing 401 from other errors.
func (c *Client) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v3/system/status", nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized:
		return fmt.Errorf("invalid API key (401)")
	default:
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
}

// Refresh asks the *arr to rescan the parent (series or movie) so it picks up the new file.
func (c *Client) Refresh(ctx context.Context, parentID int64) error {
	var body any
	switch c.kind {
	case KindSonarr:
		body = map[string]any{"name": "RefreshSeries", "seriesId": parentID}
	case KindRadarr:
		body = map[string]any{"name": "RefreshMovie", "movieIds": []int64{parentID}}
	default:
		return fmt.Errorf("unknown arr kind %q", c.kind)
	}
	buf, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v3/command", bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s refresh: status=%d", c.kind, resp.StatusCode)
	}
	return nil
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status=%d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// --- webhook payload parsing ---

type sonarrPayload struct {
	EventType string `json:"eventType"`
	Series    struct {
		ID    int64   `json:"id"`
		Title string  `json:"title"`
		Path  string  `json:"path"`
		Tags  []string `json:"tags,omitempty"`
	} `json:"series"`
	EpisodeFile struct {
		ID           int64  `json:"id"`
		RelativePath string `json:"relativePath"`
		Path         string `json:"path"`
		Size         int64  `json:"size"`
	} `json:"episodeFile"`
	DownloadID string `json:"downloadId"`
}

type radarrPayload struct {
	EventType string `json:"eventType"`
	Movie     struct {
		ID         int64    `json:"id"`
		Title      string   `json:"title"`
		FolderPath string   `json:"folderPath"` // Radarr's "path" equivalent on movies
		Tags       []string `json:"tags,omitempty"`
	} `json:"movie"`
	MovieFile struct {
		ID           int64  `json:"id"`
		RelativePath string `json:"relativePath"`
		Path         string `json:"path"`
		Size         int64  `json:"size"`
	} `json:"movieFile"`
	DownloadID string `json:"downloadId"`
}

// ParseWebhook decodes a Sonarr or Radarr webhook body into a normalized Item.
func ParseWebhook(kind Kind, body []byte) (*Item, error) {
	switch kind {
	case KindSonarr:
		var p sonarrPayload
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return &Item{
			EventType:    p.EventType,
			ParentID:     p.Series.ID,
			ParentTitle:  p.Series.Title,
			ParentPath:   p.Series.Path,
			ParentTags:   p.Series.Tags,
			FileID:       p.EpisodeFile.ID,
			FilePath:     p.EpisodeFile.Path,
			RelativePath: p.EpisodeFile.RelativePath,
			Size:         p.EpisodeFile.Size,
			DownloadID:   p.DownloadID,
		}, nil
	case KindRadarr:
		var p radarrPayload
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return &Item{
			EventType:    p.EventType,
			ParentID:     p.Movie.ID,
			ParentTitle:  p.Movie.Title,
			ParentPath:   p.Movie.FolderPath,
			ParentTags:   p.Movie.Tags,
			FileID:       p.MovieFile.ID,
			FilePath:     p.MovieFile.Path,
			RelativePath: p.MovieFile.RelativePath,
			Size:         p.MovieFile.Size,
			DownloadID:   p.DownloadID,
		}, nil
	default:
		return nil, fmt.Errorf("unknown arr kind %q", kind)
	}
}
