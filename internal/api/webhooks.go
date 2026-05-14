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

// WebhookBasicAuthUser is the fixed Basic-auth username *arr must send. Pairs
// with the per-instance webhook_secret as the password.
const WebhookBasicAuthUser = "recodarr"

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
			slog.Warn("webhook: bad instance id in URL", "kind", kind, "raw", chi.URLParam(r, "id"))
			http.Error(w, "bad instance id", http.StatusBadRequest)
			return
		}

		// Resolve instance + auth FIRST, before reading the body. We don't want to
		// parse untrusted JSON for unauthenticated callers, and we want to fail-fast
		// when the URL points at a deleted/disabled row.
		inst, instErr := loadArrInstance(r.Context(), st, instID, string(kind))
		if instErr != nil {
			slog.Warn("webhook: instance lookup failed", "kind", kind, "id", instID, "err", instErr)
			respondInstanceError(w, instErr)
			return
		}
		user, pass, ok := r.BasicAuth()
		if !ok ||
			inst.WebhookSecret == "" ||
			subtle.ConstantTimeCompare([]byte(user), []byte(WebhookBasicAuthUser)) != 1 ||
			subtle.ConstantTimeCompare([]byte(pass), []byte(inst.WebhookSecret)) != 1 {
			slog.Warn("webhook: auth failed",
				"kind", kind, "id", instID,
				"hasAuthHeader", ok, "user", user)
			w.Header().Set("WWW-Authenticate", `Basic realm="recodarr"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			slog.Warn("webhook: read body failed", "kind", kind, "id", instID, "err", err)
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		item, err := arr.ParseWebhook(kind, body)
		if err != nil {
			slog.Warn("webhook: parse failed; rejecting payload",
				"kind", kind, "id", instID,
				"err", err,
				"contentType", r.Header.Get("Content-Type"),
				"bodySnippet", snippet(body, 512),
			)
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		slog.Info("webhook received",
			"kind", kind, "id", instID,
			"event", item.EventType,
			"title", item.ParentTitle,
			"size", item.Size,
		)
		if item.EventType == "Test" {
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
			return
		}
		if !processableEvents[item.EventType] {
			slog.Info("webhook: event ignored", "kind", kind, "event", item.EventType)
			w.WriteHeader(http.StatusNoContent)
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
// Match is by tag *label* (string), since *arr webhooks serialize tags as labels.
// Items with no matching tag are silently skipped (returns false).
func findTagProfile(ctx context.Context, st *store.Store, inst *store.ArrInstanceRow, itemTags []string) (sql.NullInt64, bool) {
	mappings, err := st.ListTagMappingsByKind(ctx, inst.Kind)
	if err != nil || len(mappings) == 0 {
		return sql.NullInt64{}, false
	}
	m := make(map[string]int64, len(mappings))
	for _, mp := range mappings {
		m[mp.TagLabel] = mp.ProfileID
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

// snippet returns up to n bytes of body for logging without dumping huge payloads.
func snippet(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "...(truncated)"
	}
	return string(b)
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
