package store

import (
	"context"
	"strconv"
	"strings"
)

// AppSettings is the typed view of the key/value `settings` table. All consumers should
// load this struct via LoadAppSettings instead of calling GetSetting with magic key strings,
// so that key names live in exactly one place.
type AppSettings struct {
	WorkerIntervalSeconds int    // default 30; clamped to >= 5 at load time
	MaxParallelEncodes    int    // default 1; clamped to 1..16
	EncodingWindowStart   string // "HH:MM" or "" (no window)
	EncodingWindowEnd     string // "HH:MM" or ""
	EncodingPaused        bool   // master kill-switch; jobs keep queueing but worker won't encode
	OutputSuffixEnabled   bool   // when on, encoded files are renamed to include OutputSuffix
	OutputSuffix          string // bare token, no leading dot; inserted as `<base>.<suffix><ext>`
	NotifyURL             string
	NotifyOnDone          bool // default true
	NotifyOnFail          bool // default true
	NotifyOnHealth        bool // default true; fire on new and resolved health issues

	// LogAppLevel is the slog level for the human-facing stdout log (what
	// `docker logs` shows). Stored as the level's uppercase string:
	// "DEBUG", "INFO", "WARN", "ERROR". Changes take effect immediately.
	LogAppLevel string
	// LogMaxSizeMB, LogMaxAgeDays, LogMaxBackups, LogCompress control the
	// rotation policy of the file sinks (access.log / outbound.log /
	// handbrake.log under <data-dir>/logs/). Changes take effect on restart.
	LogMaxSizeMB  int
	LogMaxAgeDays int
	LogMaxBackups int
	LogCompress   bool
}

// settings table keys — the only place these magic strings should appear.
const (
	keyWorkerIntervalSeconds = "worker_interval_seconds"
	keyMaxParallelEncodes    = "max_parallel_encodes"
	keyEncodingWindowStart   = "encoding_window_start"
	keyEncodingWindowEnd     = "encoding_window_end"
	keyEncodingPaused        = "encoding_paused"
	keyOutputSuffixEnabled   = "output_suffix_enabled"
	keyOutputSuffix          = "output_suffix"
	keyNotifyURL             = "notify_url"
	keyNotifyOnDone          = "notify_on_done"
	keyNotifyOnFail          = "notify_on_fail"
	keyNotifyOnHealth        = "notify_on_health"

	keyLogAppLevel   = "log_app_level"
	keyLogMaxSizeMB  = "log_max_size_mb"
	keyLogMaxAgeDays = "log_max_age_days"
	keyLogMaxBackups = "log_max_backups"
	keyLogCompress   = "log_compress"
)

// MaxParallelEncodesCap is the absolute hard limit on concurrent encodes,
// regardless of user setting. Prevents pathological values from exhausting
// file descriptors / RAM.
const MaxParallelEncodesCap = 16

// LoadAppSettings reads the entire settings table and decodes it into AppSettings, applying
// defaults for any missing keys.
func (s *Store) LoadAppSettings(ctx context.Context) (AppSettings, error) {
	cfg := AppSettings{
		WorkerIntervalSeconds: 30,
		MaxParallelEncodes:    1,
		NotifyOnDone:          true,
		NotifyOnFail:          true,
		NotifyOnHealth:        true,
		LogAppLevel:           "INFO",
		LogMaxSizeMB:          50,
		LogMaxAgeDays:         30,
		LogMaxBackups:         5,
	}
	all, err := s.GetAllSettings(ctx)
	if err != nil {
		return cfg, err
	}
	if v, ok := all[keyWorkerIntervalSeconds]; ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 5 {
			cfg.WorkerIntervalSeconds = n
		}
	}
	if v, ok := all[keyMaxParallelEncodes]; ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			if n > MaxParallelEncodesCap {
				n = MaxParallelEncodesCap
			}
			cfg.MaxParallelEncodes = n
		}
	}
	cfg.EncodingWindowStart = all[keyEncodingWindowStart]
	cfg.EncodingWindowEnd = all[keyEncodingWindowEnd]
	cfg.EncodingPaused = all[keyEncodingPaused] == "true"
	cfg.OutputSuffixEnabled = all[keyOutputSuffixEnabled] == "true"
	cfg.OutputSuffix = strings.TrimSpace(all[keyOutputSuffix])
	if cfg.OutputSuffix == "" {
		cfg.OutputSuffix = "recodarr"
	}
	cfg.NotifyURL = all[keyNotifyURL]
	if v, ok := all[keyNotifyOnDone]; ok {
		cfg.NotifyOnDone = v == "true"
	}
	if v, ok := all[keyNotifyOnFail]; ok {
		cfg.NotifyOnFail = v == "true"
	}
	if v, ok := all[keyNotifyOnHealth]; ok {
		cfg.NotifyOnHealth = v == "true"
	}
	if v, ok := all[keyLogAppLevel]; ok {
		switch strings.ToUpper(strings.TrimSpace(v)) {
		case "DEBUG", "INFO", "WARN", "ERROR":
			cfg.LogAppLevel = strings.ToUpper(strings.TrimSpace(v))
		}
	}
	if v, ok := all[keyLogMaxSizeMB]; ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			cfg.LogMaxSizeMB = n
		}
	}
	if v, ok := all[keyLogMaxAgeDays]; ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.LogMaxAgeDays = n
		}
	}
	if v, ok := all[keyLogMaxBackups]; ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.LogMaxBackups = n
		}
	}
	if v, ok := all[keyLogCompress]; ok {
		cfg.LogCompress = v == "true"
	}
	return cfg, nil
}
