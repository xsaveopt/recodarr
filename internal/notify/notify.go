package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/sratabix/recodarr/internal/store"
)

// notifyClient bounds outbound notify requests so a slow/hanging endpoint
// can't pin a worker goroutine indefinitely.
var notifyClient = &http.Client{Timeout: 10 * time.Second}

// Send fires a notification webhook if configured and the given status matches the user's prefs.
// Compatible with ntfy, Gotify, and any generic JSON webhook.
func Send(ctx context.Context, st *store.Store, title, status, filePath string, originalSize, finalSize int64) {
	cfg, err := st.LoadAppSettings(ctx)
	if err != nil {
		slog.Warn("notify: load settings", "err", err)
		return
	}
	if cfg.NotifyURL == "" {
		return
	}
	switch status {
	case "done":
		if !cfg.NotifyOnDone {
			return
		}
	case "failed":
		if !cfg.NotifyOnFail {
			return
		}
	default:
		return
	}

	var savedBytes int64
	var msg string
	if status == "done" {
		if originalSize > 0 && finalSize > 0 {
			savedBytes = originalSize - finalSize
			msg = fmt.Sprintf("%s encoded — saved %s", title, formatBytes(savedBytes))
		} else {
			msg = fmt.Sprintf("%s encoded", title)
		}
	} else {
		msg = fmt.Sprintf("Failed to encode %s", title)
	}

	payload := map[string]any{
		"title":      "Recodarr",
		"message":    msg,
		"status":     status,
		"filePath":   filePath,
		"savedBytes": savedBytes,
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, "POST", cfg.NotifyURL, bytes.NewReader(body))
	if err != nil {
		slog.Warn("notify: create request", "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := notifyClient.Do(req)
	if err != nil {
		slog.Warn("notify: send", "err", err)
		return
	}
	_ = resp.Body.Close()
	slog.Info("notification sent", "status", status, "title", title, "httpStatus", resp.StatusCode)
}

// SendHealth fires a notification for a health issue transition. transition is
// "opened" when an issue first appears or "resolved" when it clears. The
// caller is expected to dedupe; this function fires every call unless the user
// has disabled NotifyOnHealth.
func SendHealth(ctx context.Context, st *store.Store, source, title, detail, level, transition string) {
	cfg, err := st.LoadAppSettings(ctx)
	if err != nil {
		slog.Warn("notify: load settings", "err", err)
		return
	}
	if cfg.NotifyURL == "" || !cfg.NotifyOnHealth {
		return
	}

	var msg string
	switch transition {
	case "opened":
		msg = title
		if detail != "" {
			msg = title + " — " + detail
		}
	case "resolved":
		msg = "Resolved: " + title
	default:
		return
	}

	payload := map[string]any{
		"title":      "Recodarr",
		"message":    msg,
		"status":     "health",
		"source":     source,
		"level":      level,
		"transition": transition,
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, "POST", cfg.NotifyURL, bytes.NewReader(body))
	if err != nil {
		slog.Warn("notify: create request", "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := notifyClient.Do(req)
	if err != nil {
		slog.Warn("notify: send", "err", err)
		return
	}
	_ = resp.Body.Close()
	slog.Info("health notification sent", "source", source, "transition", transition, "httpStatus", resp.StatusCode)
}

func formatBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	v := float64(n) / 1024
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}
