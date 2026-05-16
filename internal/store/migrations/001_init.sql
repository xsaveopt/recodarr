-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS arr_instances (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    kind TEXT NOT NULL CHECK (kind IN ('sonarr','radarr')),
    name TEXT NOT NULL,
    url TEXT NOT NULL,
    api_key TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    webhook_secret TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS qbit_instances (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    url TEXT NOT NULL,
    username TEXT NOT NULL,
    password TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS profiles (
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
    skip_codecs TEXT NOT NULL DEFAULT '',
    skip_bitrate_mb_per_hour INTEGER NOT NULL DEFAULT 0,
    skip_file_size_mb INTEGER NOT NULL DEFAULT 0,
    skip_duration_minutes INTEGER NOT NULL DEFAULT 0,
    skip_height_px INTEGER NOT NULL DEFAULT 0,
    skip_hdr INTEGER NOT NULL DEFAULT 0,
    bloat_policy TEXT NOT NULL DEFAULT 'off',
    bloat_retry_max INTEGER NOT NULL DEFAULT 3,
    bloat_retry_step INTEGER NOT NULL DEFAULT 3,
    bloat_min_savings_percent INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS tag_mappings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    arr_kind TEXT NOT NULL CHECK (arr_kind IN ('sonarr','radarr','both')),
    tag_id INTEGER NOT NULL,
    tag_label TEXT NOT NULL DEFAULT '',
    profile_id INTEGER NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    UNIQUE(arr_kind, tag_id)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS jobs (
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
);
-- +goose StatementEnd

CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status);
CREATE INDEX IF NOT EXISTS idx_jobs_lookup ON jobs(arr_kind, arr_instance_id, arr_item_id);

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS admin_users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS sessions (
    token TEXT PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at TIMESTAMP NOT NULL
);
-- +goose StatementEnd

CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);

-- +goose Down
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS admin_users;
DROP TABLE IF EXISTS jobs;
DROP TABLE IF EXISTS tag_mappings;
DROP TABLE IF EXISTS profiles;
DROP TABLE IF EXISTS qbit_instances;
DROP TABLE IF EXISTS arr_instances;
DROP TABLE IF EXISTS settings;
