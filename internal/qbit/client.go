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
		http:     &http.Client{Jar: jar, Timeout: 15 * time.Second},
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
	// Intentionally do NOT set Origin/Referer. qBit's CSRF check (see isCrossSiteRequest
	// in webapplication.cpp) treats requests with neither header as same-origin and lets
	// them through; setting Origin triggers a strict host-equality check that fails on
	// any reverse-proxy / port-forward / hostname-vs-IP mismatch. Sonarr and Radarr
	// rely on the same omission.
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	trimmed := strings.TrimSpace(string(body))

	if resp.StatusCode != http.StatusOK || trimmed != "Ok." {
		// Loud diagnostic so we can tell apart CSRF vs. host-validation vs. wrong creds
		// vs. ban. Includes the actual Host header Go sent and qBit's full response.
		slog.Warn("qbit login failed",
			"url", req.URL.String(),
			"hostHeader", req.Host,
			"status", resp.StatusCode,
			"respBody", trimmed,
			"respHeaders", flattenHeaders(resp.Header),
		)
	}
	switch {
	// Explicit failure: qBit returned 200 with the literal "Fails." body.
	case resp.StatusCode == http.StatusOK && trimmed == "Fails.":
		return fmt.Errorf("qbit rejected credentials (wrong username or password)")
	// Any 2xx is treated as success. 200 + "Ok." is the normal login response;
	// 204 No Content is what qBit returns when the client IP is in the
	// "Bypass authentication for clients on whitelisted IP subnets" list and no
	// login is required.
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("qbit returned 403 (likely IP-banned after failed attempts; restart qBittorrent or wait the ban out)")
	case resp.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf(`qbit returned 401. Most likely qBit's "Server domains" (Tools → Options → Web UI) is set to something restrictive that excludes %q, or there's a port mismatch between qBit's bind port and the URL host. Body: %q`, hostOnly(c.baseURL), trimmed)
	default:
		return fmt.Errorf("qbit login failed: status=%d body=%q", resp.StatusCode, trimmed)
	}
}

// flattenHeaders serializes response headers as a single string for logging.
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

// hostOnly strips scheme and port from a URL, leaving just the host part.
// Used to suggest the value qBit's "Server domains" field needs.
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
}

// TorrentByHash returns the torrent if it exists in qBit, or (nil, nil) if it's gone.
func (c *Client) TorrentByHash(ctx context.Context, hash string) (*Torrent, error) {
	u := fmt.Sprintf("%s/api/v2/torrents/info?hashes=%s", c.baseURL, url.QueryEscape(strings.ToLower(hash)))
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
	if len(list) == 0 {
		return nil, nil
	}
	return &list[0], nil
}
