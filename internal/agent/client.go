package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sratabix/recodarr/internal/handbrake"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{},
	}
}

func (c *Client) Ping(ctx context.Context) (*HealthSnapshot, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/healthz", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("agent /healthz returned %d", resp.StatusCode)
	}
	var hs HealthSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&hs); err != nil {
		return nil, fmt.Errorf("decode healthz: %w", err)
	}
	return &hs, nil
}

func (c *Client) Encode(
	ctx context.Context,
	sourcePath string,
	s handbrake.Settings,
	onProgress func(handbrake.Progress),
) (handbrake.RunResult, error) {
	info, err := os.Stat(sourcePath)
	if err != nil {
		return handbrake.RunResult{}, fmt.Errorf("stat source: %w", err)
	}
	req := JobRequest{
		Filename:        filepath.Base(sourcePath),
		SizeBytes:       info.Size(),
		Settings:        s,
		OutputContainer: containerFor(sourcePath, s),
	}

	created, err := c.createJob(ctx, req)
	if err != nil {
		return handbrake.RunResult{}, fmt.Errorf("agent create job: %w", err)
	}

	cleanup := true
	defer func() {
		if !cleanup {
			return
		}
		dctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = c.Delete(dctx, created.JobID)
	}()

	if err := c.uploadSource(ctx, created.UploadURL, sourcePath, info.Size()); err != nil {
		return handbrake.RunResult{}, fmt.Errorf("agent upload: %w", err)
	}

	if err := c.watchUntilDone(ctx, created.JobID, onProgress); err != nil {
		logCtx, logCancel := context.WithTimeout(context.Background(), 10*time.Second)
		log, _ := c.fetchLog(logCtx, created.JobID)
		logCancel()
		return handbrake.RunResult{Log: log}, fmt.Errorf("agent encode: %w", err)
	}

	tempPath := localTempPath(sourcePath, s)
	size, err := c.downloadOutput(ctx, created.JobID, tempPath)
	if err != nil {
		_ = os.Remove(tempPath)
		return handbrake.RunResult{}, fmt.Errorf("agent download: %w", err)
	}

	log, _ := c.fetchLog(ctx, created.JobID)

	cleanup = false
	dctx, cancelDel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelDel()
	_ = c.Delete(dctx, created.JobID)

	return handbrake.RunResult{FinalSize: size, TempPath: tempPath, Log: log}, nil
}

func (c *Client) Delete(ctx context.Context, id string) error {
	req, err := c.newRequest(ctx, http.MethodDelete, "/jobs/"+id, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("agent DELETE returned %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) createJob(ctx context.Context, body JobRequest) (*JobCreateResponse, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/jobs", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, readErr(resp.Body))
	}
	var cr JobCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return nil, fmt.Errorf("decode create response: %w", err)
	}
	return &cr, nil
}

func (c *Client) uploadSource(ctx context.Context, uploadPath, sourcePath string, size int64) error {
	f, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	path := strings.TrimPrefix(uploadPath, PathPrefix)
	req, err := c.newRequest(ctx, http.MethodPut, path, f)
	if err != nil {
		return err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("status %d: %s", resp.StatusCode, readErr(resp.Body))
	}
	return nil
}

func (c *Client) watchUntilDone(ctx context.Context, id string, onProgress func(handbrake.Progress)) error {
	req, err := c.newRequest(ctx, http.MethodGet, "/jobs/"+id+"/events", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d: %s", resp.StatusCode, readErr(resp.Body))
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 16*1024), 1<<20)
	var event, data string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":

			if event != "" {
				c.handleEvent(event, data, onProgress)
				if event == EventState {
					var sp StatePayload
					if json.Unmarshal([]byte(data), &sp) == nil && sp.State.Terminal() {
						if sp.State == StateDone {
							return nil
						}
						msg := sp.Error
						if msg == "" {
							msg = string(sp.State)
						}
						return fmt.Errorf("remote: %s", msg)
					}
				}
			}
			event, data = "", ""
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("sse stream: %w", err)
	}

	js, err := c.poll(ctx, id)
	if err != nil {
		return fmt.Errorf("sse closed; poll fallback: %w", err)
	}
	if js.State == StateDone {
		return nil
	}
	return fmt.Errorf("remote ended in state %s: %s", js.State, js.Error)
}

func (c *Client) handleEvent(event, data string, onProgress func(handbrake.Progress)) {
	if event != EventProgress {
		return
	}
	var pp ProgressPayload
	if err := json.Unmarshal([]byte(data), &pp); err != nil {
		return
	}
	if onProgress != nil {
		onProgress(handbrake.Progress{Percent: pp.Percent, FPS: pp.FPS, ETA: pp.ETA})
	}
}

func (c *Client) poll(ctx context.Context, id string) (*JobStateSnapshot, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/jobs/"+id, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var js JobStateSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&js); err != nil {
		return nil, err
	}
	return &js, nil
}

func (c *Client) downloadOutput(ctx context.Context, id, dest string) (int64, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/jobs/"+id+"/output", nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf("status %d: %s", resp.StatusCode, readErr(resp.Body))
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, resp.Body)
	_ = f.Close()
	if err != nil {
		return 0, err
	}
	return n, nil
}

func (c *Client) fetchLog(ctx context.Context, id string) (string, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/jobs/"+id+"/log", nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+PathPrefix+path, body)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return req, nil
}

func readErr(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 4096))
	var er ErrorResponse
	if json.Unmarshal(b, &er) == nil && er.Error != "" {
		return er.Error
	}
	return strings.TrimSpace(string(b))
}

func localTempPath(input string, s handbrake.Settings) string {
	dir := filepath.Dir(input)
	base := filepath.Base(input)
	ext := filepath.Ext(base)
	if s.ContainerFormat == "mp4" {
		ext = ".mp4"
	}
	return filepath.Join(dir, "."+base+".recodarr.tmp"+ext)
}

func containerFor(sourcePath string, s handbrake.Settings) string {
	if s.ContainerFormat == "mp4" {
		return "mp4"
	}
	if s.ContainerFormat == "mkv" {
		return "mkv"
	}

	switch strings.ToLower(filepath.Ext(sourcePath)) {
	case ".mp4", ".m4v":
		return "mp4"
	default:
		return "mkv"
	}
}

var _ = slog.Info
var _ = errors.New
