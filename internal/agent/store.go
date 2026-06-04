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

type Store struct {
	root string

	mu   sync.Mutex
	jobs map[string]*JobStateSnapshot
}

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

func (s *Store) Root() string { return s.root }

func (s *Store) Create(req JobRequest, localSource bool) (*JobStateSnapshot, error) {
	id := uuid.NewString()
	if err := os.MkdirAll(s.JobDir(id), 0o755); err != nil {
		return nil, fmt.Errorf("create job dir: %w", err)
	}
	state := StateAwaitingSource
	if localSource {
		state = StateQueued
	}
	js := &JobStateSnapshot{
		ID:          id,
		State:       state,
		CreatedAt:   time.Now(),
		Request:     &req,
		LocalSource: localSource,
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

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	delete(s.jobs, id)
	s.mu.Unlock()
	return os.RemoveAll(s.JobDir(id))
}

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
		pick.State = StateQueued
		pick.StartedAt = nil
		slog.Error("agent: persist claim failed", "id", pick.ID, "err", err)
		return nil, false
	}
	cp := *pick
	return &cp, true
}

func (s *Store) JobDir(id string) string { return filepath.Join(s.root, "jobs", id) }

func (s *Store) SourcePath(js *JobStateSnapshot) string {
	ext := filepath.Ext(js.Request.Filename)
	if ext == "" {
		ext = ".mkv"
	}
	return filepath.Join(s.JobDir(js.ID), "source"+ext)
}

func (s *Store) OutputPath(js *JobStateSnapshot) string {
	ext := js.Request.OutputContainer
	if ext == "" {
		ext = "mkv"
	}
	return filepath.Join(s.JobDir(js.ID), "output."+ext)
}

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

var ErrNotFound = errors.New("job not found")
