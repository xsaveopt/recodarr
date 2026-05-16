// Package agent implements Recodarr's remote-encode protocol.
//
// The same Recodarr binary runs as either the primary server (default) or as
// an "agent": a stripped-down HTTP service that accepts encode jobs, runs
// HandBrake locally, and streams the result back. The server-side Client in
// this package speaks to a running Agent over the wire.
//
// Protocol overview: see docs/remote-agent.md. In short:
//
//  1. POST   /v1/jobs                 — create the job, returns ID
//  2. PUT    /v1/jobs/{id}/source     — upload source bytes
//  3. GET    /v1/jobs/{id}/events     — SSE stream: progress + state events
//  4. GET    /v1/jobs/{id}/output     — download encoded file (Range supported)
//  5. DELETE /v1/jobs/{id}            — clean up
//
// All requests carry `Authorization: Bearer <token>` except `GET /v1/healthz`,
// which is unauthenticated so reverse-proxy health checks don't need the
// token distributed.
package agent

import (
	"time"

	"github.com/sratabix/recodarr/internal/handbrake"
)

// PathPrefix is the URL prefix every agent endpoint lives under. Bumping it
// reserves the older path for backwards-compat shims when (if ever) the
// protocol changes incompatibly.
const PathPrefix = "/v1"

// State is the lifecycle position of a single agent-side job.
type State string

const (
	StateAwaitingSource State = "awaiting_source" // created, waiting for the PUT
	StateQueued         State = "queued"          // source uploaded, runner hasn't picked it up
	StateEncoding       State = "encoding"        // HandBrake is running
	StateDone           State = "done"            // success; output ready to download
	StateFailed         State = "failed"          // error; see Error field
	StateCancelled      State = "cancelled"       // DELETE-d while active
)

// Terminal reports whether s is a sink state from which no more transitions
// happen. Used to decide when to close SSE streams.
func (s State) Terminal() bool {
	return s == StateDone || s == StateFailed || s == StateCancelled
}

// JobRequest is the POST /v1/jobs body. The agent stores it verbatim alongside
// the job so it survives restarts.
type JobRequest struct {
	// Filename is a hint used to derive the agent-side on-disk filename and
	// the extension HandBrake reads. Not authoritative; the agent only uses
	// the extension. The server is free to pass the original basename.
	Filename string `json:"filename"`
	// SizeBytes is the expected upload size. The agent rejects a PUT whose
	// Content-Length doesn't match (defense against truncated uploads).
	SizeBytes int64 `json:"sizeBytes"`
	// Settings is the full HandBrake encoder configuration. Reused verbatim
	// from internal/handbrake so adding new knobs there doesn't need a
	// protocol bump.
	Settings handbrake.Settings `json:"settings"`
	// OutputContainer is "mkv" or "mp4". The agent writes output.<ext>.
	OutputContainer string `json:"outputContainer"`
}

// JobCreateResponse is the POST /v1/jobs reply.
type JobCreateResponse struct {
	JobID     string `json:"jobId"`
	UploadURL string `json:"uploadUrl"` // relative, e.g. "/v1/jobs/<id>/source"
}

// JobStateSnapshot is what GET /v1/jobs/{id} returns. Fields that are not yet
// known for the current state are zero-valued (e.g. Progress on a queued
// job, FinishedAt on an encoding job).
type JobStateSnapshot struct {
	ID              string             `json:"id"`
	State           State              `json:"state"`
	Progress        ProgressPayload    `json:"progress"`
	CreatedAt       time.Time          `json:"createdAt"`
	StartedAt       *time.Time         `json:"startedAt,omitempty"`
	FinishedAt      *time.Time         `json:"finishedAt,omitempty"`
	Error           string             `json:"error,omitempty"`
	OutputSizeBytes int64              `json:"outputSizeBytes,omitempty"`
	Request         *JobRequest        `json:"request,omitempty"` // omitted in list responses
	Result          *handbrake.RunResult `json:"-"`               // server-side only
}

// ProgressPayload matches the on-the-wire shape of HandBrake's per-tick
// progress. Identical to handbrake.Progress but kept separate so a future
// protocol version can extend it without touching the encoder package.
type ProgressPayload struct {
	Percent float64 `json:"percent"`
	FPS     float64 `json:"fps"`
	ETA     string  `json:"eta"`
}

// StatePayload is the body of an SSE `state` event. Fired on every state
// transition. Error is only populated when transitioning into StateFailed.
type StatePayload struct {
	State State  `json:"state"`
	Error string `json:"error,omitempty"`
}

// HealthSnapshot is the GET /v1/healthz body. Public — does not include the
// token or any per-job detail.
type HealthSnapshot struct {
	Version          string `json:"version"`           // recodarr binary version
	HandbrakeVersion string `json:"handbrakeVersion"`  // first line of HandBrakeCLI --version
	SlotsUsed        int    `json:"slotsUsed"`
	SlotsMax         int    `json:"slotsMax"`
	JobsActive       int    `json:"jobsActive"`        // queued + encoding
	DiskFreeBytes    uint64 `json:"diskFreeBytes"`     // on the data dir partition
}

// ErrorResponse is the body of any non-2xx JSON response.
type ErrorResponse struct {
	Error string `json:"error"`
}

// SSE event names exchanged on GET /v1/jobs/{id}/events.
const (
	EventProgress = "progress"
	EventState    = "state"
)
