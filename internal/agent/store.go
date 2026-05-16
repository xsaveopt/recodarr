package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Store is the agent's per-process job registry. State is mirrored to
// <root>/jobs/<id>/state.json so a crash-restart can reconstruct everything.
//
// The Store also owns each job's on-disk directory. Files written by the
// agent runner (source upload, encoded output, handbrake log) live alongside
// state.json so DELETE /v1/jobs/{id} is a single os.RemoveAll.
type Store struct {
	root string

	mu   sync.Mutex
	jobs map[string]*JobStateSnapshot
}

// OpenStore prepares the agent's working directory, scans any pre-existing
// jobs into memory, and reconciles in-flight state to terminal failure.
// Anything we found in StateEncoding could not possibly still be running
// (we just started), so it's a crash to recover from.
func OpenStore(root string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(root, "jobs"), 0o755); err != nil {
		return nil, fmt.Errorf("create agent root: %w", err)
	}
	s := &Store{root: root, jobs: map[string]*JobStateSnapshot{}}
	if err := s.scan(); err != nil {
		return nil, fmt.Errorf("scan agent jobs: %w", err)
	}
	return s, nil
}

func (s *Store) scan() error {
	entries, err := os.ReadDir(filepath.Join(s.root, "jobs"))
	if err != nil {
		return err
	}
	recovered := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		js, err := s.readManifest(e.Name())
		if err != nil {
			slog.Warn("agent: bad job manifest, skipping", "id", e.Name(), "err", err)
			continue
		}
		// In-flight at boot means we crashed. Mark failed so the server-side
		// client sees a definitive terminal state instead of polling forever.
		if js.State == StateEncoding {
			js.State = StateFailed
			js.Error = "agent restarted while encoding"
			now := time.Now()
			js.FinishedAt = &now
			if err := s.writeManifest(js); err != nil {
				return err
			}
			recovered++
		}
		s.jobs[js.ID] = js
	}
	if recovered > 0 {
		slog.Warn("agent: recovered in-flight jobs as failed", "count", recovered)
	}
	return nil
}

// Root returns the data directory the store owns. Used by the server's
// /healthz handler to report disk usage.
func (s *Store) Root() string { return s.root }

// Create allocates a new job ID, persists the initial manifest, and returns
// the in-memory snapshot. The caller is then expected to accept a source
// upload via SourcePath.
func (s *Store) Create(req JobRequest) (*JobStateSnapshot, error) {
	id := uuid.NewString()
	if err := os.MkdirAll(s.JobDir(id), 0o755); err != nil {
		return nil, fmt.Errorf("create job dir: %w", err)
	}
	js := &JobStateSnapshot{
		ID:        id,
		State:     StateAwaitingSource,
		CreatedAt: time.Now(),
		Request:   &req,
	}
	if err := s.writeManifest(js); err != nil {
		_ = os.RemoveAll(s.JobDir(id))
		return nil, err
	}
	s.mu.Lock()
	s.jobs[id] = js
	s.mu.Unlock()
	return js, nil
}

// Get returns a copy of the current snapshot.
func (s *Store) Get(id string) (*JobStateSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	js, ok := s.jobs[id]
	if !ok {
		return nil, false
	}
	cp := *js
	return &cp, true
}

// List returns a snapshot copy of every known job. Order is not stable.
func (s *Store) List() []*JobStateSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*JobStateSnapshot, 0, len(s.jobs))
	for _, js := range s.jobs {
		cp := *js
		out = append(out, &cp)
	}
	return out
}

// Delete removes the job's on-disk directory and forgets it. Safe to call on
// an unknown id (returns nil).
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	delete(s.jobs, id)
	s.mu.Unlock()
	return os.RemoveAll(s.JobDir(id))
}

// Update applies fn under the store lock and persists the result. Use this
// for any state transition (StateAwaitingSource → StateQueued, progress
// updates, terminal markers) so the manifest never diverges from memory.
//
// fn receives a pointer to the live snapshot; mutations are written back to
// disk atomically before Update returns.
func (s *Store) Update(id string, fn func(js *JobStateSnapshot) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	js, ok := s.jobs[id]
	if !ok {
		return ErrNotFound
	}
	if err := fn(js); err != nil {
		return err
	}
	return s.writeManifest(js)
}

// ClaimQueued atomically picks the oldest StateQueued job, transitions it to
// StateEncoding, and returns a snapshot. Returns (nil, false) when nothing
// is waiting.
func (s *Store) ClaimQueued() (*JobStateSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var pick *JobStateSnapshot
	for _, js := range s.jobs {
		if js.State != StateQueued {
			continue
		}
		if pick == nil || js.CreatedAt.Before(pick.CreatedAt) {
			pick = js
		}
	}
	if pick == nil {
		return nil, false
	}
	now := time.Now()
	pick.State = StateEncoding
	pick.StartedAt = &now
	if err := s.writeManifest(pick); err != nil {
		// Roll back the in-memory change so a subsequent claim can retry.
		pick.State = StateQueued
		pick.StartedAt = nil
		slog.Error("agent: persist claim failed", "id", pick.ID, "err", err)
		return nil, false
	}
	cp := *pick
	return &cp, true
}

// JobDir returns the directory the agent stores per-job artifacts in.
// Exported so the server and runner share one source of truth for the layout.
func (s *Store) JobDir(id string) string { return filepath.Join(s.root, "jobs", id) }

// SourcePath returns where the uploaded source for id is written. The
// extension is derived from the original Filename hint so HandBrake's
// container sniffing has something sensible to look at.
func (s *Store) SourcePath(js *JobStateSnapshot) string {
	ext := filepath.Ext(js.Request.Filename)
	if ext == "" {
		ext = ".mkv"
	}
	return filepath.Join(s.JobDir(js.ID), "source"+ext)
}

// OutputPath returns where the encoded result is written. The container is
// taken from the request; HandBrake validates that the encoder/container
// combo is sane.
func (s *Store) OutputPath(js *JobStateSnapshot) string {
	ext := js.Request.OutputContainer
	if ext == "" {
		ext = "mkv"
	}
	return filepath.Join(s.JobDir(js.ID), "output."+ext)
}

// LogPath returns where the HandBrake-side log for this job lives. The
// runner writes a per-job copy so DELETE /v1/jobs/{id} fully cleans up.
func (s *Store) LogPath(js *JobStateSnapshot) string {
	return filepath.Join(s.JobDir(js.ID), "handbrake.log")
}

func (s *Store) manifestPath(id string) string {
	return filepath.Join(s.JobDir(id), "state.json")
}

func (s *Store) readManifest(id string) (*JobStateSnapshot, error) {
	data, err := os.ReadFile(s.manifestPath(id))
	if err != nil {
		return nil, err
	}
	var js JobStateSnapshot
	if err := json.Unmarshal(data, &js); err != nil {
		return nil, err
	}
	return &js, nil
}

func (s *Store) writeManifest(js *JobStateSnapshot) error {
	data, err := json.MarshalIndent(js, "", "  ")
	if err != nil {
		return err
	}
	final := s.manifestPath(js.ID)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

// ErrNotFound is returned by Update / SetSourceUploaded when the given id is
// unknown. The server handler maps it to a 404.
var ErrNotFound = errors.New("job not found")
