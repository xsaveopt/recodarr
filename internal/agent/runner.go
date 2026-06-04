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

const terminalTTL = 1 * time.Hour

const tickInterval = 1 * time.Second

type Runner struct {
	store       *Store
	maxParallel int

	mu       sync.Mutex
	active   map[string]context.CancelFunc
	subs     map[string][]chan Event
	subIndex map[chan Event]string
}

type Event struct {
	Progress *ProgressPayload
	State    *StatePayload
}

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
		r.finish(id, StateFailed, fmt.Errorf("open log: %w", err), 0, "")
		return
	}
	defer func() { _ = logFile.Close() }()

	settings := js.Request.Settings
	settings.NoCommit = true

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

	input := r.store.SourcePath(js)
	if js.LocalSource {
		input = js.Request.SourcePath
	}

	res, err := handbrake.Run(ctx, input, settings, sink, onProgress)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			r.finish(id, StateCancelled, errors.New("cancelled"), 0, "")
			return
		}
		r.finish(id, StateFailed, err, 0, "")
		return
	}

	if js.LocalSource {
		r.finish(id, StateDone, nil, res.FinalSize, res.TempPath)
		return
	}

	if err := os.Rename(res.TempPath, r.store.OutputPath(js)); err != nil {
		_ = os.Remove(res.TempPath)
		r.finish(id, StateFailed, fmt.Errorf("publish output: %w", err), 0, "")
		return
	}
	r.finish(id, StateDone, nil, res.FinalSize, "")
}

func (r *Runner) finish(id string, terminal State, encErr error, finalSize int64, localOutput string) {
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
		js.LocalOutputPath = localOutput
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
