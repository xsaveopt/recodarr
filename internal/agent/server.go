package agent

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/go-chi/chi/v5"

	"github.com/xsaveopt/recodarr/internal/handbrake"
)

const AgentVersion = "1"

type Server struct {
	store   *Store
	runner  *Runner
	token   string
	localFS bool
	hbFor   func(jobID int64) io.Writer
}

func NewServer(store *Store, runner *Runner, token string, localFS bool, hbFor func(jobID int64) io.Writer) *Server {
	return &Server{store: store, runner: runner, token: token, localFS: localFS, hbFor: hbFor}
}

func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Route(PathPrefix, func(r chi.Router) {
		r.Get("/healthz", s.healthz)
		r.Group(func(r chi.Router) {
			r.Use(s.bearerAuth)
			r.Post("/jobs", s.createJob)
			r.Get("/jobs", s.listJobs)
			r.Put("/jobs/{id}/source", s.uploadSource)
			r.Get("/jobs/{id}", s.getJob)
			r.Get("/jobs/{id}/events", s.streamEvents)
			r.Get("/jobs/{id}/output", s.downloadOutput)
			r.Get("/jobs/{id}/log", s.downloadLog)
			r.Delete("/jobs/{id}", s.deleteJob)
		})
	})
	return r
}

func (s *Server) bearerAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			writeError(w, http.StatusUnauthorized, "missing or invalid bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	var free uint64
	var stat syscall.Statfs_t
	if err := syscall.Statfs(s.store.Root(), &stat); err == nil {
		free = stat.Bavail * uint64(stat.Bsize)
	}
	active := 0
	for _, js := range s.store.List() {
		if js.State == StateQueued || js.State == StateEncoding {
			active++
		}
	}
	writeJSON(w, http.StatusOK, HealthSnapshot{
		Version:          AgentVersion + "+" + runtime.Version(),
		HandbrakeVersion: firstLine(handbrake.VersionString()),
		SlotsMax:         s.runner.maxParallel,
		SlotsUsed:        len(s.runner.active),
		JobsActive:       active,
		DiskFreeBytes:    free,
		LocalFS:          s.localFS,
	})
}

func (s *Server) createJob(w http.ResponseWriter, r *http.Request) {
	var req JobRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad JSON: "+err.Error())
		return
	}
	if req.SizeBytes <= 0 {
		writeError(w, http.StatusBadRequest, "sizeBytes must be > 0")
		return
	}
	if req.Filename == "" {
		writeError(w, http.StatusBadRequest, "filename required")
		return
	}
	if req.OutputContainer != "mkv" && req.OutputContainer != "mp4" {
		writeError(w, http.StatusBadRequest, "outputContainer must be 'mkv' or 'mp4'")
		return
	}

	localSource := s.canEncodeInPlace(req)

	js, err := s.store.Create(req, localSource)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	slog.Info("agent: job created", "id", js.ID, "filename", req.Filename, "size", req.SizeBytes, "localSource", localSource)
	resp := JobCreateResponse{
		JobID:       js.ID,
		LocalSource: localSource,
	}
	if !localSource {
		resp.UploadURL = fmt.Sprintf("%s/jobs/%s/source", PathPrefix, js.ID)
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) canEncodeInPlace(req JobRequest) bool {
	if !s.localFS || req.SourcePath == "" || req.SourceHash == "" {
		return false
	}
	if !filepath.IsAbs(req.SourcePath) || strings.ContainsRune(req.SourcePath, 0) ||
		filepath.Clean(req.SourcePath) != req.SourcePath {
		slog.Warn("agent: local-fs source path rejected", "path", req.SourcePath)
		return false
	}
	info, err := os.Stat(req.SourcePath)
	if err != nil || info.IsDir() {
		slog.Info("agent: local-fs miss, file not present locally", "path", req.SourcePath)
		return false
	}
	if info.Size() != req.SizeBytes {
		slog.Info("agent: local-fs miss, size mismatch", "path", req.SourcePath, "local", info.Size(), "remote", req.SizeBytes)
		return false
	}
	got, err := HashFile(req.SourcePath)
	if err != nil {
		slog.Warn("agent: local-fs hash failed", "path", req.SourcePath, "err", err)
		return false
	}
	if got != req.SourceHash {
		slog.Info("agent: local-fs miss, hash mismatch", "path", req.SourcePath)
		return false
	}
	slog.Info("agent: encoding in place from shared filesystem", "path", req.SourcePath)
	return true
}

func (s *Server) uploadSource(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	js, ok := s.store.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	if js.State != StateAwaitingSource {
		writeError(w, http.StatusConflict, "job is in state "+string(js.State)+", not awaiting_source")
		return
	}
	if r.ContentLength != js.Request.SizeBytes {
		writeError(w, http.StatusBadRequest,
			fmt.Sprintf("Content-Length %d doesn't match declared size %d", r.ContentLength, js.Request.SizeBytes))
		return
	}
	target := s.store.SourcePath(js)
	f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "open source: "+err.Error())
		return
	}
	n, err := io.Copy(f, r.Body)
	_ = f.Close()
	if err != nil {
		_ = os.Remove(target)
		writeError(w, http.StatusInternalServerError, "write source: "+err.Error())
		return
	}
	if n != js.Request.SizeBytes {
		_ = os.Remove(target)
		writeError(w, http.StatusBadRequest,
			fmt.Sprintf("uploaded %d bytes but declared %d", n, js.Request.SizeBytes))
		return
	}
	if err := s.store.Update(id, func(js *JobStateSnapshot) error {
		js.State = StateQueued
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	slog.Info("agent: source uploaded", "id", id, "bytes", n)
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	js, ok := s.store.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	writeJSON(w, http.StatusOK, js)
}

func (s *Server) listJobs(w http.ResponseWriter, _ *http.Request) {
	all := s.store.List()

	for _, js := range all {
		js.Request = nil
	}
	writeJSON(w, http.StatusOK, all)
}

func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	js, ok := s.store.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	writeSSE(w, EventState, StatePayload{State: js.State, Error: js.Error})
	if js.Progress.Percent > 0 {
		writeSSE(w, EventProgress, js.Progress)
	}
	flusher.Flush()
	if js.State.Terminal() {
		return
	}

	ch, unsub := s.runner.Subscribe(id)
	defer unsub()
	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			if e.Progress != nil {
				writeSSE(w, EventProgress, *e.Progress)
			}
			if e.State != nil {
				writeSSE(w, EventState, *e.State)
			}
			flusher.Flush()
			if e.State != nil && e.State.State.Terminal() {
				return
			}
		}
	}
}

func (s *Server) downloadOutput(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	js, ok := s.store.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	if js.State != StateDone {
		writeError(w, http.StatusConflict, "job is in state "+string(js.State)+", not done")
		return
	}
	path := s.store.OutputPath(js)

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(js.Request.Filename))
	http.ServeFile(w, r, path)
}

func (s *Server) downloadLog(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	js, ok := s.store.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	http.ServeFile(w, r, s.store.LogPath(js))
}

func (s *Server) deleteJob(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	js, ok := s.store.Get(id)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !js.State.Terminal() {
		s.runner.Cancel(id)
	}
	if err := s.store.Delete(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	slog.Info("agent: job deleted", "id", id)
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ErrorResponse{Error: msg})
}

func writeSSE(w io.Writer, event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

var _ = errors.New
