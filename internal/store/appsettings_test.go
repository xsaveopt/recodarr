package store

import (
	"context"
	"testing"
)

func setAll(t *testing.T, st *Store, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		if err := st.SetSetting(context.Background(), k, v); err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}
}

func TestLoadAppSettingsDefaults(t *testing.T) {
	cfg, err := openTestStore(t).LoadAppSettings(context.Background())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := AppSettings{
		WorkerIntervalSeconds:    30,
		ReconcileIntervalSeconds: 300,
		MaxParallelEncodes:       1,
		OutputSuffix:             "recodarr",
		NotifyOnDone:             true,
		NotifyOnFail:             true,
		NotifyOnHealth:           true,
		LogAppLevel:              "INFO",
		LogRotateEnabled:         true,
		LogMaxSizeMB:             50,
		LogMaxAgeDays:            30,
		LogMaxBackups:            5,
		AgentFallbackLocal:       true,
	}
	if cfg != want {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
}

func TestLoadAppSettingsAppliesStoredValues(t *testing.T) {
	st := openTestStore(t)
	setAll(t, st, map[string]string{
		"worker_interval_seconds":    "10",
		"reconcile_interval_seconds": "600",
		"max_parallel_encodes":       "3",
		"encoding_window_start":      "01:00",
		"encoding_window_end":        "06:00",
		"encoding_paused":            "true",
		"output_suffix_enabled":      "true",
		"output_suffix":              "  small  ",
		"notify_url":                 "http://hook",
		"notify_on_done":             "false",
		"notify_on_fail":             "false",
		"notify_on_health":           "false",
		"log_app_level":              " debug ",
		"log_rotate_enabled":         "false",
		"log_max_size_mb":            "10",
		"log_max_age_days":           "0",
		"log_max_backups":            "0",
		"log_compress":               "true",
		"agent_enabled":              "true",
		"agent_url":                  "  http://agent:9000  ",
		"agent_token":                " tok ",
		"agent_fallback_local":       "false",
	})
	cfg, err := st.LoadAppSettings(context.Background())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := AppSettings{
		WorkerIntervalSeconds:    10,
		ReconcileIntervalSeconds: 600,
		MaxParallelEncodes:       3,
		EncodingWindowStart:      "01:00",
		EncodingWindowEnd:        "06:00",
		EncodingPaused:           true,
		OutputSuffixEnabled:      true,
		OutputSuffix:             "small",
		NotifyURL:                "http://hook",
		LogAppLevel:              "DEBUG",
		LogMaxSizeMB:             10,
		LogCompress:              true,
		AgentEnabled:             true,
		AgentURL:                 "http://agent:9000",
		AgentToken:               " tok ",
	}
	if cfg != want {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
}

func TestLoadAppSettingsIgnoresOutOfRangeValues(t *testing.T) {
	st := openTestStore(t)
	setAll(t, st, map[string]string{
		"worker_interval_seconds":    "4",
		"reconcile_interval_seconds": "59",
		"max_parallel_encodes":       "0",
		"log_app_level":              "chatty",
		"log_max_size_mb":            "0",
		"log_max_age_days":           "-1",
		"log_max_backups":            "x",
		"output_suffix":              "   ",
	})
	cfg, err := st.LoadAppSettings(context.Background())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.WorkerIntervalSeconds != 30 || cfg.ReconcileIntervalSeconds != 300 || cfg.MaxParallelEncodes != 1 {
		t.Fatalf("got %+v, want the interval and parallel defaults kept", cfg)
	}
	if cfg.LogAppLevel != "INFO" || cfg.LogMaxSizeMB != 50 || cfg.LogMaxAgeDays != 30 || cfg.LogMaxBackups != 5 {
		t.Fatalf("got %+v, want the log defaults kept", cfg)
	}
	if cfg.OutputSuffix != "recodarr" {
		t.Fatalf("got suffix %q, want the default for a blank value", cfg.OutputSuffix)
	}
}

func TestLoadAppSettingsCapsParallelEncodes(t *testing.T) {
	st := openTestStore(t)
	setAll(t, st, map[string]string{"max_parallel_encodes": "999"})
	cfg, err := st.LoadAppSettings(context.Background())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.MaxParallelEncodes != MaxParallelEncodesCap {
		t.Fatalf("got %d, want the cap %d", cfg.MaxParallelEncodes, MaxParallelEncodesCap)
	}
}

func TestLoadAppSettingsReportsAClosedDatabase(t *testing.T) {
	st := openTestStore(t)
	_ = st.Close()
	cfg, err := st.LoadAppSettings(context.Background())
	if err == nil {
		t.Fatal("loading from a closed database succeeded")
	}
	if cfg.WorkerIntervalSeconds != 30 {
		t.Fatalf("got %+v, want defaults alongside the error", cfg)
	}
}
