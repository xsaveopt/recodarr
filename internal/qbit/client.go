package qbit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

var HTTPTransport http.RoundTripper = http.DefaultTransport

type Client struct {
	baseURL  string
	username string
	password string
	http     *http.Client
}

func New(baseURL, username, password string) (*Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		username: username,
		password: password,
		http:     &http.Client{Jar: jar, Timeout: 15 * time.Second, Transport: HTTPTransport},
	}, nil
}

func (c *Client) Login(ctx context.Context) error {
	form := url.Values{"username": {c.username}, "password": {c.password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v2/auth/login", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Recodarr")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	trimmed := strings.TrimSpace(string(body))

	var loginErr error
	switch {
	case resp.StatusCode == http.StatusOK && trimmed == "Fails.":
		loginErr = fmt.Errorf("qbit rejected credentials (wrong username or password)")

	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusForbidden:
		loginErr = fmt.Errorf("qbit returned 403 (likely IP-banned after failed attempts; restart qBittorrent or wait the ban out)")
	case resp.StatusCode == http.StatusUnauthorized:
		loginErr = fmt.Errorf(`qbit returned 401. Most likely qBit's "Server domains" (Tools → Options → Web UI) is set to something restrictive that excludes %q, or there's a port mismatch between qBit's bind port and the URL host. Body: %q`, hostOnly(c.baseURL), trimmed)
	default:
		loginErr = fmt.Errorf("qbit login failed: status=%d body=%q", resp.StatusCode, trimmed)
	}

	slog.Warn("qbit login failed",
		"url", req.URL.String(),
		"hostHeader", req.Host,
		"status", resp.StatusCode,
		"respBody", trimmed,
		"respHeaders", flattenHeaders(resp.Header),
	)
	return loginErr
}

func flattenHeaders(h http.Header) string {
	var sb strings.Builder
	for k, vs := range h {
		for _, v := range vs {
			if sb.Len() > 0 {
				sb.WriteString("; ")
			}
			sb.WriteString(k)
			sb.WriteString("=")
			sb.WriteString(v)
		}
	}
	return sb.String()
}

func hostOnly(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return rawURL
	}
	return u.Hostname()
}

type Torrent struct {
	Hash     string  `json:"hash"`
	Name     string  `json:"name"`
	State    string  `json:"state"`
	Progress float64 `json:"progress"`
	Category string  `json:"category"`
	SavePath string  `json:"save_path"`
}

func (c *Client) TorrentByHash(ctx context.Context, hash string) (*Torrent, error) {
	got, err := c.TorrentsByHashes(ctx, []string{hash})
	if err != nil {
		return nil, err
	}
	if t, ok := got[strings.ToLower(hash)]; ok {
		return &t, nil
	}
	return nil, nil
}

func (c *Client) TorrentsByHashes(ctx context.Context, hashes []string) (map[string]Torrent, error) {
	if len(hashes) == 0 {
		return map[string]Torrent{}, nil
	}
	lowered := make([]string, len(hashes))
	for i, h := range hashes {
		lowered[i] = strings.ToLower(h)
	}
	u := fmt.Sprintf("%s/api/v2/torrents/info?hashes=%s",
		c.baseURL, url.QueryEscape(strings.Join(lowered, "|")))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("qbit torrents/info: status=%d", resp.StatusCode)
	}
	var list []Torrent
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	out := make(map[string]Torrent, len(list))
	for _, t := range list {
		out[strings.ToLower(t.Hash)] = t
	}
	return out, nil
}
