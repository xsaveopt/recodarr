package store

import (
	"context"
	"strconv"
	"strings"
)

type AppSettings struct {
	WorkerIntervalSeconds int
	MaxParallelEncodes    int
	EncodingWindowStart   string
	EncodingWindowEnd     string
	EncodingPaused        bool
	OutputSuffixEnabled   bool
	OutputSuffix          string
	NotifyURL             string
	NotifyOnDone          bool
	NotifyOnFail          bool
	NotifyOnHealth        bool

	LogAppLevel string

	LogRotateEnabled bool
	LogMaxSizeMB     int
	LogMaxAgeDays    int
	LogMaxBackups    int
	LogCompress      bool

	AgentEnabled       bool
	AgentURL           string
	AgentToken         string
	AgentFallbackLocal bool
}

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

	keyLogAppLevel      = "log_app_level"
	keyLogRotateEnabled = "log_rotate_enabled"
	keyLogMaxSizeMB     = "log_max_size_mb"
	keyLogMaxAgeDays    = "log_max_age_days"
	keyLogMaxBackups    = "log_max_backups"
	keyLogCompress      = "log_compress"

	keyAgentEnabled       = "agent_enabled"
	keyAgentURL           = "agent_url"
	keyAgentToken         = "agent_token"
	keyAgentFallbackLocal = "agent_fallback_local"
)

const MaxParallelEncodesCap = 16

func (s *Store) LoadAppSettings(ctx context.Context) (AppSettings, error) {
	cfg := AppSettings{
		WorkerIntervalSeconds: 30,
		MaxParallelEncodes:    1,
		NotifyOnDone:          true,
		NotifyOnFail:          true,
		NotifyOnHealth:        true,
		LogAppLevel:           "INFO",
		LogRotateEnabled:      true,
		LogMaxSizeMB:          50,
		LogMaxAgeDays:         30,
		LogMaxBackups:         5,
		AgentFallbackLocal:    true,
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
	if v, ok := all[keyLogRotateEnabled]; ok {
		cfg.LogRotateEnabled = v == "true"
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
	if v, ok := all[keyAgentEnabled]; ok {
		cfg.AgentEnabled = v == "true"
	}
	cfg.AgentURL = strings.TrimSpace(all[keyAgentURL])
	cfg.AgentToken = all[keyAgentToken]
	if v, ok := all[keyAgentFallbackLocal]; ok {
		cfg.AgentFallbackLocal = v == "true"
	}
	return cfg, nil
}
