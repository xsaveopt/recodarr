package store

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

type Store struct {
	DB *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	s := &Store{DB: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := s.seedDefaultProfiles(context.Background()); err != nil {
		return nil, fmt.Errorf("seed defaults: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS arr_instances (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			kind TEXT NOT NULL CHECK (kind IN ('sonarr','radarr')),
			name TEXT NOT NULL,
			url TEXT NOT NULL,
			api_key TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			webhook_secret TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS qbit_instances (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			url TEXT NOT NULL,
			username TEXT NOT NULL,
			password TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS profiles (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			extra_args TEXT NOT NULL DEFAULT '',
			encoder TEXT NOT NULL DEFAULT 'x265',
			encoder_preset TEXT NOT NULL DEFAULT 'medium',
			encoder_profile TEXT NOT NULL DEFAULT '',
			encoder_tune TEXT NOT NULL DEFAULT '',
			encoder_level TEXT NOT NULL DEFAULT '',
			quality INTEGER NOT NULL DEFAULT 22,
			max_width INTEGER NOT NULL DEFAULT 0,
			max_height INTEGER NOT NULL DEFAULT 0,
			subtitle_copy INTEGER NOT NULL DEFAULT 0,
			two_pass INTEGER NOT NULL DEFAULT 0,
			container_format TEXT NOT NULL DEFAULT 'mkv',
			framerate TEXT NOT NULL DEFAULT '',
			audio_encoder TEXT NOT NULL DEFAULT '',
			audio_bitrate INTEGER NOT NULL DEFAULT 0,
			audio_mixdown TEXT NOT NULL DEFAULT '',
			-- Pre-encode filters. All zero / empty = filter inactive. See
			-- internal/job/filters.go for evaluation order and semantics.
			skip_codecs TEXT NOT NULL DEFAULT '',         -- comma-separated codec names to skip (e.g. "av1,hevc")
			skip_bitrate_mb_per_hour INTEGER NOT NULL DEFAULT 0, -- skip if source ≤ this MB/hour
			skip_file_size_mb INTEGER NOT NULL DEFAULT 0, -- skip if source file ≤ this MB
			skip_duration_minutes INTEGER NOT NULL DEFAULT 0, -- skip if source ≤ this minutes long
			skip_height_px INTEGER NOT NULL DEFAULT 0,    -- skip if source video height ≤ this
			skip_hdr INTEGER NOT NULL DEFAULT 0,          -- skip if source has HDR transfer (PQ or HLG)
			-- Post-encode size guard. 'off' = always commit; 'keep_original' = discard
			-- the new file when it didn't shrink enough; 'retry_higher_crf' = re-encode
			-- with quality + retry_step, up to retry_max times, then fall back to keep_original.
			bloat_policy TEXT NOT NULL DEFAULT 'off',
			bloat_retry_max INTEGER NOT NULL DEFAULT 3,
			bloat_retry_step INTEGER NOT NULL DEFAULT 3,
			-- Minimum savings (% of original) required to keep a re-encode. 0 = keep
			-- anything ≤ original. 5 = require at least 5% smaller. Applied to every
			-- attempt under both keep_original and retry_higher_crf policies.
			bloat_min_savings_percent INTEGER NOT NULL DEFAULT 0,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS tag_mappings (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			arr_kind TEXT NOT NULL CHECK (arr_kind IN ('sonarr','radarr','both')),
			tag_id INTEGER NOT NULL,
			tag_label TEXT NOT NULL DEFAULT '',
			profile_id INTEGER NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
			UNIQUE(arr_kind, tag_id)
		)`,
		`CREATE TABLE IF NOT EXISTS jobs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			arr_kind TEXT NOT NULL,
			arr_instance_id INTEGER NOT NULL,
			arr_item_id INTEGER NOT NULL,
			arr_parent_id INTEGER NOT NULL DEFAULT 0,
			title TEXT NOT NULL,
			file_path TEXT NOT NULL,
			file_size INTEGER NOT NULL,
			download_id TEXT NOT NULL,
			profile_id INTEGER,
			status TEXT NOT NULL DEFAULT 'waiting_for_seed',
			error TEXT NOT NULL DEFAULT '',
			encode_log TEXT NOT NULL DEFAULT '',
			refresh_error TEXT NOT NULL DEFAULT '',
			attempts INTEGER NOT NULL DEFAULT 0,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			started_at TIMESTAMP,
			finished_at TIMESTAMP,
			original_size INTEGER,
			final_size INTEGER
		)`,
		`CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status)`,
		`CREATE INDEX IF NOT EXISTS idx_jobs_lookup ON jobs(arr_kind, arr_instance_id, arr_item_id)`,
		`CREATE TABLE IF NOT EXISTS admin_users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			token TEXT PRIMARY KEY,
			user_id INTEGER NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			expires_at TIMESTAMP NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at)`,
	}
	for _, stmt := range stmts {
		if _, err := s.DB.Exec(stmt); err != nil {
			return err
		}
	}

	return nil
}
