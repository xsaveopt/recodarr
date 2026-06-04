package agent

import (
	"time"

	"github.com/sratabix/recodarr/internal/handbrake"
)

const PathPrefix = "/v1"

type State string

const (
	StateAwaitingSource State = "awaiting_source"
	StateQueued         State = "queued"
	StateEncoding       State = "encoding"
	StateDone           State = "done"
	StateFailed         State = "failed"
	StateCancelled      State = "cancelled"
)

func (s State) Terminal() bool {
	return s == StateDone || s == StateFailed || s == StateCancelled
}

type JobRequest struct {
	Filename string `json:"filename"`

	SizeBytes int64 `json:"sizeBytes"`

	Settings handbrake.Settings `json:"settings"`

	OutputContainer string `json:"outputContainer"`

	SourcePath string `json:"sourcePath,omitempty"`

	SourceHash string `json:"sourceHash,omitempty"`
}

type JobCreateResponse struct {
	JobID     string `json:"jobId"`
	UploadURL string `json:"uploadUrl"`

	LocalSource bool `json:"localSource,omitempty"`
}

type JobStateSnapshot struct {
	ID              string               `json:"id"`
	State           State                `json:"state"`
	Progress        ProgressPayload      `json:"progress"`
	CreatedAt       time.Time            `json:"createdAt"`
	StartedAt       *time.Time           `json:"startedAt,omitempty"`
	FinishedAt      *time.Time           `json:"finishedAt,omitempty"`
	Error           string               `json:"error,omitempty"`
	OutputSizeBytes int64                `json:"outputSizeBytes,omitempty"`
	Request         *JobRequest          `json:"request,omitempty"`
	Result          *handbrake.RunResult `json:"-"`

	LocalSource     bool   `json:"localSource,omitempty"`
	LocalOutputPath string `json:"localOutputPath,omitempty"`
}

type ProgressPayload struct {
	Percent float64 `json:"percent"`
	FPS     float64 `json:"fps"`
	ETA     string  `json:"eta"`
}

type StatePayload struct {
	State State  `json:"state"`
	Error string `json:"error,omitempty"`
}

type HealthSnapshot struct {
	Version          string `json:"version"`
	HandbrakeVersion string `json:"handbrakeVersion"`
	SlotsUsed        int    `json:"slotsUsed"`
	SlotsMax         int    `json:"slotsMax"`
	JobsActive       int    `json:"jobsActive"`
	DiskFreeBytes    uint64 `json:"diskFreeBytes"`
	LocalFS          bool   `json:"localFs"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

const (
	EventProgress = "progress"
	EventState    = "state"
)
