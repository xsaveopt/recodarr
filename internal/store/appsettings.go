package store

import (
	"context"
	"strconv"
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
	NotifyURL             string
	NotifyOnDone          bool // default true
	NotifyOnFail          bool // default true
}

// settings table keys — the only place these magic strings should appear.
const (
	keyWorkerIntervalSeconds = "worker_interval_seconds"
	keyMaxParallelEncodes    = "max_parallel_encodes"
	keyEncodingWindowStart   = "encoding_window_start"
	keyEncodingWindowEnd     = "encoding_window_end"
	keyEncodingPaused        = "encoding_paused"
	keyNotifyURL             = "notify_url"
	keyNotifyOnDone          = "notify_on_done"
	keyNotifyOnFail          = "notify_on_fail"
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
	cfg.NotifyURL = all[keyNotifyURL]
	if v, ok := all[keyNotifyOnDone]; ok {
		cfg.NotifyOnDone = v == "true"
	}
	if v, ok := all[keyNotifyOnFail]; ok {
		cfg.NotifyOnFail = v == "true"
	}
	return cfg, nil
}
