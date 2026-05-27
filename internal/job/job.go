package job

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sratabix/recodarr/internal/arr"
	"github.com/sratabix/recodarr/internal/handbrake"
	"github.com/sratabix/recodarr/internal/notify"
	"github.com/sratabix/recodarr/internal/qbit"
	"github.com/sratabix/recodarr/internal/store"
)

type Status string

const (
	StatusWaitingForSeed     Status = "waiting_for_seed"
	StatusWaitingForHardlink Status = "waiting_for_hardlink"
	StatusReady              Status = "ready"
	StatusEncoding           Status = "encoding"
	StatusDone               Status = "done"
	StatusFailed             Status = "failed"
	StatusSkipped            Status = "skipped"
)

type Job struct {
	ID            int64     `json:"id"`
	ArrKind       string    `json:"arrKind"`
	ArrInstanceID int64     `json:"arrInstanceId"`
	ArrItemID     int64     `json:"arrItemId"`
	Title         string    `json:"title"`
	FilePath      string    `json:"filePath"`
	FileSize      int64     `json:"fileSize"`
	DownloadID    string    `json:"downloadId"`
	ProfileID     *int64    `json:"profileId,omitempty"`
	Status        Status    `json:"status"`
	Error         string    `json:"error,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type ProgressEvent struct {
	JobID   int64   `json:"jobId"`
	Title   string  `json:"title"`
	Percent float64 `json:"percent"`
	FPS     float64 `json:"fps"`
	ETA     string  `json:"eta"`
}

// activeEncode is the per-job state held while an encode is in flight. The
// worker holds one of these in `encoding[jobID]` for each running encode.
type activeEncode struct {
	cancel       context.CancelFunc
	title        string
	lastProgress ProgressEvent
}

type Worker struct {
	store       *store.Store
	mu          sync.Mutex
	encoding    map[int64]*activeEncode // jobID → in-flight encode state
	lastTickAt  time.Time
	subscribers map[chan ProgressEvent]struct{}
	// HandbrakeWriterFor, if set, is called per encode to obtain an io.Writer
	// that receives HandBrakeCLI's raw stdout/stderr. Wired by main.go to
	// the logging package's handbrake.log sink. Nil means "discard verbose
	// output", which is fine for tests.
	HandbrakeWriterFor func(jobID int64) io.Writer
	// requeueOnCancel marks job ids whose in-flight encode should be re-queued
	// (back to ready) instead of marked failed when the per-job context is
	// cancelled. Used by the pause path so a paused mid-encode job isn't
	// penalized in the retry budget. Entries are consumed by encodeOne's
	// cancellation branch.
	requeueOnCancel map[int64]struct{}
	// remoteResolver, when set, is called right before each encode to decide
	// whether to route to a remote agent. Resolving at encode-start (rather
	// than relying on a cached binding refreshed by the slow health-checker
	// tick) means the worker reacts immediately to an agent that just came
	// up or went down. Return nil to encode locally.
	remoteResolver RemoteEncoderResolver
}

// RemoteEncoder is the minimal surface area encodeOne needs from a remote
// encode agent client. internal/agent.Client implements it; tests can
// substitute their own.
type RemoteEncoder interface {
	Encode(ctx context.Context, sourcePath string, s handbrake.Settings, onProgress func(handbrake.Progress)) (handbrake.RunResult, error)
}

// RemoteEncoderResolver returns the remote encoder to use for the next encode,
// or nil to encode locally. Called with a short-lived context per encode so
// the implementation can do a live reachability check without holding the
// worker up indefinitely.
type RemoteEncoderResolver func(ctx context.Context) RemoteEncoder

// SetRemoteEncoderResolver swaps the per-encode remote resolver. Pass nil to
// always encode locally. Safe to call from any goroutine; the next encode
// reads the new value.
func (w *Worker) SetRemoteEncoderResolver(r RemoteEncoderResolver) {
	w.mu.Lock()
	w.remoteResolver = r
	w.mu.Unlock()
}

func (w *Worker) getRemoteResolver() RemoteEncoderResolver {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.remoteResolver
}

func NewWorker(s *store.Store) *Worker {
	return &Worker{
		store:           s,
		encoding:        make(map[int64]*activeEncode),
		subscribers:     make(map[chan ProgressEvent]struct{}),
		requeueOnCancel: make(map[int64]struct{}),
	}
}

// Subscribe returns a channel that receives progress events for any
// currently-encoding job. Each event carries its JobID so consumers can
// route them. Caller must invoke the returned cancel to stop receiving.
func (w *Worker) Subscribe() (<-chan ProgressEvent, func()) {
	ch := make(chan ProgressEvent, 16)
	w.mu.Lock()
	w.subscribers[ch] = struct{}{}
	// Replay the latest known progress for each in-flight job so a fresh
	// subscriber doesn't have to wait for the next progress tick to see state.
	snapshots := make([]ProgressEvent, 0, len(w.encoding))
	for _, ae := range w.encoding {
		if ae.lastProgress.JobID != 0 {
			snapshots = append(snapshots, ae.lastProgress)
		}
	}
	w.mu.Unlock()
	for _, ev := range snapshots {
		select {
		case ch <- ev:
		default:
		}
	}
	return ch, func() {
		w.mu.Lock()
		if _, ok := w.subscribers[ch]; ok {
			delete(w.subscribers, ch)
			close(ch)
		}
		w.mu.Unlock()
	}
}

// CurrentProgress returns one in-flight job's most recent progress event
// (the one with the lowest job id, deterministically). Returns zero-value
// when nothing is encoding. Kept for back-compat with the single-job UI.
// Use AllProgress for full multi-job state.
func (w *Worker) CurrentProgress() ProgressEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	ids := make([]int64, 0, len(w.encoding))
	for id := range w.encoding {
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return ProgressEvent{}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return w.encoding[ids[0]].lastProgress
}

// AllProgress returns the latest progress event for every in-flight encode.
func (w *Worker) AllProgress() []ProgressEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]ProgressEvent, 0, len(w.encoding))
	for _, ae := range w.encoding {
		if ae.lastProgress.JobID != 0 {
			out = append(out, ae.lastProgress)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].JobID < out[j].JobID })
	return out
}

func (w *Worker) broadcast(ev ProgressEvent) {
	w.mu.Lock()
	if ae, ok := w.encoding[ev.JobID]; ok {
		ae.lastProgress = ev
	}
	subs := make([]chan ProgressEvent, 0, len(w.subscribers))
	for ch := range w.subscribers {
		subs = append(subs, ch)
	}
	w.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- ev:
		default:
			// slow subscriber — drop the event rather than blocking the encode loop
		}
	}
}

// CancelEncoding cancels the running encode for jobID.
// Returns true if the job was active and cancellation was sent.
func (w *Worker) CancelEncoding(jobID int64) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	ae, ok := w.encoding[jobID]
	if !ok {
		return false
	}
	ae.cancel()
	return true
}

// EncodingJobID returns the lowest in-flight job id, or 0 if none.
// Kept as the legacy single-job accessor; use EncodingJobIDs for the full set.
func (w *Worker) EncodingJobID() int64 {
	ids := w.EncodingJobIDs()
	if len(ids) == 0 {
		return 0
	}
	return ids[0]
}

// EncodingJobIDs returns the ids of every in-flight encode, sorted ascending.
func (w *Worker) EncodingJobIDs() []int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	ids := make([]int64, 0, len(w.encoding))
	for id := range w.encoding {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// LastTickAt returns the time of the most recent worker tick (zero if not yet ticked).
func (w *Worker) LastTickAt() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastTickAt
}

// WindowStatus reports the encoding window configuration and whether the worker
// is currently inside it. UI surfaces this so a paused worker isn't a mystery.
type WindowStatus struct {
	Start    string `json:"start"`
	End      string `json:"end"`
	Active   bool   `json:"active"`
	HasLimit bool   `json:"hasLimit"`
}

// SetPaused persists the master encoding-paused flag and, when transitioning
// to paused, immediately cancels every in-flight encode and re-queues them
// (status encoding → ready, attempts decremented). The settings PUT handler
// also writes encoding_paused, but it goes through this method when it wants
// the cancellation side-effect.
//
// Returns the number of encodes that were cancelled by this call (0 if the
// flag didn't change to paused, or no encodes were running).
func (w *Worker) SetPaused(ctx context.Context, paused bool) (int, error) {
	val := "false"
	if paused {
		val = "true"
	}
	if err := w.store.SetSetting(ctx, "encoding_paused", val); err != nil {
		return 0, err
	}
	if !paused {
		return 0, nil
	}

	w.mu.Lock()
	ids := make([]int64, 0, len(w.encoding))
	for id, ae := range w.encoding {
		w.requeueOnCancel[id] = struct{}{}
		ae.cancel()
		ids = append(ids, id)
	}
	w.mu.Unlock()

	if len(ids) > 0 {
		slog.Info("encoding paused; cancelled in-flight encodes", "count", len(ids), "ids", ids)
	}
	return len(ids), nil
}

// IsPaused reports the current value of the encoding_paused setting. Cheap
// (single SQLite read); the worker also re-checks this on each tick.
func (w *Worker) IsPaused(ctx context.Context) bool {
	cfg, _ := w.store.LoadAppSettings(ctx)
	return cfg.EncodingPaused
}

func (w *Worker) WindowStatus(ctx context.Context) WindowStatus {
	cfg, _ := w.store.LoadAppSettings(ctx)
	if cfg.EncodingWindowStart == "" || cfg.EncodingWindowEnd == "" {
		return WindowStatus{Active: true}
	}
	return WindowStatus{
		Start:    cfg.EncodingWindowStart,
		End:      cfg.EncodingWindowEnd,
		Active:   w.inEncodingWindow(ctx),
		HasLimit: true,
	}
}

func (w *Worker) Run(ctx context.Context) {
	slog.Info("worker started")
	w.tick(ctx)
	for {
		interval := w.readInterval(ctx)
		select {
		case <-ctx.Done():
			slog.Info("worker stopped")
			return
		case <-time.After(interval):
			w.tick(ctx)
		}
	}
}

func (w *Worker) readInterval(ctx context.Context) time.Duration {
	cfg, _ := w.store.LoadAppSettings(ctx)
	return time.Duration(cfg.WorkerIntervalSeconds) * time.Second
}

func (w *Worker) inEncodingWindow(ctx context.Context) bool {
	cfg, _ := w.store.LoadAppSettings(ctx)
	if cfg.EncodingWindowStart == "" || cfg.EncodingWindowEnd == "" {
		return true
	}
	now := time.Now()
	startH, startM := parseHHMM(cfg.EncodingWindowStart)
	endH, endM := parseHHMM(cfg.EncodingWindowEnd)
	startMins := startH*60 + startM
	endMins := endH*60 + endM
	nowMins := now.Hour()*60 + now.Minute()
	if startMins <= endMins {
		return nowMins >= startMins && nowMins < endMins
	}
	// overnight window e.g. 22:00–06:00
	return nowMins >= startMins || nowMins < endMins
}

func parseHHMM(s string) (int, int) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, 0
	}
	h, _ := strconv.Atoi(parts[0])
	m, _ := strconv.Atoi(parts[1])
	return h, m
}

func (w *Worker) tick(ctx context.Context) {
	w.mu.Lock()
	w.lastTickAt = time.Now()
	w.mu.Unlock()
	w.checkSeeding(ctx)
	w.checkHardlinks(ctx)
	w.runEncodes(ctx)
}

// SeedCheckBatchLimit caps how many waiting_for_seed jobs are inspected per
// tick. Set high enough that real-world queues clear in one pass; the bulk
// qBit call is a single HTTP roundtrip regardless of size, so this is mostly
// a safety valve against pathological queue depths.
const SeedCheckBatchLimit = 5000

// checkSeeding moves jobs from waiting_for_seed → ready when qBit no longer holds the torrent.
func (w *Worker) checkSeeding(ctx context.Context) {
	jobs, err := w.store.JobsByStatus(ctx, string(StatusWaitingForSeed), SeedCheckBatchLimit)
	if err != nil {
		slog.Error("checkSeeding list", "err", err)
		return
	}
	if len(jobs) == 0 {
		return
	}

	// Jobs with no downloadId can transition immediately — no qBit call needed.
	hashes := make([]string, 0, len(jobs))
	hashJobs := make(map[string][]int64, len(jobs))
	for _, j := range jobs {
		if j.DownloadID == "" {
			w.transition(ctx, j.ID, string(StatusWaitingForSeed), "no downloadId, skipping seed check")
			continue
		}
		h := strings.ToLower(j.DownloadID)
		if _, seen := hashJobs[h]; !seen {
			hashes = append(hashes, h)
		}
		hashJobs[h] = append(hashJobs[h], j.ID)
	}
	if len(hashes) == 0 {
		return
	}

	qbitRow, err := w.store.FirstQbitInstance(ctx)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			slog.Warn("qbit not configured; jobs remain waiting", "count", len(hashes))
			return
		}
		slog.Error("checkSeeding qbit lookup", "err", err)
		return
	}

	client, err := qbit.New(qbitRow.URL, qbitRow.Username, qbitRow.Password)
	if err != nil {
		slog.Error("qbit client", "err", err)
		return
	}
	if err := client.Login(ctx); err != nil {
		slog.Error("qbit login", "err", err)
		return
	}

	got, err := client.TorrentsByHashes(ctx, hashes)
	if err != nil {
		slog.Warn("qbit torrents lookup", "count", len(hashes), "err", err)
		return
	}
	for hash, ids := range hashJobs {
		if _, present := got[hash]; present {
			continue
		}
		for _, id := range ids {
			w.transition(ctx, id, string(StatusWaitingForSeed), "qbit no longer holds torrent")
		}
	}
}

// checkHardlinks moves library-backfill jobs from waiting_for_hardlink → ready
// once the library file has no remaining hardlinks elsewhere. Backfill jobs
// carry no downloadId (the *arr library API doesn't expose the qBit hash), so
// they can't be seed-checked against qBit. Instead we lean on the project's
// documented setup — qBit auto-remove + *arr hardlink imports — where the
// library file and qBit's download copy share an inode. While qBit still seeds,
// st_nlink > 1; once qBit removes the torrent, nlink drops to 1 and the encode
// is safe. The check fails OPEN on stat errors (missing file, copy-mode imports
// on a different filesystem where nlink is always 1): we'd rather encode than
// wedge a job forever, and a missing file surfaces clearly as an encode failure.
func (w *Worker) checkHardlinks(ctx context.Context) {
	jobs, err := w.store.JobsByStatus(ctx, string(StatusWaitingForHardlink), SeedCheckBatchLimit)
	if err != nil {
		slog.Error("checkHardlinks list", "err", err)
		return
	}
	for _, j := range jobs {
		n, err := HardlinkCount(j.FilePath)
		if err != nil {
			slog.Warn("checkHardlinks stat failed; releasing job to ready", "id", j.ID, "path", j.FilePath, "err", err)
			w.transition(ctx, j.ID, string(StatusWaitingForHardlink), "stat failed, cannot detect seeding")
			continue
		}
		if n > 1 {
			slog.Debug("library file still has extra hardlinks, likely seeding", "id", j.ID, "path", j.FilePath, "links", n)
			continue
		}
		w.transition(ctx, j.ID, string(StatusWaitingForHardlink), "no remaining hardlinks, torrent gone")
	}
}

// HardlinkCount returns the number of hardlinks to the file at path (st_nlink).
// The syscall.Stat_t.Nlink field width differs by GOOS (uint16 on darwin,
// uint64 on linux); returning int64 makes the conversion real on both (so the
// unconvert linter stays quiet on the linux build) and matches how counts are
// typed elsewhere in the codebase.
func HardlinkCount(path string) (int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("hardlink count unavailable on this platform")
	}
	return int64(st.Nlink), nil
}

// reresolveProfile returns the profile_id that the current tag→profile
// mappings produce for this job, plus a flag indicating whether that differs
// from the job's stored profile_id. Jobs whose tags column is empty (the JSON
// "[]" default, or an unmigrated row) are left alone — returning changed=false
// preserves whatever was frozen at webhook time. A successful re-resolve to
// "no match" returns (invalid NullInt64, true) so the worker can fail the job
// with a clear "no mapping matches current tags" message.
func (w *Worker) reresolveProfile(ctx context.Context, j store.JobRow) (sql.NullInt64, bool) {
	if j.Tags == "" || j.Tags == "[]" {
		return j.ProfileID, false
	}
	var tags []string
	if err := json.Unmarshal([]byte(j.Tags), &tags); err != nil || len(tags) == 0 {
		return j.ProfileID, false
	}
	mappings, err := w.store.ListTagMappingsByKind(ctx, j.ArrKind)
	if err != nil {
		return j.ProfileID, false
	}
	idx := make(map[string]int64, len(mappings))
	for _, m := range mappings {
		idx[m.TagLabel] = m.ProfileID
	}
	var resolved sql.NullInt64
	for _, t := range tags {
		if pid, ok := idx[t]; ok {
			resolved = sql.NullInt64{Int64: pid, Valid: true}
			break
		}
	}
	if resolved.Valid == j.ProfileID.Valid && resolved.Int64 == j.ProfileID.Int64 {
		return j.ProfileID, false
	}
	return resolved, true
}

// transition advances a job from a given status to 'ready'. Every worker-driven
// status change in this file moves a queued job forward into the encode queue,
// so the destination is fixed; only the source status varies.
func (w *Worker) transition(ctx context.Context, id int64, from, reason string) {
	to := string(StatusReady)
	ok, err := w.store.TransitionJobStatus(ctx, id, from, to)
	if err != nil {
		slog.Error("transition", "id", id, "err", err)
		return
	}
	if ok {
		slog.Debug("job transitioned", "id", id, "from", from, "to", to, "reason", reason)
	}
}

// runEncodes claims as many ready jobs as the configured concurrency allows and
// launches each in its own goroutine. It does NOT wait for them — they run
// independently and free their slot on completion.
func (w *Worker) runEncodes(ctx context.Context) {
	cfg, _ := w.store.LoadAppSettings(ctx)
	if cfg.EncodingPaused {
		slog.Debug("encoding paused, skipping encode")
		return
	}
	if !w.inEncodingWindow(ctx) {
		slog.Debug("outside encoding window, skipping encode")
		return
	}
	maxParallel := cfg.MaxParallelEncodes
	if maxParallel < 1 {
		maxParallel = 1
	}

	w.mu.Lock()
	slots := maxParallel - len(w.encoding)
	w.mu.Unlock()
	if slots <= 0 {
		return
	}

	jobs, err := w.store.JobsByStatus(ctx, string(StatusReady), slots)
	if err != nil {
		slog.Error("runEncodes list", "err", err)
		return
	}
	for _, j := range jobs {
		claimed, err := w.store.MarkJobEncoding(ctx, j.ID)
		if err != nil {
			slog.Error("claim job", "id", j.ID, "err", err)
			continue
		}
		if !claimed {
			// Either we raced (shouldn't happen — only this goroutine claims) or
			// attempts cap was hit; auto-fail in the latter case so the queue moves.
			if j.Attempts >= store.MaxJobAttempts {
				_ = w.store.MarkJobFailed(ctx, j.ID,
					fmt.Sprintf("gave up after %d attempts", j.Attempts), "")
				slog.Warn("job exceeded max attempts", "id", j.ID, "attempts", j.Attempts)
			}
			continue
		}
		w.startEncode(ctx, j)
	}
}

// startEncode registers the job in the encoding map and launches the encode
// goroutine. Returns immediately.
func (w *Worker) startEncode(parentCtx context.Context, j store.JobRow) {
	encCtx, cancel := context.WithCancel(parentCtx)
	ae := &activeEncode{
		cancel:       cancel,
		title:        j.Title,
		lastProgress: ProgressEvent{JobID: j.ID, Title: j.Title},
	}
	w.mu.Lock()
	w.encoding[j.ID] = ae
	w.mu.Unlock()

	go func() {
		defer func() {
			cancel()
			w.mu.Lock()
			delete(w.encoding, j.ID)
			w.mu.Unlock()
			// Signal subscribers that this job's slot is free with a zero-percent
			// event tagged with the JobID so the UI can clean up its row.
			w.broadcast(ProgressEvent{JobID: j.ID, Title: j.Title})
		}()
		w.encodeOne(encCtx, parentCtx, j)
	}()
}

// encodeOne runs HandBrake on a single claimed job. encCtx is cancellable per-job
// (used to terminate the running HandBrakeCLI process); parentCtx is the worker's
// long-lived context used for DB writes that must complete even if the per-job
// context was cancelled.
func (w *Worker) encodeOne(encCtx, parentCtx context.Context, j store.JobRow) {
	// Re-resolve profile from the job's stored tags + current mappings, so
	// edits to tag→profile mappings take effect on already-queued jobs. Jobs
	// with no stored tags (i.e. created before this column existed) fall
	// through to whatever profile_id was frozen at webhook time.
	if newPID, changed := w.reresolveProfile(parentCtx, j); changed {
		if err := w.store.UpdateJobProfile(parentCtx, j.ID, newPID); err != nil {
			slog.Warn("re-resolve profile update", "id", j.ID, "err", err)
		} else {
			j.ProfileID = newPID
			if newPID.Valid {
				slog.Info("job profile re-resolved", "id", j.ID, "profile_id", newPID.Int64)
			} else {
				slog.Info("job profile cleared by current mappings", "id", j.ID)
			}
		}
	}

	if !j.ProfileID.Valid {
		_ = w.store.MarkJobFailed(parentCtx, j.ID, "no profile assigned (tag-profile mapping missing)", "")
		slog.Error("job missing profile", "id", j.ID)
		return
	}
	profile, err := w.store.GetProfile(parentCtx, j.ProfileID.Int64)
	if err != nil {
		_ = w.store.MarkJobFailed(parentCtx, j.ID, "profile lookup: "+err.Error(), "")
		return
	}

	// Pre-encode filters (per-profile). Run before disk-space because filtering
	// is the cheapest way to reject a job — no point checking disk space for a
	// file we're about to skip.
	if skip, reason := evaluateFilters(parentCtx, profile, j); skip {
		if err := w.store.MarkJobSkipped(parentCtx, j.ID, reason); err != nil {
			slog.Error("mark skipped", "id", j.ID, "err", err)
		}
		slog.Info("job skipped by filter", "id", j.ID, "title", j.Title, "reason", reason)
		return
	}

	if err := checkDiskSpace(j.FilePath, j.FileSize); err != nil {
		_ = w.store.MarkJobFailed(parentCtx, j.ID, err.Error(), "")
		slog.Error("disk space check failed", "id", j.ID, "err", err)
		return
	}

	rateAttr := slog.Int("quality", profile.Quality)
	rateLabel := "crf"
	if strings.EqualFold(profile.RateControl, "abr") {
		rateAttr = slog.Int("bitrate_kbps", profile.VideoBitrate)
		rateLabel = "abr"
	}
	slog.Info("encoding",
		"id", j.ID, "title", j.Title, "path", j.FilePath,
		"encoder", profile.Encoder, "rate_control", rateLabel, rateAttr,
		"profile_id", j.ProfileID.Int64, "profile_name", profile.Name)
	onProgress := func(p handbrake.Progress) {
		w.broadcast(ProgressEvent{JobID: j.ID, Title: j.Title, Percent: p.Percent, FPS: p.FPS, ETA: p.ETA})
	}
	var hbSink *handbrake.LineSink
	if w.HandbrakeWriterFor != nil {
		out := w.HandbrakeWriterFor(j.ID)
		hbSink = &handbrake.LineSink{Stdout: out, Stderr: out}
	}
	cfg, _ := w.store.LoadAppSettings(parentCtx)

	// Size-guard policy decides whether to defer the file commit. When 'off'
	// (default), handbrake.Run renames over the source as soon as the encode
	// succeeds — same behavior as before this feature existed. Otherwise we
	// run with NoCommit so we can compare new vs. original and decide whether
	// to keep the encode, retry, or discard it.
	guard := profile.BloatPolicy
	if guard != "keep_original" && guard != "retry_higher_crf" {
		guard = "off"
	}
	noCommit := guard != "off"

	// Track retries (only meaningful for retry_higher_crf). currentQuality
	// starts at the profile's RF and bumps by BloatRetryStep on each retry.
	// In ABR mode the same step instead reduces VideoBitrate (kbps).
	currentQuality := profile.Quality
	currentBitrate := profile.VideoBitrate
	isABR := strings.EqualFold(profile.RateControl, "abr")
	maxRetries := 0
	step := 0
	if guard == "retry_higher_crf" {
		maxRetries = profile.BloatRetryMax
		step = profile.BloatRetryStep
		if step <= 0 {
			if isABR {
				step = 200 // kbps
			} else {
				step = 3 // CRF units
			}
		}
	}
	combinedLog := strings.Builder{}
	attempt := 0

	var lastResult handbrake.RunResult
	var lastErr error
	for {
		attempt++
		if attempt > 1 {
			if isABR {
				fmt.Fprintf(&combinedLog, "\n--- retry %d (ABR %d kbps) ---\n", attempt-1, currentBitrate)
				slog.Debug("size-guard retry", "id", j.ID, "attempt", attempt, "bitrate", currentBitrate)
			} else {
				fmt.Fprintf(&combinedLog, "\n--- retry %d (CRF %d) ---\n", attempt-1, currentQuality)
				slog.Debug("size-guard retry", "id", j.ID, "attempt", attempt, "quality", currentQuality)
			}
		}
		settings := handbrake.Settings{
			Encoder:         profile.Encoder,
			EncoderPreset:   profile.EncoderPreset,
			EncoderProfile:  profile.EncoderProfile,
			EncoderTune:     profile.EncoderTune,
			EncoderLevel:    profile.EncoderLevel,
			RateControl:     profile.RateControl,
			Quality:         currentQuality,
			VideoBitrate:    currentBitrate,
			MaxWidth:        profile.MaxWidth,
			MaxHeight:       profile.MaxHeight,
			AudioEncoder:    profile.AudioEncoder,
			AudioBitrate:    profile.AudioBitrate,
			AudioMixdown:    profile.AudioMixdown,
			SubtitleCopy:    profile.SubtitleCopy,
			TwoPass:         profile.TwoPass,
			ContainerFormat: profile.ContainerFormat,
			ExtraArgs:       profile.ExtraArgs,
			Framerate:       profile.Framerate,
			NoCommit:        noCommit,
		}
		// Branch local vs. remote. The remote agent always returns the
		// encoded bytes in a sibling temp file (mirroring NoCommit=true), so
		// the size-guard branch below using lastResult.TempPath works
		// identically regardless of where the encode ran. The handbrake log
		// for failures is also returned via RunResult.Log, so the dashboard's
		// failure UI doesn't have to branch either.
		var remote RemoteEncoder
		if resolver := w.getRemoteResolver(); resolver != nil {
			rctx, rcancel := context.WithTimeout(encCtx, 8*time.Second)
			remote = resolver(rctx)
			rcancel()
		}
		if remote != nil {
			// Remote always behaves as NoCommit — the worker performs the
			// Commit locally after download.
			settings.NoCommit = true
			lastResult, lastErr = remote.Encode(encCtx, j.FilePath, settings, onProgress)
		} else {
			lastResult, lastErr = handbrake.Run(encCtx, j.FilePath, settings, hbSink, onProgress)
		}
		combinedLog.WriteString(lastResult.Log)

		if lastErr != nil {
			break
		}
		if guard == "off" {
			// Local handbrake.Run commits the rename itself. The remote
			// encoder always leaves the result at TempPath (regardless of
			// settings.NoCommit) so we have to commit here.
			if lastResult.TempPath != "" {
				if err := handbrake.Commit(lastResult.TempPath, j.FilePath); err != nil {
					lastErr = err
				}
			}
			break
		}

		// Size guard: compare new vs. original and decide.
		threshold := j.FileSize
		if profile.BloatMinSavingsPercent > 0 && j.FileSize > 0 {
			// Required final size to count as "not bloated":
			//   final ≤ original × (1 − savings/100)
			threshold = j.FileSize - (j.FileSize*int64(profile.BloatMinSavingsPercent))/100
		}
		if lastResult.FinalSize <= threshold {
			// Acceptable size — commit and move on.
			if err := handbrake.Commit(lastResult.TempPath, j.FilePath); err != nil {
				lastErr = err
				break
			}
			break
		}

		// New file is at-or-larger than the threshold. Always discard the
		// uncommitted temp.
		handbrake.DiscardTemp(lastResult.TempPath)

		if guard == "retry_higher_crf" && attempt <= maxRetries {
			if isABR {
				currentBitrate -= step
				if currentBitrate < 200 {
					currentBitrate = 200 // floor; lower than this almost never decodes cleanly
				}
			} else {
				currentQuality += step
			}
			continue
		}

		// Either policy is keep_original, or retries exhausted — keep the
		// source file (already untouched on disk) and mark the job as
		// skipped with an explanatory reason. Not a failure: encode worked,
		// we just chose not to keep it.
		reason := fmt.Sprintf(
			"encode produced larger file (%s vs original %s) — kept original",
			formatBytes(lastResult.FinalSize), formatBytes(j.FileSize),
		)
		if guard == "retry_higher_crf" {
			reason = fmt.Sprintf(
				"encode produced larger file after %d retries (final %s vs original %s) — kept original",
				attempt-1, formatBytes(lastResult.FinalSize), formatBytes(j.FileSize),
			)
		}
		if err := w.store.MarkJobSkipped(parentCtx, j.ID, reason); err != nil {
			slog.Error("mark skipped (size guard)", "id", j.ID, "err", err)
		}
		slog.Info("size guard kept original", "id", j.ID, "title", j.Title,
			"original", j.FileSize, "encoded", lastResult.FinalSize, "attempts", attempt)
		go notify.Send(context.Background(), w.store, j.Title, "skipped", j.FilePath, j.FileSize, 0)
		return
	}

	if lastErr != nil {
		msg := lastErr.Error()
		if encCtx.Err() != nil {
			msg = "cancelled"
		}
		// Pause-initiated cancellations (and any other "external" cancel that
		// pre-marked the job in requeueOnCancel) put the job back in the queue
		// rather than failing it. The cancel wasn't its fault, and we want it
		// to run later when encoding resumes. attempts is rolled back inside
		// RequeueEncoding so this doesn't eat into the MaxJobAttempts budget.
		w.mu.Lock()
		_, requeue := w.requeueOnCancel[j.ID]
		delete(w.requeueOnCancel, j.ID)
		w.mu.Unlock()
		if requeue && encCtx.Err() != nil {
			if err := w.store.RequeueEncoding(parentCtx, j.ID); err != nil {
				slog.Error("requeue after pause-cancel failed", "id", j.ID, "err", err)
			} else {
				slog.Debug("requeued after pause-cancel", "id", j.ID)
			}
			return
		}
		// Also discard any leftover temp from a guarded run that errored.
		if lastResult.TempPath != "" {
			handbrake.DiscardTemp(lastResult.TempPath)
		}
		encLog := truncateLog(combinedLog.String(), 200)
		_ = w.store.MarkJobFailed(parentCtx, j.ID, msg, encLog)
		slog.Error("encode failed", "id", j.ID, "err", msg)
		go notify.Send(context.Background(), w.store, j.Title, "failed", j.FilePath, j.FileSize, 0)
		return
	}

	if err := w.store.MarkJobDone(parentCtx, j.ID, lastResult.FinalSize); err != nil {
		slog.Error("mark done", "id", j.ID, "err", err)
		return
	}
	// Drop a sidecar marker next to the encoded file so future webhook deliveries
	// (replays, *arr re-imports, manual re-runs) can detect the file's already
	// been processed without consulting the DB. Failure here doesn't fail the
	// encode — the marker is an optimization, not a contract.
	if cfg.OutputSuffixEnabled {
		if err := writeSidecar(j.FilePath, cfg.OutputSuffix, j, profile, lastResult.FinalSize); err != nil {
			slog.Warn("sidecar write failed", "id", j.ID, "err", err)
		}
	}
	slog.Info("encode done", "id", j.ID, "path", j.FilePath, "originalSize", j.FileSize, "finalSize", lastResult.FinalSize, "attempts", attempt)
	go notify.Send(context.Background(), w.store, j.Title, "done", j.FilePath, j.FileSize, lastResult.FinalSize)

	w.refreshArr(parentCtx, j)
}

// sidecarPath returns the marker path Recodarr writes next to an encoded file
// when the OutputSuffix feature is enabled. Same stem, different extension
// (the suffix becomes the extension). For `.../Movie (2024).mkv` with suffix
// `recodarr`, this returns `.../Movie (2024).recodarr`.
func sidecarPath(mediaPath, suffix string) string {
	dir := filepath.Dir(mediaPath)
	base := filepath.Base(mediaPath)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	return filepath.Join(dir, stem+"."+suffix)
}

// writeSidecar emits a small human-readable marker file recording what Recodarr
// did to this media file. Format is plain key=value lines so a user (or a
// script) can grep it without parsing JSON. The presence of the file is the
// load-bearing part — the contents are informational.
func writeSidecar(mediaPath, suffix string, j store.JobRow, p *store.ProfileRow, finalSize int64) error {
	path := sidecarPath(mediaPath, suffix)
	var b strings.Builder
	fmt.Fprintf(&b, "# Recodarr re-encode marker — do not delete unless you want this file re-encoded.\n")
	fmt.Fprintf(&b, "encoded_at=%s\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "job_id=%d\n", j.ID)
	fmt.Fprintf(&b, "title=%s\n", j.Title)
	fmt.Fprintf(&b, "profile=%s\n", p.Name)
	fmt.Fprintf(&b, "encoder=%s\n", p.Encoder)
	if p.EncoderPreset != "" {
		fmt.Fprintf(&b, "preset=%s\n", p.EncoderPreset)
	}
	if p.EncoderTune != "" {
		fmt.Fprintf(&b, "tune=%s\n", p.EncoderTune)
	}
	if p.EncoderProfile != "" {
		fmt.Fprintf(&b, "encoder_profile=%s\n", p.EncoderProfile)
	}
	if strings.EqualFold(p.RateControl, "abr") {
		fmt.Fprintf(&b, "rate_control=abr\nvideo_bitrate_kbps=%d\n", p.VideoBitrate)
	} else {
		fmt.Fprintf(&b, "rate_control=crf\nquality=%d\n", p.Quality)
	}
	fmt.Fprintf(&b, "container=%s\n", p.ContainerFormat)
	fmt.Fprintf(&b, "original_size=%d\n", j.FileSize)
	fmt.Fprintf(&b, "final_size=%d\n", finalSize)
	if j.FileSize > 0 {
		pct := float64(j.FileSize-finalSize) / float64(j.FileSize) * 100
		fmt.Fprintf(&b, "saved_percent=%.1f\n", pct)
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// checkDiskSpace verifies the directory containing path has at least 1.1× needed bytes free.
func checkDiskSpace(path string, needed int64) error {
	var stat syscall.Statfs_t
	// Can't stat the FS (rare). Don't block the encode on it; HandBrake will
	// fail clearly if the disk really is too small.
	if err := syscall.Statfs(filepath.Dir(path), &stat); err != nil { //nolint:nilerr // intentional: best-effort precheck
		slog.Debug("statfs failed, skipping disk-space check", "path", path, "err", err)
		return nil
	}
	available := int64(stat.Bavail) * int64(stat.Bsize) //nolint:unconvert // Bavail/Bsize types differ across GOOS
	required := needed + needed/10                      // 110% of source size
	if available < required {
		return fmt.Errorf("insufficient disk space: need %s, have %s",
			formatBytes(required), formatBytes(available))
	}
	return nil
}

func formatBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	v := float64(n) / 1024
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

// maxEncodeLogBytes caps the encode_log column to keep pathological HandBrake output
// (e.g. binary garbage, megabyte-long single lines) from bloating the SQLite DB.
const maxEncodeLogBytes = 64 * 1024

// truncateLog keeps at most maxLines lines from the tail of s, then enforces a hard byte
// cap so a single huge line can't blow past the limit.
func truncateLog(s string, maxLines int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	out := strings.Join(lines, "\n")
	if len(out) > maxEncodeLogBytes {
		// Keep the tail (most recent output is usually the most useful for diagnosis).
		out = "…[truncated]\n" + out[len(out)-maxEncodeLogBytes:]
	}
	return out
}

// refreshArr asks Sonarr/Radarr to rescan so it picks up the new file.
func (w *Worker) refreshArr(ctx context.Context, j store.JobRow) {
	if j.ArrParentID == 0 {
		return
	}
	inst, err := w.store.GetArrInstance(ctx, j.ArrInstanceID)
	if err != nil {
		slog.Warn("refreshArr: instance lookup", "id", j.ArrInstanceID, "err", err)
		return
	}
	if j.ArrKind != "sonarr" && j.ArrKind != "radarr" {
		return
	}
	if err := arr.New(arr.Kind(j.ArrKind), inst.URL, inst.APIKey).Refresh(ctx, j.ArrParentID); err != nil {
		slog.Warn("arr refresh", "kind", j.ArrKind, "jobId", j.ID, "err", err)
		_ = w.store.SetRefreshError(ctx, j.ID, err.Error())
		return
	}
	_ = w.store.SetRefreshError(ctx, j.ID, "")
}
