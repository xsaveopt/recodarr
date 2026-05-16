package store

import (
	"database/sql"
	"embed"
	"fmt"
	"log/slog"

	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const migrationsDir = "migrations"

// migrate brings the database schema up to date.
//
// Schema is managed by goose with embedded .sql files in migrations/. For new
// installs goose creates everything from 001_init.sql. For databases that
// pre-date the migration system, adoptLegacyDB reconciles any missing columns
// added since their initial CREATE TABLE and stamps the DB at version 1 so
// goose takes over cleanly from there.
//
// To add a schema change: drop a new migrations/NNN_<slug>.sql file with
// +goose Up / +goose Down blocks. Numbering must be strictly increasing.
func (s *Store) migrate() error {
	goose.SetBaseFS(migrationsFS)
	goose.SetLogger(gooseLogger{})
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("goose dialect: %w", err)
	}

	if err := s.adoptLegacyDB(); err != nil {
		return fmt.Errorf("adopt legacy db: %w", err)
	}

	if err := goose.Up(s.DB, migrationsDir); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}
	return nil
}

// adoptLegacyDB handles databases created before goose was introduced. If our
// tables exist but goose's version table does not, we reconcile any missing
// columns (the columns added between the original schema and migration v1)
// and then stamp the DB at version 1 so goose.Up becomes a no-op for v1 and
// future migrations apply normally.
func (s *Store) adoptLegacyDB() error {
	hasGoose, err := s.tableExists("goose_db_version")
	if err != nil {
		return err
	}
	if hasGoose {
		return nil
	}
	hasLegacy, err := s.tableExists("arr_instances")
	if err != nil {
		return err
	}
	if !hasLegacy {
		// Fresh install — let goose.Up build everything.
		return nil
	}

	slog.Info("legacy db detected, reconciling schema before adopting goose")
	if err := s.addMissingColumns(); err != nil {
		return err
	}
	// Create the goose version table and mark v1 applied. goose creates this
	// table on first Up; we pre-create it so we can insert a stamp row.
	if _, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS goose_db_version (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		version_id INTEGER NOT NULL,
		is_applied INTEGER NOT NULL,
		tstamp TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("create goose_db_version: %w", err)
	}
	if _, err := s.DB.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (0, 1), (1, 1)`); err != nil {
		return fmt.Errorf("stamp goose version: %w", err)
	}
	return nil
}

// addMissingColumns reconciles legacy databases to the v1 schema. It exists
// only for adopting installs that pre-date goose; once a DB is stamped, all
// schema evolution happens via migration files.
//
// Only additive: never drops or renames. Type/default must match
// migrations/001_init.sql.
func (s *Store) addMissingColumns() error {
	type col struct{ name, ddl string }
	tables := map[string][]col{
		"arr_instances": {
			{"webhook_secret", "TEXT NOT NULL DEFAULT ''"},
		},
		"profiles": {
			{"encoder", "TEXT NOT NULL DEFAULT 'x265'"},
			{"encoder_preset", "TEXT NOT NULL DEFAULT 'medium'"},
			{"encoder_profile", "TEXT NOT NULL DEFAULT ''"},
			{"encoder_tune", "TEXT NOT NULL DEFAULT ''"},
			{"encoder_level", "TEXT NOT NULL DEFAULT ''"},
			{"quality", "INTEGER NOT NULL DEFAULT 22"},
			{"max_width", "INTEGER NOT NULL DEFAULT 0"},
			{"max_height", "INTEGER NOT NULL DEFAULT 0"},
			{"subtitle_copy", "INTEGER NOT NULL DEFAULT 0"},
			{"two_pass", "INTEGER NOT NULL DEFAULT 0"},
			{"container_format", "TEXT NOT NULL DEFAULT 'mkv'"},
			{"framerate", "TEXT NOT NULL DEFAULT ''"},
			{"audio_encoder", "TEXT NOT NULL DEFAULT ''"},
			{"audio_bitrate", "INTEGER NOT NULL DEFAULT 0"},
			{"audio_mixdown", "TEXT NOT NULL DEFAULT ''"},
			{"skip_codecs", "TEXT NOT NULL DEFAULT ''"},
			{"skip_bitrate_mb_per_hour", "INTEGER NOT NULL DEFAULT 0"},
			{"skip_file_size_mb", "INTEGER NOT NULL DEFAULT 0"},
			{"skip_duration_minutes", "INTEGER NOT NULL DEFAULT 0"},
			{"skip_height_px", "INTEGER NOT NULL DEFAULT 0"},
			{"skip_hdr", "INTEGER NOT NULL DEFAULT 0"},
			{"bloat_policy", "TEXT NOT NULL DEFAULT 'off'"},
			{"bloat_retry_max", "INTEGER NOT NULL DEFAULT 3"},
			{"bloat_retry_step", "INTEGER NOT NULL DEFAULT 3"},
			{"bloat_min_savings_percent", "INTEGER NOT NULL DEFAULT 0"},
		},
		"jobs": {
			{"arr_parent_id", "INTEGER NOT NULL DEFAULT 0"},
			{"refresh_error", "TEXT NOT NULL DEFAULT ''"},
			{"attempts", "INTEGER NOT NULL DEFAULT 0"},
			{"encode_log", "TEXT NOT NULL DEFAULT ''"},
			{"original_size", "INTEGER"},
			{"final_size", "INTEGER"},
		},
		"tag_mappings": {
			{"tag_label", "TEXT NOT NULL DEFAULT ''"},
		},
	}
	for table, cols := range tables {
		existing, err := s.tableColumns(table)
		if err != nil {
			return fmt.Errorf("inspect %s: %w", table, err)
		}
		for _, c := range cols {
			if _, ok := existing[c.name]; ok {
				continue
			}
			stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, c.name, c.ddl)
			if _, err := s.DB.Exec(stmt); err != nil {
				return fmt.Errorf("add column %s.%s: %w", table, c.name, err)
			}
		}
	}
	return nil
}

func (s *Store) tableExists(name string) (bool, error) {
	var got string
	err := s.DB.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name,
	).Scan(&got)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) tableColumns(table string) (map[string]struct{}, error) {
	rows, err := s.DB.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]struct{}{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		out[name] = struct{}{}
	}
	return out, rows.Err()
}

// gooseLogger routes goose's chatty output through slog so it lands in the
// app log alongside everything else.
type gooseLogger struct{}

func (gooseLogger) Fatalf(format string, v ...interface{}) {
	slog.Error("goose: " + fmt.Sprintf(format, v...))
}
func (gooseLogger) Printf(format string, v ...interface{}) {
	slog.Info("goose: " + fmt.Sprintf(format, v...))
}

// Compile-time check that gooseLogger satisfies goose.Logger.
var _ goose.Logger = gooseLogger{}
