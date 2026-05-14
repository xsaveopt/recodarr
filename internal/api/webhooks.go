package api

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/sratabix/recodarr/internal/arr"
	"github.com/sratabix/recodarr/internal/job"
	"github.com/sratabix/recodarr/internal/store"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// processable event types we react to. Sonarr/Radarr versions vary; accept the union.
var processableEvents = map[string]bool{
	"Download":               true,
	"OnDownloadFileImported": true,
	"OnImport":               true,
	"OnDownload":             true,
}

func handleArrWebhook(st *store.Store, kind arr.Kind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		instID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "bad instance id", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		item, err := arr.ParseWebhook(kind, body)
		if err != nil {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		// Auth: every instance has a webhook_secret (auto-generated on insert) and every
		// inbound webhook MUST present it. Constant-time compare to avoid timing oracles.
		inst, instErr := loadArrInstance(r.Context(), st, instID, string(kind))
		if instErr == nil {
			provided := r.Header.Get("X-Webhook-Token")
			if inst.WebhookSecret == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(inst.WebhookSecret)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		if item.EventType == "Test" {
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
			return
		}
		if !processableEvents[item.EventType] {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if instErr != nil {
			respondInstanceError(w, instErr)
			return
		}
		profileID, ok := findTagProfile(r.Context(), st, inst, item.ParentTags)
		if !ok {
			slog.Info("webhook filtered out", "kind", kind, "title", item.ParentTitle, "size", item.Size)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		filePath := item.FilePath
		if filePath == "" {
			filePath = item.ParentPath + "/" + item.RelativePath
		}
		clean, err := sanitizeMediaPath(filePath)
		if err != nil {
			slog.Warn("webhook rejected: bad path", "kind", kind, "path", filePath, "err", err)
			http.Error(w, "bad file path", http.StatusBadRequest)
			return
		}
		filePath = clean
		jr := store.JobRow{
			ArrKind:       string(kind),
			ArrInstanceID: inst.ID,
			ArrItemID:     item.FileID,
			ArrParentID:   item.ParentID,
			Title:         item.ParentTitle,
			FilePath:      filePath,
			FileSize:      item.Size,
			DownloadID:    item.DownloadID,
			ProfileID:     profileID,
			Status:        string(job.StatusWaitingForSeed),
		}
		id, err := enqueueIfNew(r.Context(), st, jr)
		if err != nil {
			slog.Error("enqueue", "kind", kind, "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		slog.Info("job enqueued", "kind", kind, "id", id, "title", jr.Title, "path", jr.FilePath)
		w.WriteHeader(http.StatusAccepted)
	}
}

func loadArrInstance(ctx context.Context, st *store.Store, id int64, expectedKind string) (*store.ArrInstanceRow, error) {
	inst, err := st.GetArrInstance(ctx, id)
	if err != nil {
		return nil, err
	}
	if inst.Kind != expectedKind {
		return nil, errors.New("instance kind mismatch")
	}
	if !inst.Enabled {
		return nil, errors.New("instance disabled")
	}
	return inst, nil
}

func respondInstanceError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "instance not found", http.StatusNotFound)
		return
	}
	http.Error(w, err.Error(), http.StatusBadRequest)
}

// findTagProfile checks the item's tags against global tag→profile mappings for this kind.
// Items with no matching tag are silently skipped (returns false).
func findTagProfile(ctx context.Context, st *store.Store, inst *store.ArrInstanceRow, itemTags []int64) (sql.NullInt64, bool) {
	mappings, err := st.ListTagMappingsByKind(ctx, inst.Kind)
	if err != nil || len(mappings) == 0 {
		return sql.NullInt64{}, false
	}
	m := make(map[int64]int64, len(mappings))
	for _, mp := range mappings {
		m[mp.TagID] = mp.ProfileID
	}
	for _, t := range itemTags {
		if pid, ok := m[t]; ok {
			return sql.NullInt64{Int64: pid, Valid: true}, true
		}
	}
	return sql.NullInt64{}, false
}

// sanitizeMediaPath rejects paths that aren't well-formed absolute file paths.
// We trust *arr to send sensible paths, but the webhook body is attacker-controlled
// if the per-instance secret leaks; this is a cheap defense-in-depth check.
func sanitizeMediaPath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	if strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("null byte in path")
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("not absolute")
	}
	clean := filepath.Clean(p)
	for _, seg := range strings.Split(clean, string(filepath.Separator)) {
		if seg == ".." {
			return "", fmt.Errorf("contains parent-dir segment")
		}
	}
	return clean, nil
}

func enqueueIfNew(ctx context.Context, st *store.Store, jr store.JobRow) (int64, error) {
	exists, err := st.HasActiveJob(ctx, jr.ArrKind, jr.ArrInstanceID, jr.ArrItemID)
	if err != nil {
		return 0, err
	}
	if exists {
		return 0, nil
	}
	return st.InsertJob(ctx, jr)
}
