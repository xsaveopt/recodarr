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

func (c *Client) Tags(ctx context.Context) ([]Tag, error) {
	var tags []Tag
	if err := c.getJSON(ctx, "/api/v3/tag", &tags); err != nil {
		return nil, fmt.Errorf("%s tags: %w", c.kind, err)
	}
	return tags, nil
}

type LibraryItem struct {
	ID        int64
	Title     string
	Path      string
	TagIDs    []int64
	FileCount int
	TotalSize int64
}

type LibraryFile struct {
	ID           int64
	ParentID     int64
	Path         string
	RelativePath string
	Size         int64
}

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

type sonarrEpisodeFile struct {
	ID           int64  `json:"id"`
	SeriesID     int64  `json:"seriesId"`
	Path         string `json:"path"`
	RelativePath string `json:"relativePath"`
	Size         int64  `json:"size"`
}

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

type ImportEvent struct {
	DownloadID   string
	ImportedPath string
	Date         time.Time
}

type historyRecord struct {
	DownloadID string    `json:"downloadId"`
	EventType  string    `json:"eventType"`
	Date       time.Time `json:"date"`
	Data       struct {
		ImportedPath string `json:"importedPath"`
	} `json:"data"`
}

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
