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

	"github.com/sratabix/recodarr/internal/agent"
	"github.com/sratabix/recodarr/internal/handbrake"
	"github.com/sratabix/recodarr/internal/health"
	"github.com/sratabix/recodarr/internal/job"
	"github.com/sratabix/recodarr/internal/logging"
	"github.com/sratabix/recodarr/internal/qbit"
	"github.com/sratabix/recodarr/internal/arr"
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
	APIKey        string `json:"apiKey,omitempty"`        // write-only; never returned
	Enabled       bool   `json:"enabled"`
	WebhookSecret string `json:"webhookSecret,omitempty"` // write-only; copyable URL is enough
	HasAPIKey     bool   `json:"hasApiKey"`
	HasSecret     bool   `json:"hasWebhookSecret"`
}

func (d arrInstanceDTO) toRow() store.ArrInstanceRow {
	return store.ArrInstanceRow{
		ID: d.ID, Kind: d.Kind, Name: d.Name, URL: d.URL, APIKey: d.APIKey,
		Enabled: d.Enabled, WebhookSecret: d.WebhookSecret,
	}
}

// arrRowToDTO redacts secrets. The SPA never receives api_key or webhook_secret;
// it only learns whether they're set. Saving the form with a blank field means
// "leave as is" (see Store.UpdateArrInstance).
func arrRowToDTO(r store.ArrInstanceRow) arrInstanceDTO {
	return arrInstanceDTO{
		ID: r.ID, Kind: r.Kind, Name: r.Name, URL: r.URL,
		Enabled:   r.Enabled,
		HasAPIKey: r.APIKey != "",
		HasSecret: r.WebhookSecret != "",
	}
}

type qbitInstanceDTO struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	Username    string `json:"username"`
	Password    string `json:"password,omitempty"` // write-only
	HasPassword bool   `json:"hasPassword"`
}

type profileDTO struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	Encoder         string `json:"encoder"`
	EncoderPreset   string `json:"encoderPreset"`
	EncoderProfile  string `json:"encoderProfile"`
	EncoderTune     string `json:"encoderTune"`
	EncoderLevel    string `json:"encoderLevel"`
	RateControl     string `json:"rateControl"` // "crf" | "abr"
	Quality         int    `json:"quality"`
	VideoBitrate    int    `json:"videoBitrate"` // kbps; only used when rateControl=abr
	MaxWidth        int    `json:"maxWidth"`
	MaxHeight       int    `json:"maxHeight"`
	AudioEncoder    string `json:"audioEncoder"`
	AudioBitrate    int    `json:"audioBitrate"`
	AudioMixdown    string `json:"audioMixdown"`
	SubtitleCopy    bool   `json:"subtitleCopy"`
	TwoPass         bool   `json:"twoPass"`
	ContainerFormat string `json:"containerFormat"`
	ExtraArgs       string `json:"extraArgs"`
	Framerate       string `json:"framerate"`
	// Pre-encode filters; zero/empty = inactive.
	SkipCodecs           string `json:"skipCodecs"`
	SkipBitrateMBPerHour int    `json:"skipBitrateMBPerHour"` // value; unit determines interpretation
	SkipBitrateUnit      string `json:"skipBitrateUnit"`      // "mb_per_hour" (default) | "kbps"
	SkipFileSizeMB       int    `json:"skipFileSizeMB"`
	SkipDurationMinutes  int    `json:"skipDurationMinutes"`
	SkipHeightPx         int    `json:"skipHeightPx"`
	SkipHDR              bool   `json:"skipHDR"`
	// Post-encode size guard.
	BloatPolicy            string `json:"bloatPolicy"`
	BloatRetryMax          int    `json:"bloatRetryMax"`
	BloatRetryStep         int    `json:"bloatRetryStep"`
	BloatMinSavingsPercent int    `json:"bloatMinSavingsPercent"`
}

func profileRowToDTO(r store.ProfileRow) profileDTO {
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
		SubtitleCopy: r.SubtitleCopy, TwoPass: r.TwoPass,
		ContainerFormat: r.ContainerFormat, ExtraArgs: r.ExtraArgs,
		Framerate: r.Framerate,
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
	WaitingForSeed  int64 `json:"waitingForSeed"`
	Ready           int64 `json:"ready"`
	Encoding        int64 `json:"encoding"`
	Done            int64 `json:"done"`
	Failed          int64 `json:"failed"`
	Skipped         int64 `json:"skipped"`
	TotalSavedBytes int64 `json:"totalSavedBytes"`
}

// LogLevelSetter is the surface area the settings handler needs to push live
// log-level changes into the logging subsystem without depending on its
// concrete types. Satisfied by *logging.Sinks.
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

	// Lightweight health snapshot for the dashboard: external service
	// reachability, missing-config warnings. Cached for ~30s in the checker.
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
			WaitingForSeed:  stats.WaitingForSeed,
			Ready:           stats.Ready,
			Encoding:        stats.Encoding,
			Done:            stats.Done,
			Failed:          stats.Failed,
			Skipped:         stats.Skipped,
			TotalSavedBytes: stats.TotalSavedBytes,
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
		r.Post("/{id}/library/queue", queueArrLibrary(st))
		// Reveal endpoint: returns the auto-generated webhook secret so the user
		// can copy it after saving. The user is the single admin and is already
		// authenticated, so this is no weaker than the SQLite file itself.
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

// --- settings ---

func getSettings(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m, err := st.GetAllSettings(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Write-only secrets follow the same pattern as webhook_secret / qbit
		// password / *arr api_key: strip from response, emit a boolean
		// presence flag so the UI can show "(stored, leave blank to keep)".
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
		// Blank agent_token means "keep what's stored" — drop from the write
		// set so we don't clobber. To explicitly clear, send a sentinel like
		// "clear" (intentionally undocumented; user can clear via the UI's
		// disable toggle which sets agent_enabled=false).
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

// normalizeBloatPolicy gates the small enum we accept on profile writes. Any
// unknown value collapses to "off" so we don't store junk in the DB and so an
// older client sending a value we removed in a future version stays harmless.
func normalizeBloatPolicy(s string) string {
	switch s {
	case "keep_original", "retry_higher_crf":
		return s
	default:
		return "off"
	}
}

// clamp keeps a numeric setting inside a sensible range without rejecting the
// whole request. Out-of-range values get pulled to the nearest bound rather
// than triggering a 400; the user usually meant "as much / as little as
// possible" anyway.
func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// isValidOutputSuffix gates the output_suffix setting. We're strict on purpose:
// the suffix becomes part of every filename Recodarr writes, so it can't contain
// path separators, leading/trailing dots (which would create hidden files or
// double-dot stems), or whitespace. Empty is rejected because the toggle is
// independent — disable via output_suffix_enabled, not by blanking the value.
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

// --- arr instance handlers ---

func listArrInstances(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := st.ListArrInstances(r.Context())
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

// --- library backfill ---

type libraryItemDTO struct {
	ItemID      int64  `json:"itemId"` // seriesId / movieId
	Title       string `json:"title"`
	Path        string `json:"path"`
	TagID       int64  `json:"tagId"` // first matching tag (ties resolved by mapping order)
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
	NoMappings bool             `json:"noMappings"` // true when this instance has no tag→profile mappings configured
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

		// Build tagID → first matching mapping. ListTagMappingsByKind returns
		// rows in id order, so a tag carried by multiple mappings deterministically
		// resolves to the oldest one — matches the webhook precedence.
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
		// Tag labels: the REST API returns tag IDs, but the SPA renders the label.
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

type queueLibraryRequest struct {
	ItemIDs []int64 `json:"itemIds"`
}

type queueLibraryResponse struct {
	Inserted int      `json:"inserted"`
	Skipped  int      `json:"skipped"` // duplicate or unprocessable files
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

		// Re-derive the eligible set server-side: clients can only queue what
		// the catalogue currently exposes. Defends against stale UI state
		// trying to queue items whose tag was removed.
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
			tagsJSON, _ := json.Marshal([]string{meta.tagLabel})
			for _, f := range files {
				clean, err := sanitizeMediaPath(f.Path)
				if err != nil {
					resp.Skipped++
					resp.Errors = append(resp.Errors, fmt.Sprintf("file %s: %v", f.Path, err))
					continue
				}
				jr := store.JobRow{
					ArrKind:       inst.Kind,
					ArrInstanceID: inst.ID,
					ArrItemID:     f.ID,
					ArrParentID:   pid,
					Title:         meta.title,
					FilePath:      clean,
					FileSize:      f.Size,
					ProfileID:     sql.NullInt64{Int64: meta.mapping.ProfileID, Valid: true},
					Status:        string(job.StatusReady),
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

// --- tag profile handlers ---

// --- qbit handlers ---

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

// testQbitCredentials tests credentials supplied inline in the request body (no saved instance needed).
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

// --- profiles ---

func listProfiles(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := st.ListProfiles(r.Context())
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
			SubtitleCopy: d.SubtitleCopy, TwoPass: d.TwoPass,
			ContainerFormat: d.ContainerFormat, ExtraArgs: d.ExtraArgs,
			Framerate:            d.Framerate,
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

// --- all tags ---

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

// --- tag mappings ---

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

// --- debug ---

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

// --- worker ---

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
			"encodingJobId":      first, // back-compat: first in-flight job
			"encodingJobIds":     ids,
			"progress":           wk.AllProgress(),
			"lastTickAt":         lastTick,
			"window":             wk.WindowStatus(r.Context()),
			"maxParallelEncodes": cfg.MaxParallelEncodes,
			"paused":             cfg.EncodingPaused,
		})
	}
}

// workerSetPaused flips the master encoding-paused flag. When pausing, the
// worker also cancels every in-flight encode and re-queues them. Body:
// {"paused": true|false}. Response: {"paused": <bool>, "cancelled": <int>}.
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

var _ = context.Background // keep context imported for the workerClient interface

// workerProgressSSE streams encode progress as Server-Sent Events. Sends an event whenever
// the encoding worker reports new progress, plus a keepalive comment every 15s to keep the
// connection open through proxies.
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
		w.Header().Set("X-Accel-Buffering", "no") // disable nginx proxy buffering

		// Chi's middleware.Timeout (30s default) would kill the stream — undo it for this handler.
		ctx := r.Context()

		send := func(ev job.ProgressEvent) {
			b, _ := json.Marshal(ev)
			_, _ = w.Write([]byte("event: progress\ndata: "))
			_, _ = w.Write(b)
			_, _ = w.Write([]byte("\n\n"))
			flusher.Flush()
		}

		// Send the current state immediately so the client doesn't wait for the next change.
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

// --- jobs ---

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

// splitNonEmpty splits a comma-separated query-param value into a clean
// list, dropping empty fragments. Returns nil for "" so the caller can use
// len(...) == 0 to mean "no filter".
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
	Total  int64     `json:"total"`
	Limit  int       `json:"limit"`
	Offset int       `json:"offset"`
	Jobs   []jobDTO  `json:"jobs"`
}

func listJobs(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		opts := store.JobListOptions{
			Statuses: splitNonEmpty(q.Get("status")),
			Kinds:    splitNonEmpty(q.Get("kind")),
			Search:   q.Get("q"),
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

// jobDebugDTO bundles the per-job diagnostics the UI needs to figure out why a
// job is stuck — particularly why a waiting_for_seed job hasn't transitioned.
// Everything here is read-only and computed live; nothing is persisted.
type jobDebugDTO struct {
	JobID            int64             `json:"jobId"`
	Status           string            `json:"status"`
	DownloadID       string            `json:"downloadId"`
	DownloadIDLength int               `json:"downloadIdLength"`
	FilePath         string            `json:"filePath"`
	Attempts         int64             `json:"attempts"`
	Qbit             jobDebugQbitDTO   `json:"qbit"`
	WaitingForSeed   int64             `json:"waitingForSeedCount"`
	SeedCheckLimit   int               `json:"seedCheckBatchLimit"`
	StalledReason    string            `json:"stalledReason,omitempty"`
	Encode           *jobDebugEncodeDTO `json:"encode,omitempty"`
}

// jobDebugEncodeDTO carries post-encode info for terminal jobs (done/failed/
// skipped). Populated whenever original_size/final_size or an error/skip
// reason exists on the row.
type jobDebugEncodeDTO struct {
	ProfileID       *int64  `json:"profileId,omitempty"`
	ProfileName     string  `json:"profileName,omitempty"`
	ProfileEncoder  string  `json:"profileEncoder,omitempty"`
	OriginalBytes   *int64  `json:"originalBytes,omitempty"`
	FinalBytes      *int64  `json:"finalBytes,omitempty"`
	SavedBytes      *int64  `json:"savedBytes,omitempty"`
	SavedPercent    *float64 `json:"savedPercent,omitempty"`
	StartedAt       string  `json:"startedAt,omitempty"`
	FinishedAt      string  `json:"finishedAt,omitempty"`
	DurationSeconds *int64  `json:"durationSeconds,omitempty"`
	Error           string  `json:"error,omitempty"`
	RefreshError    string  `json:"refreshError,omitempty"`
}

type jobDebugQbitDTO struct {
	Configured   bool                 `json:"configured"`
	URL          string               `json:"url,omitempty"`
	Reachable    bool                 `json:"reachable"`
	LoginError   string               `json:"loginError,omitempty"`
	Lookup       *jobDebugLookupDTO   `json:"lookup,omitempty"`
	LookupError  string               `json:"lookupError,omitempty"`
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

		// Encode info: surfaced whenever the job has been worked at least
		// once. The dialog already displays the qBit block; this gives the
		// "what actually happened" half once the job has reached a terminal
		// (or in-progress) state.
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
		// Optional ?status=done,failed,skipped — empty/missing keeps the old
		// default of {done, failed, skipped}. Unknown values are dropped by
		// the store layer.
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

// bulkSetJobProfile reassigns profile_id on the listed jobs. Pass profileId=0
// (or omit) to clear. In-flight encodes are skipped server-side.
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

// testAgent dials the configured remote agent server-side so the SPA never
// has to handle the bearer token. Accepts an optional inline payload to test
// values the user hasn't saved yet (typical "Test connection" UX).
func testAgent(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			URL   string `json:"url"`
			Token string `json:"token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body) // empty body is fine; fall back to stored

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
		})
	}
}
