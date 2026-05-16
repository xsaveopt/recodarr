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

// Client speaks the agent protocol on behalf of the Recodarr server. One
// Client per configured remote agent. Safe for concurrent use; the underlying
// http.Client manages connection reuse.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewClient constructs a Client. baseURL is the agent's base address
// (e.g. "http://gpu-box:8090") — no trailing slash. token is the shared
// bearer secret configured on the agent.
//
// The http.Client carries no overall timeout: encodes legitimately take
// hours. Per-request bounds live inside individual operations.
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{},
	}
}

// Ping returns the agent's /healthz snapshot, or an error if the agent is
// unreachable / mis-configured. Used by the health checker.
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

// Encode performs a remote encode: uploads sourcePath, watches progress over
// SSE (forwarding to onProgress), downloads the result to a sibling temp
// file, and returns a handbrake.RunResult shaped identically to what the
// local encoder produces. The caller commits the temp file via
// handbrake.Commit the same way it does for local encodes.
//
// On any failure mid-flight the partial agent-side job is best-effort
// deleted so the agent's disk doesn't fill up.
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
	// Defer cleanup, but skip it on success — Recodarr's worker DELETEs the
	// job explicitly after the local commit succeeds so the agent disk frees
	// in the steady state.
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
		return handbrake.RunResult{}, fmt.Errorf("agent encode: %w", err)
	}

	tempPath := localTempPath(sourcePath, s)
	size, err := c.downloadOutput(ctx, created.JobID, tempPath)
	if err != nil {
		_ = os.Remove(tempPath)
		return handbrake.RunResult{}, fmt.Errorf("agent download: %w", err)
	}

	// Best-effort fetch the encode log so the standard failure-path UI (which
	// shows handbrake stdout/stderr) works for remote encodes too.
	log, _ := c.fetchLog(ctx, created.JobID)

	cleanup = false
	dctx, cancelDel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelDel()
	_ = c.Delete(dctx, created.JobID)

	return handbrake.RunResult{FinalSize: size, TempPath: tempPath, Log: log}, nil
}

// Delete asks the agent to drop a job and its on-disk artifacts. Safe to
// call on an unknown id (the agent responds 204 either way).
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

// uploadSource streams sourcePath to the agent via PUT. uploadPath is the
// path component returned by createJob (e.g. "/v1/jobs/<id>/source"); we
// strip the protocol prefix if the agent embedded it.
func (c *Client) uploadSource(ctx context.Context, uploadPath, sourcePath string, size int64) error {
	f, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	// uploadPath comes back with PathPrefix already in it (/v1/jobs/…); but
	// newRequest also prepends PathPrefix. Strip to keep the join clean.
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

// watchUntilDone opens the SSE event stream and forwards progress to
// onProgress until the job reaches a terminal state. Returns nil on
// StateDone, an error describing the failure on StateFailed / StateCancelled.
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
			// Frame terminator: dispatch what we accumulated.
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
	// Stream closed without a terminal state: fall back to a single poll
	// so we don't report success on a connection drop.
	js, err := c.poll(ctx, id)
	if err != nil {
		return fmt.Errorf("sse closed; poll fallback: %w", err)
	}
	if js.State == StateDone {
		return nil
	}
	return fmt.Errorf("remote ended in state %s: %s", js.State, js.Error)
}

// handleEvent forwards a progress payload to onProgress. Bad JSON and
// non-progress events are silently ignored — progress is best-effort and
// state transitions are handled in the watch loop.
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

// localTempPath replicates the convention handbrake.Run uses for its sibling
// temp file so encodeOne's existing handbrake.Commit call at job.go works
// unchanged for remote encodes.
func localTempPath(input string, s handbrake.Settings) string {
	dir := filepath.Dir(input)
	base := filepath.Base(input)
	ext := filepath.Ext(base)
	if s.ContainerFormat == "mp4" {
		ext = ".mp4"
	}
	return filepath.Join(dir, "."+base+".recodarr.tmp"+ext)
}

// containerFor reports the output container the agent should ask HandBrake
// for, mirroring the same logic Run uses for the temp file's extension.
func containerFor(sourcePath string, s handbrake.Settings) string {
	if s.ContainerFormat == "mp4" {
		return "mp4"
	}
	if s.ContainerFormat == "mkv" {
		return "mkv"
	}
	// Mirror the local encoder's behavior: keep the source container when
	// unset, defaulting to mkv if the source has no recognized extension.
	switch strings.ToLower(filepath.Ext(sourcePath)) {
	case ".mp4", ".m4v":
		return "mp4"
	default:
		return "mkv"
	}
}

// (Note: the agent runner currently ignores Settings.ContainerFormat='' and
// produces output based on its own logic. Keep containerFor here as the
// authoritative source for the wire request so additions don't drift.)

// silence unused-imports lint if any helpers above get refactored away.
var _ = slog.Info
var _ = errors.New
