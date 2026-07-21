package job

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xsaveopt/recodarr/internal/arr"
	"github.com/xsaveopt/recodarr/internal/store"
)

func (w *Worker) runReconcile(ctx context.Context) {
	slog.Info("reconciler started")
	w.reconcile(ctx)
	for {
		interval := w.readReconcileInterval(ctx)
		select {
		case <-ctx.Done():
			slog.Info("reconciler stopped")
			return
		case <-time.After(interval):
			w.reconcile(ctx)
		}
	}
}

func (w *Worker) readReconcileInterval(ctx context.Context) time.Duration {
	cfg, _ := w.store.LoadAppSettings(ctx)
	return time.Duration(cfg.ReconcileIntervalSeconds) * time.Second
}

func (w *Worker) reconcile(ctx context.Context) {
	cfg, err := w.store.LoadAppSettings(ctx)
	if err != nil {
		slog.Error("reconcile: load settings", "err", err)
		return
	}
	instances, err := w.store.ListArrInstances(ctx)
	if err != nil {
		slog.Error("reconcile: list instances", "err", err)
		return
	}
	for _, inst := range instances {
		if !inst.Enabled || (inst.Kind != "sonarr" && inst.Kind != "radarr") {
			continue
		}
		w.reconcileInstance(ctx, cfg, inst)
	}
}

func (w *Worker) reconcileInstance(ctx context.Context, cfg store.AppSettings, inst store.ArrInstanceRow) {
	mappings, err := w.store.ListTagMappingsByKind(ctx, inst.Kind)
	if err != nil {
		slog.Error("reconcile: mappings", "kind", inst.Kind, "instance", inst.ID, "err", err)
		return
	}
	if len(mappings) == 0 {
		return
	}
	mapByTagID := make(map[int64]store.TagMappingRow, len(mappings))
	for _, mp := range mappings {
		if _, ok := mapByTagID[mp.TagID]; !ok {
			mapByTagID[mp.TagID] = mp
		}
	}

	client := arr.New(arr.Kind(inst.Kind), inst.URL, inst.APIKey)
	items, err := client.Library(ctx)
	if err != nil {
		slog.Warn("reconcile: library fetch", "kind", inst.Kind, "instance", inst.ID, "err", err)
		return
	}

	inserted := 0
	for _, it := range items {
		mp, ok := matchMapping(it.TagIDs, mapByTagID)
		if !ok {
			continue
		}
		inserted += w.reconcileParent(ctx, cfg, inst, client, it, mp)
	}
	if inserted > 0 {
		slog.Info("reconcile: enqueued", "kind", inst.Kind, "instance", inst.ID, "jobs", inserted)
	}
}

func matchMapping(tagIDs []int64, mapByTagID map[int64]store.TagMappingRow) (store.TagMappingRow, bool) {
	for _, tid := range tagIDs {
		if mp, ok := mapByTagID[tid]; ok {
			return mp, true
		}
	}
	return store.TagMappingRow{}, false
}

func (w *Worker) reconcileParent(ctx context.Context, cfg store.AppSettings, inst store.ArrInstanceRow, client *arr.Client, it arr.LibraryItem, mp store.TagMappingRow) int {
	files, err := client.Files(ctx, it.ID)
	if err != nil {
		slog.Warn("reconcile: files", "kind", inst.Kind, "parent", it.ID, "err", err)
		return 0
	}

	tagsJSON, _ := json.Marshal([]string{mp.TagLabel})
	var imports []arr.ImportEvent
	importsLoaded := false
	inserted := 0
	for _, f := range files {
		clean, err := sanitizeMediaPath(f.Path)
		if err != nil {
			continue
		}
		if cfg.OutputSuffixEnabled && sidecarExists(clean, cfg.OutputSuffix) {
			continue
		}
		has, err := w.store.HasJobForItem(ctx, inst.Kind, inst.ID, f.ID)
		if err != nil {
			slog.Error("reconcile: dedup check", "err", err)
			continue
		}
		if has {
			continue
		}

		if !importsLoaded {
			imports, err = client.ImportHistory(ctx, it.ID)
			if err != nil {
				slog.Warn("reconcile: import history lookup failed; falling back to hardlink wait",
					"kind", inst.Kind, "parent", it.ID, "err", err)
			}
			importsLoaded = true
		}

		downloadID := arr.MatchImportDownloadID(imports, arr.Kind(inst.Kind), clean, f.RelativePath)
		status := string(StatusWaitingForHardlink)
		if downloadID != "" {
			status = string(StatusWaitingForSeed)
		}
		jr := store.JobRow{
			ArrKind:       inst.Kind,
			ArrInstanceID: inst.ID,
			ArrItemID:     f.ID,
			ArrParentID:   it.ID,
			Title:         it.Title,
			FilePath:      clean,
			FileSize:      f.Size,
			DownloadID:    downloadID,
			ProfileID:     sql.NullInt64{Int64: mp.ProfileID, Valid: true},
			Status:        status,
			Tags:          string(tagsJSON),
			Source:        "poll",
		}
		if _, err := w.store.InsertJob(ctx, jr); err != nil {
			slog.Error("reconcile: insert job", "path", clean, "err", err)
			continue
		}
		inserted++
	}
	return inserted
}

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

func sidecarExists(mediaPath, suffix string) bool {
	if suffix == "" {
		return false
	}
	dir := filepath.Dir(mediaPath)
	base := filepath.Base(mediaPath)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	_, err := os.Stat(filepath.Join(dir, stem+"."+suffix))
	return err == nil
}
