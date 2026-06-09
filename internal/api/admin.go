package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/sync/errgroup"

	"github.com/sratabix/recodarr/internal/agent"
	"github.com/sratabix/recodarr/internal/arr"
	"github.com/sratabix/recodarr/internal/handbrake"
	"github.com/sratabix/recodarr/internal/health"
	"github.com/sratabix/recodarr/internal/job"
	"github.com/sratabix/recodarr/internal/logging"
	"github.com/sratabix/recodarr/internal/qbit"
	"github.com/sratabix/recodarr/internal/store"
)

type workerClient interface {
	CancelEncoding(jobID int64) bool
	EncodingJobID() int64
	EncodingJobIDs() []int64
	LastTickAt() time.Time
	Subscribe() (<-chan job.ProgressEvent, func())
	CurrentProgress() job.ProgressEvent
	AllProgress() []job.ProgressEvent
	WindowStatus(ctx context.Context) job.WindowStatus
	SetPaused(ctx context.Context, paused bool) (int, error)
	IsPaused(ctx context.Context) bool
}

type arrInstanceDTO struct {
	ID            int64  `json:"id"`
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	URL           string `json:"url"`
	APIKey        string `json:"apiKey,omitempty"`
	Enabled       bool   `json:"enabled"`
	WebhookSecret string `json:"webhookSecret,omitempty"`
	HasAPIKey     bool   `json:"hasApiKey"`
	HasSecret     bool   `json:"hasWebhookSecret"`
	Deleted       bool   `json:"deleted"`
}

func (d arrInstanceDTO) toRow() store.ArrInstanceRow {
	return store.ArrInstanceRow{
		ID: d.ID, Kind: d.Kind, Name: d.Name, URL: d.URL, APIKey: d.APIKey,
		Enabled: d.Enabled, WebhookSecret: d.WebhookSecret,
	}
}

func arrRowToDTO(r store.ArrInstanceRow) arrInstanceDTO {
	return arrInstanceDTO{
		ID: r.ID, Kind: r.Kind, Name: r.Name, URL: r.URL,
		Enabled:   r.Enabled,
		HasAPIKey: r.APIKey != "",
		HasSecret: r.WebhookSecret != "",
		Deleted:   r.DeletedAt.Valid,
	}
}

type qbitInstanceDTO struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	Username    string `json:"username"`
	Password    string `json:"password,omitempty"`
	HasPassword bool   `json:"hasPassword"`
}

type profileDTO struct {
	ID                      int64          `json:"id"`
	Name                    string         `json:"name"`
	Encoder                 string         `json:"encoder"`
	EncoderPreset           string         `json:"encoderPreset"`
	EncoderProfile          string         `json:"encoderProfile"`
	EncoderTune             string         `json:"encoderTune"`
	EncoderLevel            string         `json:"encoderLevel"`
	RateControl             string         `json:"rateControl"`
	Quality                 int            `json:"quality"`
	VideoBitrate            int            `json:"videoBitrate"`
	MaxWidth                int            `json:"maxWidth"`
	MaxHeight               int            `json:"maxHeight"`
	AudioEncoder            string         `json:"audioEncoder"`
	AudioBitrate            int            `json:"audioBitrate"`
	AudioMixdown            string         `json:"audioMixdown"`
	AudioBitratesByChannels map[string]int `json:"audioBitratesByChannels"`
	SubtitleCopy            bool           `json:"subtitleCopy"`
	TwoPass                 bool           `json:"twoPass"`
	ContainerFormat         string         `json:"containerFormat"`
	ExtraArgs               string         `json:"extraArgs"`
	Framerate               string         `json:"framerate"`

	SkipCodecs           string `json:"skipCodecs"`
	SkipBitrateMBPerHour int    `json:"skipBitrateMBPerHour"`
	SkipBitrateUnit      string `json:"skipBitrateUnit"`
	SkipFileSizeMB       int    `json:"skipFileSizeMB"`
	SkipDurationMinutes  int    `json:"skipDurationMinutes"`
	SkipHeightPx         int    `json:"skipHeightPx"`
	SkipHDR              bool   `json:"skipHDR"`

	BloatPolicy            string `json:"bloatPolicy"`
	BloatRetryMax          int    `json:"bloatRetryMax"`
	BloatRetryStep         int    `json:"bloatRetryStep"`
	BloatMinSavingsPercent int    `json:"bloatMinSavingsPercent"`
	Deleted                bool   `json:"deleted"`
}

func profileRowToDTO(r store.ProfileRow) profileDTO {
	bitrates := map[string]int{}
	if r.AudioBitratesByChannels != "" && r.AudioBitratesByChannels != "{}" {
		_ = json.Unmarshal([]byte(r.AudioBitratesByChannels), &bitrates)
	}
	return profileDTO{
		ID: r.ID, Name: r.Name, Encoder: r.Encoder,
		EncoderPreset: r.EncoderPreset, EncoderProfile: r.EncoderProfile,
		EncoderTune: r.EncoderTune, EncoderLevel: r.EncoderLevel,
		RateControl:  r.RateControl,
		Quality:      r.Quality,
		VideoBitrate: r.VideoBitrate,
		MaxWidth:     r.MaxWidth, MaxHeight: r.MaxHeight,
		AudioEncoder: r.AudioEncoder,
		AudioBitrate: r.AudioBitrate, AudioMixdown: r.AudioMixdown,
		AudioBitratesByChannels: bitrates,
		SubtitleCopy:            r.SubtitleCopy, TwoPass: r.TwoPass,
		ContainerFormat: r.ContainerFormat, ExtraArgs: r.ExtraArgs,
		Framerate:              r.Framerate,
		SkipCodecs:             r.SkipCodecs,
		SkipBitrateMBPerHour:   r.SkipBitrateMBPerHour,
		SkipBitrateUnit:        r.SkipBitrateUnit,
		SkipFileSizeMB:         r.SkipFileSizeMB,
		SkipDurationMinutes:    r.SkipDurationMinutes,
		SkipHeightPx:           r.SkipHeightPx,
		SkipHDR:                r.SkipHDR,
		BloatPolicy:            r.BloatPolicy,
		BloatRetryMax:          r.BloatRetryMax,
		BloatRetryStep:         r.BloatRetryStep,
		BloatMinSavingsPercent: r.BloatMinSavingsPercent,
		Deleted:                r.DeletedAt.Valid,
	}
}

type tagMappingDTO struct {
	ID        int64  `json:"id"`
	ArrKind   string `json:"arrKind"`
	TagID     int64  `json:"tagId"`
	TagLabel  string `json:"tagLabel"`
	ProfileID int64  `json:"profileId"`
}

type instanceTagDTO struct {
	InstanceID   int64  `json:"instanceId"`
	InstanceName string `json:"instanceName"`
	Kind         string `json:"kind"`
	TagID        int64  `json:"tagId"`
	TagLabel     string `json:"tagLabel"`
}

type statsDTO struct {
	WaitingForSeed     int64 `json:"waitingForSeed"`
	WaitingForHardlink int64 `json:"waitingForHardlink"`
	Ready              int64 `json:"ready"`
	Encoding           int64 `json:"encoding"`
	Done               int64 `json:"done"`
	Failed             int64 `json:"failed"`
	Skipped            int64 `json:"skipped"`
	TotalSavedBytes    int64 `json:"totalSavedBytes"`
}

type LogLevelSetter interface {
	SetAppLevel(slog.Level)
}

func registerAdminRoutes(r chi.Router, st *store.Store, w workerClient, hc *health.Checker, lls LogLevelSetter) {
	r.Get("/handbrake/caps", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, handbrake.QueryCaps())
	})

	r.Get("/debug", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, buildDebugInfo())
	})

	r.Get("/status", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, http.StatusOK, hc.Snapshot(r.Context()))
	})

	r.Get("/stats", func(rw http.ResponseWriter, r *http.Request) {
		stats, err := st.GetJobStats(r.Context())
		if err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(rw, http.StatusOK, statsDTO{
			WaitingForSeed:     stats.WaitingForSeed,
			WaitingForHardlink: stats.WaitingForHardlink,
			Ready:              stats.Ready,
			Encoding:           stats.Encoding,
			Done:               stats.Done,
			Failed:             stats.Failed,
			Skipped:            stats.Skipped,
			TotalSavedBytes:    stats.TotalSavedBytes,
		})
	})

	r.Route("/settings", func(r chi.Router) {
		r.Get("/", getSettings(st))
		r.Put("/", putSettings(st, lls))
	})

	r.Route("/arr-instances", func(r chi.Router) {
		r.Get("/", listArrInstances(st))
		r.Post("/", createArrInstance(st))
		r.Get("/all-tags", listAllArrTags(st))
		r.Put("/{id}", updateArrInstance(st))
		r.Delete("/{id}", deleteArrInstance(st))
		r.Post("/{id}/test", testArrInstance(st))
		r.Get("/{id}/tags", listArrTags(st))
		r.Get("/{id}/library", listArrLibrary(st))
		r.Get("/{id}/library/scan", scanArrLibrary(st))
		r.Post("/{id}/library/queue", queueArrLibrary(st))

		r.Get("/{id}/webhook-secret", revealArrWebhookSecret(st))
	})

	r.Route("/tag-mappings", func(r chi.Router) {
		r.Get("/", listTagMappingsHandler(st))
		r.Post("/", createTagMappingHandler(st))
		r.Delete("/{id}", deleteTagMappingHandler(st))
	})

	r.Route("/qbit-instances", func(r chi.Router) {
		r.Get("/", listQbitInstances(st))
		r.Post("/", upsertQbitInstance(st))
		r.Post("/test", testQbitCredentials())
		r.Delete("/{id}", deleteQbitInstance(st))
		r.Post("/{id}/test", testQbitInstance(st))
	})

	r.Route("/profiles", func(r chi.Router) {
		r.Get("/", listProfiles(st))
		r.Post("/", upsertProfile(st))
		r.Delete("/{id}", deleteProfile(st))
	})

	r.Get("/worker/status", workerStatus(w, st))
	r.Post("/worker/pause", workerSetPaused(w))

	r.Post("/agent/test", testAgent(st))

	r.Get("/jobs", listJobs(st))
	r.Post("/jobs/retry-failed", retryAllFailed(st))
	r.Post("/jobs/{id}/retry", retryJob(st))
	r.Post("/jobs/{id}/cancel", cancelJob(st, w))
	r.Get("/jobs/{id}/debug", debugJob(st))
	r.Delete("/jobs/{id}", deleteJob(st))
	r.Delete("/jobs", deleteTerminalJobs(st))
	r.Post("/jobs/bulk-delete", bulkDeleteJobs(st))
	r.Post("/jobs/bulk-retry", bulkRetryJobs(st))
	r.Post("/jobs/bulk-set-profile", bulkSetJobProfile(st))
}

func getSettings(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m, err := st.GetAllSettings(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if tok, ok := m["agent_token"]; ok {
			delete(m, "agent_token")
			if tok != "" {
				m["hasAgentToken"] = "true"
			}
		}
		writeJSON(w, http.StatusOK, m)
	}
}

func putSettings(st *store.Store, lls LogLevelSetter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var m map[string]string
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		if v, ok := m["log_app_level"]; ok {
			switch strings.ToUpper(strings.TrimSpace(v)) {
			case "DEBUG", "INFO", "WARN", "ERROR":
				m["log_app_level"] = strings.ToUpper(strings.TrimSpace(v))
			default:
				http.Error(w, "log_app_level: expected DEBUG, INFO, WARN, or ERROR", http.StatusBadRequest)
				return
			}
		}
		if v, ok := m["log_rotate_enabled"]; ok && v != "true" && v != "false" {
			http.Error(w, "log_rotate_enabled: expected 'true' or 'false'", http.StatusBadRequest)
			return
		}
		for _, k := range []string{"agent_enabled", "agent_fallback_local"} {
			if v, ok := m[k]; ok && v != "true" && v != "false" {
				http.Error(w, k+": expected 'true' or 'false'", http.StatusBadRequest)
				return
			}
		}
		if v, ok := m["agent_url"]; ok && v != "" {
			u := strings.TrimSpace(v)
			if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
				http.Error(w, "agent_url: must start with http:// or https://", http.StatusBadRequest)
				return
			}
			m["agent_url"] = strings.TrimRight(u, "/")
		}

		if v, ok := m["agent_token"]; ok && strings.TrimSpace(v) == "" {
			delete(m, "agent_token")
		}
		for _, k := range []string{"log_max_size_mb", "log_max_age_days", "log_max_backups"} {
			if v, ok := m[k]; ok {
				n, err := strconv.Atoi(v)
				if err != nil || n < 0 {
					http.Error(w, k+": expected non-negative integer", http.StatusBadRequest)
					return
				}
				if k == "log_max_size_mb" && n < 1 {
					http.Error(w, k+": expected integer ≥ 1", http.StatusBadRequest)
					return
				}
			}
		}
		for _, k := range []string{"encoding_window_start", "encoding_window_end"} {
			if v, ok := m[k]; ok && v != "" && !isValidHHMM(v) {
				http.Error(w, k+": expected HH:MM", http.StatusBadRequest)
				return
			}
		}
		if v, ok := m["max_parallel_encodes"]; ok {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > store.MaxParallelEncodesCap {
				http.Error(w,
					fmt.Sprintf("max_parallel_encodes: expected integer 1..%d", store.MaxParallelEncodesCap),
					http.StatusBadRequest)
				return
			}
		}
		if v, ok := m["encoding_paused"]; ok && v != "true" && v != "false" {
			http.Error(w, "encoding_paused: expected 'true' or 'false'", http.StatusBadRequest)
			return
		}
		if v, ok := m["output_suffix_enabled"]; ok && v != "true" && v != "false" {
			http.Error(w, "output_suffix_enabled: expected 'true' or 'false'", http.StatusBadRequest)
			return
		}
		if v, ok := m["output_suffix"]; ok {
			if !isValidOutputSuffix(v) {
				http.Error(w,
					"output_suffix: 1–32 chars, letters/digits/dash/underscore only, no dots or path separators",
					http.StatusBadRequest)
				return
			}
		}
		for k, v := range m {
			if err := st.SetSetting(r.Context(), k, v); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		if v, ok := m["log_app_level"]; ok && lls != nil {
			lls.SetAppLevel(logging.ParseLevel(v))
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func normalizeBloatPolicy(s string) string {
	switch s {
	case "keep_original", "retry_higher_crf":
		return s
	default:
		return "off"
	}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func isValidOutputSuffix(s string) bool {
	if len(s) == 0 || len(s) > 32 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}

func isValidHHMM(s string) bool {
	if len(s) != 5 || s[2] != ':' {
		return false
	}
	h, err1 := strconv.Atoi(s[:2])
	m, err2 := strconv.Atoi(s[3:])
	if err1 != nil || err2 != nil {
		return false
	}
	return h >= 0 && h < 24 && m >= 0 && m < 60
}

func listArrInstances(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var (
			rows []store.ArrInstanceRow
			err  error
		)
		if r.URL.Query().Get("includeDeleted") == "true" {
			rows, err = st.ListArrInstancesIncludingDeleted(r.Context())
		} else {
			rows, err = st.ListArrInstances(r.Context())
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := make([]arrInstanceDTO, 0, len(rows))
		for _, row := range rows {
			out = append(out, arrRowToDTO(row))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func createArrInstance(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var d arrInstanceDTO
		if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		if d.Kind != "sonarr" && d.Kind != "radarr" {
			http.Error(w, "kind must be sonarr or radarr", http.StatusBadRequest)
			return
		}
		id, err := st.CreateArrInstance(r.Context(), d.toRow())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		row, err := st.GetArrInstance(r.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, arrRowToDTO(*row))
	}
}

func updateArrInstance(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		var d arrInstanceDTO
		if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		d.ID = id
		if err := st.UpdateArrInstance(r.Context(), d.toRow()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		row, err := st.GetArrInstance(r.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, arrRowToDTO(*row))
	}
}

func deleteArrInstance(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		if err := st.DeleteArrInstance(r.Context(), id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

type tagDTO struct {
	ID    int64  `json:"id"`
	Label string `json:"label"`
}

func listArrTags(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		inst, err := st.GetArrInstance(r.Context(), id)
		if err != nil {
			http.Error(w, "instance not found", http.StatusNotFound)
			return
		}
		tags, err := arr.New(arr.Kind(inst.Kind), inst.URL, inst.APIKey).Tags(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		out := make([]tagDTO, 0, len(tags))
		for _, t := range tags {
			out = append(out, tagDTO{ID: t.ID, Label: t.Label})
		}
		writeJSON(w, http.StatusOK, out)
	}
}

type libraryItemDTO struct {
	ItemID      int64  `json:"itemId"`
	Title       string `json:"title"`
	Path        string `json:"path"`
	TagID       int64  `json:"tagId"`
	TagLabel    string `json:"tagLabel"`
	ProfileID   int64  `json:"profileId"`
	ProfileName string `json:"profileName"`
	FileCount   int    `json:"fileCount"`
	TotalSize   int64  `json:"totalSize"`
	ActiveJobs  int    `json:"activeJobs"`
	DoneJobs    int    `json:"doneJobs"`
}

type libraryResponseDTO struct {
	Items      []libraryItemDTO `json:"items"`
	NoMappings bool             `json:"noMappings"`
}

func listArrLibrary(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		inst, err := st.GetArrInstance(r.Context(), id)
		if err != nil {
			http.Error(w, "instance not found", http.StatusNotFound)
			return
		}
		if inst.Kind != "sonarr" && inst.Kind != "radarr" {
			http.Error(w, "unsupported instance kind", http.StatusBadRequest)
			return
		}

		mappings, err := st.ListTagMappingsByKind(r.Context(), inst.Kind)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if len(mappings) == 0 {
			writeJSON(w, http.StatusOK, libraryResponseDTO{Items: []libraryItemDTO{}, NoMappings: true})
			return
		}

		mapByTagID := make(map[int64]store.TagMappingRow, len(mappings))
		for _, mp := range mappings {
			if _, exists := mapByTagID[mp.TagID]; !exists {
				mapByTagID[mp.TagID] = mp
			}
		}

		profiles, err := st.ListProfiles(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		profileNames := make(map[int64]string, len(profiles))
		for _, p := range profiles {
			profileNames[p.ID] = p.Name
		}

		client := arr.New(arr.Kind(inst.Kind), inst.URL, inst.APIKey)
		items, err := client.Library(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		tags, err := client.Tags(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		tagLabels := make(map[int64]string, len(tags))
		for _, t := range tags {
			tagLabels[t.ID] = t.Label
		}

		type pending struct {
			item     arr.LibraryItem
			mapping  store.TagMappingRow
			tagLabel string
		}
		filtered := make([]pending, 0, len(items))
		parentIDs := make([]int64, 0, len(items))
		for _, it := range items {
			for _, tid := range it.TagIDs {
				if mp, ok := mapByTagID[tid]; ok {
					filtered = append(filtered, pending{item: it, mapping: mp, tagLabel: tagLabels[tid]})
					parentIDs = append(parentIDs, it.ID)
					break
				}
			}
		}

		summaries, err := st.JobSummaryByParent(r.Context(), inst.Kind, inst.ID, parentIDs)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		out := make([]libraryItemDTO, 0, len(filtered))
		for _, f := range filtered {
			s := summaries[f.item.ID]
			out = append(out, libraryItemDTO{
				ItemID:      f.item.ID,
				Title:       f.item.Title,
				Path:        f.item.Path,
				TagID:       f.mapping.TagID,
				TagLabel:    f.tagLabel,
				ProfileID:   f.mapping.ProfileID,
				ProfileName: profileNames[f.mapping.ProfileID],
				FileCount:   f.item.FileCount,
				TotalSize:   f.item.TotalSize,
				ActiveJobs:  s.Active,
				DoneJobs:    s.Done,
			})
		}
		writeJSON(w, http.StatusOK, libraryResponseDTO{Items: out})
	}
}

type scanItemDTO struct {
	ItemID         int64  `json:"itemId"`
	Title          string `json:"title"`
	Path           string `json:"path"`
	TagLabel       string `json:"tagLabel"`
	ProfileName    string `json:"profileName"`
	FileCount      int    `json:"fileCount"`
	EncodedCount   int    `json:"encodedCount"`
	UnencodedCount int    `json:"unencodedCount"`
}

type scanResponseDTO struct {
	Items          []scanItemDTO `json:"items"`
	NoMappings     bool          `json:"noMappings"`
	SuffixDisabled bool          `json:"suffixDisabled"`
}

func scanArrLibrary(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		inst, err := st.GetArrInstance(r.Context(), id)
		if err != nil {
			http.Error(w, "instance not found", http.StatusNotFound)
			return
		}
		if inst.Kind != "sonarr" && inst.Kind != "radarr" {
			http.Error(w, "unsupported instance kind", http.StatusBadRequest)
			return
		}

		cfg, err := st.LoadAppSettings(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !cfg.OutputSuffixEnabled {
			writeJSON(w, http.StatusOK, scanResponseDTO{Items: []scanItemDTO{}, SuffixDisabled: true})
			return
		}

		mappings, err := st.ListTagMappingsByKind(r.Context(), inst.Kind)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if len(mappings) == 0 {
			writeJSON(w, http.StatusOK, scanResponseDTO{Items: []scanItemDTO{}, NoMappings: true})
			return
		}
		mapByTagID := make(map[int64]store.TagMappingRow, len(mappings))
		for _, mp := range mappings {
			if _, exists := mapByTagID[mp.TagID]; !exists {
				mapByTagID[mp.TagID] = mp
			}
		}

		profiles, err := st.ListProfiles(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		profileNames := make(map[int64]string, len(profiles))
		for _, p := range profiles {
			profileNames[p.ID] = p.Name
		}

		client := arr.New(arr.Kind(inst.Kind), inst.URL, inst.APIKey)
		items, err := client.Library(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		tags, err := client.Tags(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		tagLabels := make(map[int64]string, len(tags))
		for _, t := range tags {
			tagLabels[t.ID] = t.Label
		}

		out := make([]scanItemDTO, 0, len(items))
		for _, it := range items {
			for _, tid := range it.TagIDs {
				mp, ok := mapByTagID[tid]
				if !ok {
					continue
				}
				out = append(out, scanItemDTO{
					ItemID:      it.ID,
					Title:       it.Title,
					Path:        it.Path,
					TagLabel:    tagLabels[tid],
					ProfileName: profileNames[mp.ProfileID],
					FileCount:   it.FileCount,
				})
				break
			}
		}

		g, ctx := errgroup.WithContext(r.Context())
		g.SetLimit(6)
		for i := range out {
			i := i
			g.Go(func() error {
				files, err := client.Files(ctx, out[i].ItemID)
				if err != nil {
					return fmt.Errorf("item %d (%s): %w", out[i].ItemID, out[i].Title, err)
				}
				out[i].FileCount = len(files)
				for _, f := range files {
					if sidecarExists(f.Path, cfg.OutputSuffix) {
						out[i].EncodedCount++
					} else {
						out[i].UnencodedCount++
					}
				}
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		writeJSON(w, http.StatusOK, scanResponseDTO{Items: out})
	}
}

type queueLibraryRequest struct {
	ItemIDs []int64 `json:"itemIds"`
}

type queueLibraryResponse struct {
	Inserted int      `json:"inserted"`
	Skipped  int      `json:"skipped"`
	Errors   []string `json:"errors,omitempty"`
}

func queueArrLibrary(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		inst, err := st.GetArrInstance(r.Context(), id)
		if err != nil {
			http.Error(w, "instance not found", http.StatusNotFound)
			return
		}
		if inst.Kind != "sonarr" && inst.Kind != "radarr" {
			http.Error(w, "unsupported instance kind", http.StatusBadRequest)
			return
		}
		var req queueLibraryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if len(req.ItemIDs) == 0 {
			writeJSON(w, http.StatusOK, queueLibraryResponse{})
			return
		}

		cfg, err := st.LoadAppSettings(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		mappings, err := st.ListTagMappingsByKind(r.Context(), inst.Kind)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if len(mappings) == 0 {
			http.Error(w, "no tag mappings configured for this instance kind", http.StatusBadRequest)
			return
		}
		mapByTagID := make(map[int64]store.TagMappingRow, len(mappings))
		for _, mp := range mappings {
			if _, exists := mapByTagID[mp.TagID]; !exists {
				mapByTagID[mp.TagID] = mp
			}
		}

		client := arr.New(arr.Kind(inst.Kind), inst.URL, inst.APIKey)
		items, err := client.Library(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		type eligibleItem struct {
			title    string
			mapping  store.TagMappingRow
			tagLabel string
		}
		eligible := make(map[int64]eligibleItem, len(items))
		for _, it := range items {
			for _, tid := range it.TagIDs {
				if mp, ok := mapByTagID[tid]; ok {
					eligible[it.ID] = eligibleItem{title: it.Title, mapping: mp, tagLabel: mp.TagLabel}
					break
				}
			}
		}

		requested := make(map[int64]struct{}, len(req.ItemIDs))
		for _, pid := range req.ItemIDs {
			requested[pid] = struct{}{}
		}

		resp := queueLibraryResponse{}
		for pid := range requested {
			meta, ok := eligible[pid]
			if !ok {
				resp.Skipped++
				resp.Errors = append(resp.Errors, fmt.Sprintf("item %d: not eligible (untagged, missing, or mapping removed)", pid))
				continue
			}
			files, err := client.Files(r.Context(), pid)
			if err != nil {
				resp.Errors = append(resp.Errors, fmt.Sprintf("item %d: list files: %v", pid, err))
				continue
			}

			imports, herr := client.ImportHistory(r.Context(), pid)
			if herr != nil {
				slog.Warn("backfill: import history lookup failed; falling back to hardlink wait",
					"kind", inst.Kind, "parent", pid, "err", herr)
			}
			tagsJSON, _ := json.Marshal([]string{meta.tagLabel})
			for _, f := range files {
				clean, err := sanitizeMediaPath(f.Path)
				if err != nil {
					resp.Skipped++
					resp.Errors = append(resp.Errors, fmt.Sprintf("file %s: %v", f.Path, err))
					continue
				}

				if cfg.OutputSuffixEnabled && sidecarExists(clean, cfg.OutputSuffix) {
					resp.Skipped++
					continue
				}

				downloadID := arr.MatchImportDownloadID(imports, arr.Kind(inst.Kind), clean, f.RelativePath)
				status := string(job.StatusWaitingForHardlink)
				if downloadID != "" {
					status = string(job.StatusWaitingForSeed)
				}
				jr := store.JobRow{
					ArrKind:       inst.Kind,
					ArrInstanceID: inst.ID,
					ArrItemID:     f.ID,
					ArrParentID:   pid,
					Title:         meta.title,
					FilePath:      clean,
					FileSize:      f.Size,
					DownloadID:    downloadID,
					ProfileID:     sql.NullInt64{Int64: meta.mapping.ProfileID, Valid: true},
					Status:        status,
					Tags:          string(tagsJSON),
					Source:        "backfill",
				}
				newID, err := enqueueIfNew(r.Context(), st, jr)
				if err != nil {
					resp.Errors = append(resp.Errors, fmt.Sprintf("file %s: insert: %v", f.Path, err))
					continue
				}
				if newID == 0 {
					resp.Skipped++
					continue
				}
				resp.Inserted++
			}
		}
		slog.Info("library backfill",
			"kind", inst.Kind, "instance", inst.ID,
			"requested", len(requested), "inserted", resp.Inserted, "skipped", resp.Skipped)
		writeJSON(w, http.StatusOK, resp)
	}
}

func revealArrWebhookSecret(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		inst, err := st.GetArrInstance(r.Context(), id)
		if err != nil {
			http.Error(w, "instance not found", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"username": WebhookBasicAuthUser,
			"password": inst.WebhookSecret,
		})
	}
}

func testArrInstance(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		inst, err := st.GetArrInstance(r.Context(), id)
		if err != nil {
			http.Error(w, "instance not found", http.StatusNotFound)
			return
		}
		if inst.Kind != "sonarr" && inst.Kind != "radarr" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "unknown kind"})
			return
		}
		if err := arr.New(arr.Kind(inst.Kind), inst.URL, inst.APIKey).Ping(r.Context()); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func listQbitInstances(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := st.ListQbitInstances(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := make([]qbitInstanceDTO, 0, len(rows))
		for _, row := range rows {
			out = append(out, qbitInstanceDTO{ID: row.ID, Name: row.Name, URL: row.URL, Username: row.Username, HasPassword: row.Password != ""})
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func upsertQbitInstance(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var d qbitInstanceDTO
		if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		id, err := st.UpsertQbitInstance(r.Context(), store.QbitInstanceRow{
			ID: d.ID, Name: d.Name, URL: d.URL, Username: d.Username, Password: d.Password,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		d.ID = id
		d.Password = ""
		writeJSON(w, http.StatusOK, d)
	}
}

func deleteQbitInstance(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		if err := st.DeleteQbitInstance(r.Context(), id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func testQbitCredentials() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			URL      string `json:"url"`
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		client, err := qbit.New(body.URL, body.Username, body.Password)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if err := client.Login(r.Context()); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func testQbitInstance(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		row, err := st.GetQbitInstance(r.Context(), id)
		if err != nil {
			http.Error(w, "instance not found", http.StatusNotFound)
			return
		}
		client, err := qbit.New(row.URL, row.Username, row.Password)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if err := client.Login(r.Context()); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func listProfiles(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var (
			rows []store.ProfileRow
			err  error
		)
		if r.URL.Query().Get("includeDeleted") == "true" {
			rows, err = st.ListProfilesIncludingDeleted(r.Context())
		} else {
			rows, err = st.ListProfiles(r.Context())
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := make([]profileDTO, 0, len(rows))
		for _, row := range rows {
			out = append(out, profileRowToDTO(row))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func upsertProfile(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var d profileDTO
		if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}

		bitratesJSON := "{}"
		if len(d.AudioBitratesByChannels) > 0 {
			cleaned := make(map[string]int, len(d.AudioBitratesByChannels))
			for k, v := range d.AudioBitratesByChannels {
				if v > 0 {
					cleaned[k] = v
				}
			}
			if b, err := json.Marshal(cleaned); err == nil {
				bitratesJSON = string(b)
			}
		}
		id, err := st.UpsertProfile(r.Context(), store.ProfileRow{
			ID: d.ID, Name: d.Name, Encoder: d.Encoder,
			EncoderPreset: d.EncoderPreset, EncoderProfile: d.EncoderProfile,
			EncoderTune: d.EncoderTune, EncoderLevel: d.EncoderLevel,
			RateControl:  strings.ToLower(strings.TrimSpace(d.RateControl)),
			Quality:      d.Quality,
			VideoBitrate: d.VideoBitrate,
			MaxWidth:     d.MaxWidth, MaxHeight: d.MaxHeight,
			AudioEncoder: d.AudioEncoder,
			AudioBitrate: d.AudioBitrate, AudioMixdown: d.AudioMixdown,
			AudioBitratesByChannels: bitratesJSON,
			SubtitleCopy:            d.SubtitleCopy, TwoPass: d.TwoPass,
			ContainerFormat: d.ContainerFormat, ExtraArgs: d.ExtraArgs,
			Framerate:              d.Framerate,
			SkipCodecs:             strings.ToLower(strings.TrimSpace(d.SkipCodecs)),
			SkipBitrateMBPerHour:   d.SkipBitrateMBPerHour,
			SkipBitrateUnit:        d.SkipBitrateUnit,
			SkipFileSizeMB:         d.SkipFileSizeMB,
			SkipDurationMinutes:    d.SkipDurationMinutes,
			SkipHeightPx:           d.SkipHeightPx,
			SkipHDR:                d.SkipHDR,
			BloatPolicy:            normalizeBloatPolicy(d.BloatPolicy),
			BloatRetryMax:          clamp(d.BloatRetryMax, 0, 10),
			BloatRetryStep:         clamp(d.BloatRetryStep, 1, 20),
			BloatMinSavingsPercent: clamp(d.BloatMinSavingsPercent, 0, 50),
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		d.ID = id
		writeJSON(w, http.StatusOK, d)
	}
}

func deleteProfile(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		if err := st.DeleteProfile(r.Context(), id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func listAllArrTags(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		instances, err := st.ListArrInstances(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := []instanceTagDTO{}
		for _, inst := range instances {
			if inst.Kind != "sonarr" && inst.Kind != "radarr" {
				continue
			}
			tags, err := arr.New(arr.Kind(inst.Kind), inst.URL, inst.APIKey).Tags(r.Context())
			if err != nil {
				continue
			}
			for _, t := range tags {
				out = append(out, instanceTagDTO{InstanceID: inst.ID, InstanceName: inst.Name, Kind: inst.Kind, TagID: t.ID, TagLabel: t.Label})
			}
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func listTagMappingsHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := st.ListTagMappings(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := make([]tagMappingDTO, 0, len(rows))
		for _, row := range rows {
			out = append(out, tagMappingDTO{ID: row.ID, ArrKind: row.ArrKind, TagID: row.TagID, TagLabel: row.TagLabel, ProfileID: row.ProfileID})
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func createTagMappingHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var d tagMappingDTO
		if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		if d.ArrKind != "sonarr" && d.ArrKind != "radarr" && d.ArrKind != "both" {
			http.Error(w, "arrKind must be sonarr, radarr, or both", http.StatusBadRequest)
			return
		}
		newID, err := st.CreateTagMapping(r.Context(), store.TagMappingRow{
			ArrKind: d.ArrKind, TagID: d.TagID, TagLabel: d.TagLabel, ProfileID: d.ProfileID,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		d.ID = newID
		writeJSON(w, http.StatusCreated, d)
	}
}

func deleteTagMappingHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		if err := st.DeleteTagMapping(r.Context(), id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

type debugInfo struct {
	HBVersion      string   `json:"hbVersion"`
	HBFound        bool     `json:"hbFound"`
	Encoders       []string `json:"encoders"`
	VAAPIAvailable bool     `json:"vaapiAvailable"`
	QSVAvailable   bool     `json:"qsvAvailable"`
	NVENCAvailable bool     `json:"nvencAvailable"`
	Platform       string   `json:"platform"`
	Arch           string   `json:"arch"`
}

func buildDebugInfo() debugInfo {
	caps := handbrake.QueryCaps()
	encoderNames := make([]string, len(caps.Encoders))
	for i, e := range caps.Encoders {
		encoderNames[i] = e.Name
	}

	hbVer := handbrake.VersionString()
	hbFound := !strings.HasPrefix(hbVer, "(HandBrakeCLI not found)")

	vaapiAvail := false
	if entries, err := os.ReadDir("/dev/dri"); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "renderD") {
				vaapiAvail = true
				break
			}
		}
	}

	qsvAvail := false
	if vaapiAvail {
		_, err := os.Stat("/sys/module/i915")
		if err == nil {
			qsvAvail = true
		}
		if !qsvAvail {
			_, err = os.Stat("/sys/module/xe")
			qsvAvail = err == nil
		}
	}

	nvencAvail := false
	if _, err := os.Stat("/dev/nvidia0"); err == nil {
		nvencAvail = true
	}
	if !nvencAvail {
		if _, err := exec.LookPath("nvidia-smi"); err == nil {
			nvencAvail = true
		}
	}

	return debugInfo{
		HBVersion:      hbVer,
		HBFound:        hbFound,
		Encoders:       encoderNames,
		VAAPIAvailable: vaapiAvail,
		QSVAvailable:   qsvAvail,
		NVENCAvailable: nvencAvail,
		Platform:       runtime.GOOS,
		Arch:           runtime.GOARCH,
	}
}

func workerStatus(wk workerClient, st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ids := wk.EncodingJobIDs()
		t := wk.LastTickAt()
		var lastTick *string
		if !t.IsZero() {
			s := t.UTC().Format(time.RFC3339)
			lastTick = &s
		}
		var first int64
		if len(ids) > 0 {
			first = ids[0]
		}
		cfg, _ := st.LoadAppSettings(r.Context())
		writeJSON(w, http.StatusOK, map[string]any{
			"isEncoding":         len(ids) > 0,
			"encodingJobId":      first,
			"encodingJobIds":     ids,
			"progress":           wk.AllProgress(),
			"lastTickAt":         lastTick,
			"window":             wk.WindowStatus(r.Context()),
			"maxParallelEncodes": cfg.MaxParallelEncodes,
			"paused":             cfg.EncodingPaused,
		})
	}
}

func workerSetPaused(wk workerClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Paused bool `json:"paused"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		cancelled, err := wk.SetPaused(r.Context(), body.Paused)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"paused":    body.Paused,
			"cancelled": cancelled,
		})
	}
}

var _ = context.Background

func workerProgressSSE(wk workerClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")

		ctx := r.Context()

		send := func(ev job.ProgressEvent) {
			b, _ := json.Marshal(ev)
			_, _ = w.Write([]byte("event: progress\ndata: "))
			_, _ = w.Write(b)
			_, _ = w.Write([]byte("\n\n"))
			flusher.Flush()
		}

		if cur := wk.CurrentProgress(); cur.JobID != 0 {
			send(cur)
		} else {
			_, _ = w.Write([]byte("event: idle\ndata: {}\n\n"))
			flusher.Flush()
		}

		ch, cancel := wk.Subscribe()
		defer cancel()

		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				if ev.JobID == 0 {
					_, _ = w.Write([]byte("event: idle\ndata: {}\n\n"))
					flusher.Flush()
					continue
				}
				send(ev)
			case <-ticker.C:
				_, _ = w.Write([]byte(": keepalive\n\n"))
				flusher.Flush()
			}
		}
	}
}

type jobDTO struct {
	ID            int64   `json:"id"`
	ArrKind       string  `json:"arrKind"`
	ArrInstanceID int64   `json:"arrInstanceId"`
	ArrItemID     int64   `json:"arrItemId"`
	ArrParentID   int64   `json:"arrParentId"`
	Title         string  `json:"title"`
	FilePath      string  `json:"filePath"`
	FileSize      int64   `json:"fileSize"`
	DownloadID    string  `json:"downloadId"`
	ProfileID     *int64  `json:"profileId"`
	Status        string  `json:"status"`
	Error         string  `json:"error,omitempty"`
	EncodeLog     string  `json:"encodeLog,omitempty"`
	RefreshError  string  `json:"refreshError,omitempty"`
	Attempts      int64   `json:"attempts"`
	CreatedAt     string  `json:"createdAt"`
	UpdatedAt     string  `json:"updatedAt"`
	StartedAt     *string `json:"startedAt,omitempty"`
	FinishedAt    *string `json:"finishedAt,omitempty"`
	OriginalSize  *int64  `json:"originalSize,omitempty"`
	FinalSize     *int64  `json:"finalSize,omitempty"`
	Source        string  `json:"source"`
}

func rowToJobDTO(row store.JobRow) jobDTO {
	const ts = "2006-01-02T15:04:05Z07:00"
	d := jobDTO{
		ID: row.ID, ArrKind: row.ArrKind, ArrInstanceID: row.ArrInstanceID,
		ArrItemID: row.ArrItemID, ArrParentID: row.ArrParentID,
		Title: row.Title, FilePath: row.FilePath, FileSize: row.FileSize,
		DownloadID: row.DownloadID, Status: row.Status, Error: row.Error,
		EncodeLog: row.EncodeLog, RefreshError: row.RefreshError, Attempts: row.Attempts,
		CreatedAt: row.CreatedAt.Format(ts), UpdatedAt: row.UpdatedAt.Format(ts),
		Source: row.Source,
	}
	if d.Source == "" {
		d.Source = "webhook"
	}
	if row.ProfileID.Valid {
		v := row.ProfileID.Int64
		d.ProfileID = &v
	}
	if row.StartedAt.Valid {
		v := row.StartedAt.Time.Format(ts)
		d.StartedAt = &v
	}
	if row.FinishedAt.Valid {
		v := row.FinishedAt.Time.Format(ts)
		d.FinishedAt = &v
	}
	if row.OriginalSize.Valid {
		v := row.OriginalSize.Int64
		d.OriginalSize = &v
	}
	if row.FinalSize.Valid {
		v := row.FinalSize.Int64
		d.FinalSize = &v
	}
	return d
}

func splitNonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

type jobsPageDTO struct {
	Total  int64    `json:"total"`
	Limit  int      `json:"limit"`
	Offset int      `json:"offset"`
	Jobs   []jobDTO `json:"jobs"`
}

func listJobs(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		opts := store.JobListOptions{
			Statuses:  splitNonEmpty(q.Get("status")),
			Kinds:     splitNonEmpty(q.Get("kind")),
			Search:    q.Get("q"),
			Ascending: q.Get("order") == "asc",
			SortBy:    q.Get("sort"),
		}
		if v := q.Get("profileId"); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				opts.ProfileID = n
			}
		}
		if v := q.Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				opts.Limit = n
			}
		}
		if v := q.Get("offset"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				opts.Offset = n
			}
		}
		rows, total, err := st.ListJobs(r.Context(), opts)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jobs := make([]jobDTO, 0, len(rows))
		for _, row := range rows {
			jobs = append(jobs, rowToJobDTO(row))
		}
		limit := opts.Limit
		if limit <= 0 {
			limit = 50
		} else if limit > 500 {
			limit = 500
		}
		writeJSON(w, http.StatusOK, jobsPageDTO{
			Total: total, Limit: limit, Offset: opts.Offset, Jobs: jobs,
		})
	}
}

func retryAllFailed(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, err := st.RetryAllFailed(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]int64{"retried": n})
	}
}

func retryJob(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		if err := st.RetryJob(r.Context(), id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		row, err := st.GetJob(r.Context(), id)
		if err != nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, rowToJobDTO(*row))
	}
}

func cancelJob(st *store.Store, wk workerClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		row, err := st.GetJob(r.Context(), id)
		if err != nil {
			http.Error(w, "job not found", http.StatusNotFound)
			return
		}
		if row.Status != string(job.StatusEncoding) {
			http.Error(w, "job is not encoding", http.StatusConflict)
			return
		}
		if !wk.CancelEncoding(id) {
			http.Error(w, "job is not currently running on this worker", http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "cancelling"})
	}
}

type jobDebugDTO struct {
	JobID            int64              `json:"jobId"`
	Status           string             `json:"status"`
	DownloadID       string             `json:"downloadId"`
	DownloadIDLength int                `json:"downloadIdLength"`
	FilePath         string             `json:"filePath"`
	Attempts         int64              `json:"attempts"`
	Qbit             jobDebugQbitDTO    `json:"qbit"`
	WaitingForSeed   int64              `json:"waitingForSeedCount"`
	SeedCheckLimit   int                `json:"seedCheckBatchLimit"`
	StalledReason    string             `json:"stalledReason,omitempty"`
	Encode           *jobDebugEncodeDTO `json:"encode,omitempty"`
}

type jobDebugEncodeDTO struct {
	ProfileID       *int64   `json:"profileId,omitempty"`
	ProfileName     string   `json:"profileName,omitempty"`
	ProfileEncoder  string   `json:"profileEncoder,omitempty"`
	OriginalBytes   *int64   `json:"originalBytes,omitempty"`
	FinalBytes      *int64   `json:"finalBytes,omitempty"`
	SavedBytes      *int64   `json:"savedBytes,omitempty"`
	SavedPercent    *float64 `json:"savedPercent,omitempty"`
	StartedAt       string   `json:"startedAt,omitempty"`
	FinishedAt      string   `json:"finishedAt,omitempty"`
	DurationSeconds *int64   `json:"durationSeconds,omitempty"`
	Error           string   `json:"error,omitempty"`
	RefreshError    string   `json:"refreshError,omitempty"`
}

type jobDebugQbitDTO struct {
	Configured  bool               `json:"configured"`
	URL         string             `json:"url,omitempty"`
	Reachable   bool               `json:"reachable"`
	LoginError  string             `json:"loginError,omitempty"`
	Lookup      *jobDebugLookupDTO `json:"lookup,omitempty"`
	LookupError string             `json:"lookupError,omitempty"`
}

type jobDebugLookupDTO struct {
	Found    bool    `json:"found"`
	Hash     string  `json:"hash,omitempty"`
	Name     string  `json:"name,omitempty"`
	State    string  `json:"state,omitempty"`
	Progress float64 `json:"progress,omitempty"`
	Category string  `json:"category,omitempty"`
	SavePath string  `json:"savePath,omitempty"`
}

func debugJob(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		row, err := st.GetJob(r.Context(), id)
		if err != nil {
			http.Error(w, "job not found", http.StatusNotFound)
			return
		}

		out := jobDebugDTO{
			JobID:            row.ID,
			Status:           row.Status,
			DownloadID:       row.DownloadID,
			DownloadIDLength: len(row.DownloadID),
			FilePath:         row.FilePath,
			Attempts:         row.Attempts,
			SeedCheckLimit:   job.SeedCheckBatchLimit,
		}
		if stats, err := st.GetJobStats(r.Context()); err == nil {
			out.WaitingForSeed = stats.WaitingForSeed
		}

		if row.StartedAt.Valid || row.OriginalSize.Valid || row.FinalSize.Valid ||
			row.Error != "" || row.RefreshError != "" {
			enc := &jobDebugEncodeDTO{
				Error:        row.Error,
				RefreshError: row.RefreshError,
			}
			if row.ProfileID.Valid {
				pid := row.ProfileID.Int64
				enc.ProfileID = &pid
				if p, err := st.GetProfile(r.Context(), pid); err == nil && p != nil {
					enc.ProfileName = p.Name
					enc.ProfileEncoder = p.Encoder
				}
			}
			if row.OriginalSize.Valid {
				v := row.OriginalSize.Int64
				enc.OriginalBytes = &v
			}
			if row.FinalSize.Valid {
				v := row.FinalSize.Int64
				enc.FinalBytes = &v
			}
			if row.OriginalSize.Valid && row.FinalSize.Valid {
				saved := row.OriginalSize.Int64 - row.FinalSize.Int64
				enc.SavedBytes = &saved
				if row.OriginalSize.Int64 > 0 {
					pct := float64(saved) / float64(row.OriginalSize.Int64) * 100
					enc.SavedPercent = &pct
				}
			}
			const ts = "2006-01-02T15:04:05Z07:00"
			if row.StartedAt.Valid {
				enc.StartedAt = row.StartedAt.Time.Format(ts)
			}
			if row.FinishedAt.Valid {
				enc.FinishedAt = row.FinishedAt.Time.Format(ts)
			}
			if row.StartedAt.Valid && row.FinishedAt.Valid {
				d := int64(row.FinishedAt.Time.Sub(row.StartedAt.Time).Seconds())
				enc.DurationSeconds = &d
			}
			out.Encode = enc
		}

		if row.Status == string(job.StatusWaitingForHardlink) {
			if n, err := job.HardlinkCount(row.FilePath); err != nil {
				out.StalledReason = fmt.Sprintf("Can't stat the library file (%v). The worker will release this job to 'ready' on the next tick so the encode surfaces the error.", err)
			} else if n > 1 {
				out.StalledReason = fmt.Sprintf("Library file still has %d hardlinks — qBittorrent is likely still seeding its download copy. The job releases to 'ready' once the count drops to 1 (qBit auto-remove on completion).", n)
			} else {
				out.StalledReason = "Library file has a single hardlink — the worker should release this job to 'ready' on the next tick (within ~30s)."
			}
			writeJSON(w, http.StatusOK, out)
			return
		}

		qbitRow, err := st.FirstQbitInstance(r.Context())
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				out.Qbit.Configured = false
				if row.Status == string(job.StatusWaitingForSeed) {
					out.StalledReason = "qBit is not configured — jobs cannot leave waiting_for_seed until qBit is added in Settings."
				}
				writeJSON(w, http.StatusOK, out)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out.Qbit.Configured = true
		out.Qbit.URL = qbitRow.URL

		client, err := qbit.New(qbitRow.URL, qbitRow.Username, qbitRow.Password)
		if err != nil {
			out.Qbit.LoginError = err.Error()
			writeJSON(w, http.StatusOK, out)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		if err := client.Login(ctx); err != nil {
			out.Qbit.LoginError = err.Error()
			writeJSON(w, http.StatusOK, out)
			return
		}
		out.Qbit.Reachable = true

		if row.DownloadID == "" {
			out.StalledReason = "Job has no downloadId recorded — the worker should transition it to 'ready' on the next tick (within ~30s)."
			writeJSON(w, http.StatusOK, out)
			return
		}
		t, err := client.TorrentByHash(ctx, row.DownloadID)
		if err != nil {
			out.Qbit.LookupError = err.Error()
			out.StalledReason = "qBit lookup failed; job will retry next tick."
			writeJSON(w, http.StatusOK, out)
			return
		}
		if t == nil {
			out.Qbit.Lookup = &jobDebugLookupDTO{Found: false}
			if row.Status == string(job.StatusWaitingForSeed) {
				out.StalledReason = "qBit does not have this hash — the next worker tick (within ~30s) should transition this job to 'ready'. If it doesn't, the worker may be wedged; restart the container."
			}
		} else {
			out.Qbit.Lookup = &jobDebugLookupDTO{
				Found: true, Hash: t.Hash, Name: t.Name, State: t.State,
				Progress: t.Progress, Category: t.Category, SavePath: t.SavePath,
			}
			if row.Status == string(job.StatusWaitingForSeed) {
				out.StalledReason = fmt.Sprintf(
					"qBit still holds this torrent (state=%q, category=%q). Recodarr only releases the job once qBit removes the torrent — configure qBit to auto-remove on seeding completion (Options → BitTorrent).",
					t.State, t.Category,
				)
			}
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func deleteJob(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		if err := st.DeleteJob(r.Context(), id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func deleteTerminalJobs(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		statuses := splitNonEmpty(r.URL.Query().Get("status"))
		n, err := st.DeleteTerminalJobs(r.Context(), statuses)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]int64{"deleted": n})
	}
}

type bulkIDsBody struct {
	IDs []int64 `json:"ids"`
}

func bulkDeleteJobs(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body bulkIDsBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		n, err := st.DeleteJobsByIDs(r.Context(), body.IDs)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]int64{"deleted": n})
	}
}

func bulkRetryJobs(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body bulkIDsBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		n, err := st.RetryJobsByIDs(r.Context(), body.IDs)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]int64{"retried": n})
	}
}

func bulkSetJobProfile(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IDs       []int64 `json:"ids"`
			ProfileID int64   `json:"profileId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		var pid sql.NullInt64
		if body.ProfileID > 0 {
			pid = sql.NullInt64{Int64: body.ProfileID, Valid: true}
		}
		n, err := st.SetJobsProfile(r.Context(), body.IDs, pid)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]int64{"updated": n})
	}
}

func testAgent(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			URL   string `json:"url"`
			Token string `json:"token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		url := strings.TrimSpace(body.URL)
		token := body.Token
		if url == "" || token == "" {
			cfg, err := st.LoadAppSettings(r.Context())
			if err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			if url == "" {
				url = cfg.AgentURL
			}
			if token == "" {
				token = cfg.AgentToken
			}
		}
		if url == "" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "no agent URL configured"})
			return
		}
		if token == "" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "no agent token configured"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
		defer cancel()
		hs, err := agent.NewClient(url, token).Ping(ctx)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"version": hs.Version,
			"hb":      hs.HandbrakeVersion,
			"slots":   hs.SlotsMax,
			"active":  hs.JobsActive,
			"localFs": hs.LocalFS,
		})
	}
}
