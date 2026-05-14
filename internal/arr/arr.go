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
	"strings"
	"time"
)

type Kind string

const (
	KindSonarr Kind = "sonarr"
	KindRadarr Kind = "radarr"
)

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
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Client) Kind() Kind { return c.kind }

type Tag struct {
	ID    int64  `json:"id"`
	Label string `json:"label"`
}

// Item is the normalized form of a Sonarr/Radarr webhook payload — only the fields
// Recodarr actually consumes.
type Item struct {
	EventType    string
	ParentID     int64    // seriesId or movieId
	ParentTitle  string
	ParentPath   string
	ParentTags   []int64
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
		Tags  []int64 `json:"tags,omitempty"`
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
		ID    int64   `json:"id"`
		Title string  `json:"title"`
		Path  string  `json:"path"`
		Tags  []int64 `json:"tags,omitempty"`
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
			ParentPath:   p.Movie.Path,
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
