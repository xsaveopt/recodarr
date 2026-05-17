package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sratabix/recodarr/internal/handbrake"
)

// terminalTTL is how long a finished job's on-disk directory hangs around
// before the runner auto-deletes it. Gives the server-side client time to
// fetch the output even if Recodarr's polling stalls briefly.
const terminalTTL = 1 * time.Hour

// tickInterval is how often the runner looks for queued jobs to start.
const tickInterval = 1 * time.Second

// Runner drives the encode lifecycle on the agent. Single goroutine + ticker:
// claims queued jobs (up to MaxParallel), executes handbrake.Run, fans
// progress + state events out to SSE subscribers. The server package consumes
// those events; the runner never touches HTTP itself.
type Runner struct {
	store       *Store
	maxParallel int

	mu       sync.Mutex
	active   map[string]context.CancelFunc // jobID → cancel for the in-flight encode
	subs     map[string][]chan Event       // jobID → live subscriber channels
	subIndex map[chan Event]string         // reverse: chan → jobID, for unsubscribe
}

// Event is what subscribers receive over their SSE channel. Exactly one of
// Progress / State is non-nil.
type Event struct {
	Progress *ProgressPayload
	State    *StatePayload
}

// NewRunner constructs the runner. handbrakeWriter is currently unused — the
// agent writes a per-job log file directly so DELETE /jobs/{id} fully cleans
// up — but kept in the signature so the cmd entrypoint can pass the same
// constructor it would in server mode without thinking about it.
func NewRunner(store *Store, maxParallel int, _ func(jobID int64) io.Writer) *Runner {
	if maxParallel < 1 {
		maxParallel = 1
	}
	return &Runner{
		store:       store,
		maxParallel: maxParallel,
		active:      map[string]context.CancelFunc{},
		subs:        map[string][]chan Event{},
		subIndex:    map[chan Event]string{},
	}
}

// Run drives the claim loop until ctx is cancelled. Call once from main; safe
// to leave running for the process lifetime.
func (r *Runner) Run(ctx context.Context) {
	slog.Info("agent runner started", "maxParallel", r.maxParallel)
	t := time.NewTicker(tickInterval)
	defer t.Stop()
	ttl := time.NewTicker(5 * time.Minute)
	defer ttl.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("agent runner stopped")
			return
		case <-ttl.C:
			r.cleanupTerminal()
		case <-t.C:
			r.tick(ctx)
		}
	}
}

func (r *Runner) tick(ctx context.Context) {
	// Cap concurrent encodes. HandBrake hwaccel doesn't parallelize on
	// consumer GPUs, so the default is 1; operators with workstations can
	// raise it via RECODARR_AGENT_MAX_PARALLEL.
	r.mu.Lock()
	free := r.maxParallel - len(r.active)
	r.mu.Unlock()
	for ; free > 0; free-- {
		js, ok := r.store.ClaimQueued()
		if !ok {
			return
		}
		go r.encode(ctx, js.ID)
	}
}

func (r *Runner) encode(parent context.Context, id string) {
	js, ok := r.store.Get(id)
	if !ok {
		return
	}
	// Per-encode cancellable context so DELETE /v1/jobs/{id} (or process
	// shutdown) can interrupt a running HandBrake. The store transitioned
	// us into StateEncoding atomically inside ClaimQueued.
	ctx, cancel := context.WithCancel(parent)
	r.mu.Lock()
	r.active[id] = cancel
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.active, id)
		r.mu.Unlock()
		cancel()
	}()

	r.publish(id, Event{State: &StatePayload{State: StateEncoding}})

	logFile, err := os.OpenFile(r.store.LogPath(js), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		r.finish(id, StateFailed, fmt.Errorf("open log: %w", err), 0)
		return
	}
	defer func() { _ = logFile.Close() }()

	// Force NoCommit so Run leaves the encoded result at its temp path
	// instead of trying to rename it over the source. We rename to the
	// agent's deterministic OutputPath ourselves below.
	settings := js.Request.Settings
	settings.NoCommit = true
	// Log what we actually received so the operator can confirm rate control
	// arrived intact (the most common drift between server and agent is a
	// mismatched binary version that silently strips unknown fields).
	rateLabel := "crf"
	rateVal := settings.Quality
	if strings.EqualFold(settings.RateControl, "abr") {
		rateLabel = "abr"
		rateVal = settings.VideoBitrate
	}
	slog.Info("agent: encode start",
		"id", id, "encoder", settings.Encoder,
		"rate_control", rateLabel, "rate_value", rateVal)

	sink := &handbrake.LineSink{Stdout: logFile, Stderr: logFile}
	onProgress := func(p handbrake.Progress) {
		_ = r.store.Update(id, func(js *JobStateSnapshot) error {
			js.Progress = ProgressPayload{Percent: p.Percent, FPS: p.FPS, ETA: p.ETA}
			return nil
		})
		r.publish(id, Event{Progress: &ProgressPayload{Percent: p.Percent, FPS: p.FPS, ETA: p.ETA}})
	}

	res, err := handbrake.Run(ctx, r.store.SourcePath(js), settings, sink, onProgress)
	if err != nil {
		// Distinguish operator-initiated cancellation from a crash so the
		// subscriber sees the right terminal label.
		if errors.Is(ctx.Err(), context.Canceled) {
			r.finish(id, StateCancelled, errors.New("cancelled"), 0)
			return
		}
		r.finish(id, StateFailed, err, 0)
		return
	}
	// Rename Run's sibling temp file to our deterministic output path so the
	// download handler doesn't have to know about HandBrake's naming.
	if err := os.Rename(res.TempPath, r.store.OutputPath(js)); err != nil {
		_ = os.Remove(res.TempPath)
		r.finish(id, StateFailed, fmt.Errorf("publish output: %w", err), 0)
		return
	}
	r.finish(id, StateDone, nil, res.FinalSize)
}

func (r *Runner) finish(id string, terminal State, encErr error, finalSize int64) {
	now := time.Now()
	msg := ""
	if encErr != nil {
		msg = encErr.Error()
	}
	if err := r.store.Update(id, func(js *JobStateSnapshot) error {
		js.State = terminal
		js.FinishedAt = &now
		js.Error = msg
		js.OutputSizeBytes = finalSize
		return nil
	}); err != nil {
		slog.Error("agent: persist terminal state", "id", id, "err", err)
	}
	r.publish(id, Event{State: &StatePayload{State: terminal, Error: msg}})
	r.closeSubscribers(id)
	switch terminal {
	case StateDone:
		slog.Info("agent: encode done", "id", id, "outputBytes", finalSize)
	case StateFailed:
		slog.Warn("agent: encode failed", "id", id, "err", msg)
	case StateCancelled:
		slog.Info("agent: encode cancelled", "id", id)
	}
}

// Cancel asks the in-flight encode for id to stop. The runner transitions the
// job to StateCancelled once HandBrake exits. Safe to call on a job that
// isn't active; in that case the caller should just go straight to Delete.
func (r *Runner) Cancel(id string) bool {
	r.mu.Lock()
	cancel, active := r.active[id]
	r.mu.Unlock()
	if !active {
		return false
	}
	cancel()
	return true
}

// Subscribe returns a channel that receives every event for id until the job
// reaches a terminal state (channel closed) or the caller invokes unsubscribe.
// Buffered so a slow consumer doesn't stall the runner; events drop on full.
func (r *Runner) Subscribe(id string) (<-chan Event, func()) {
	ch := make(chan Event, 32)
	r.mu.Lock()
	r.subs[id] = append(r.subs[id], ch)
	r.subIndex[ch] = id
	r.mu.Unlock()
	unsub := func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		jid, ok := r.subIndex[ch]
		if !ok {
			return
		}
		delete(r.subIndex, ch)
		list := r.subs[jid]
		for i, c := range list {
			if c == ch {
				r.subs[jid] = append(list[:i], list[i+1:]...)
				break
			}
		}
		if len(r.subs[jid]) == 0 {
			delete(r.subs, jid)
		}
		// Drain and close so the consumer's range-loop exits cleanly.
		select {
		case <-ch:
		default:
		}
	}
	return ch, unsub
}

func (r *Runner) publish(id string, e Event) {
	r.mu.Lock()
	subs := append([]chan Event{}, r.subs[id]...)
	r.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- e:
		default:
			// Slow consumer; drop. Progress events are idempotent so a
			// missed one only loses a frame on the dashboard.
		}
	}
}

func (r *Runner) closeSubscribers(id string) {
	r.mu.Lock()
	subs := r.subs[id]
	delete(r.subs, id)
	for _, ch := range subs {
		delete(r.subIndex, ch)
	}
	r.mu.Unlock()
	for _, ch := range subs {
		close(ch)
	}
}

// cleanupTerminal removes per-job directories that have been in a terminal
// state for longer than terminalTTL. The server-side client is expected to
// DELETE the job after pulling the output, but if it doesn't (crash, network
// drop) the agent shouldn't keep growing forever.
func (r *Runner) cleanupTerminal() {
	cutoff := time.Now().Add(-terminalTTL)
	for _, js := range r.store.List() {
		if !js.State.Terminal() {
			continue
		}
		if js.FinishedAt == nil || js.FinishedAt.After(cutoff) {
			continue
		}
		if err := r.store.Delete(js.ID); err != nil {
			slog.Warn("agent: ttl cleanup failed", "id", js.ID, "err", err)
			continue
		}
		slog.Debug("agent: ttl-deleted terminal job", "id", js.ID, "state", js.State)
	}
}
