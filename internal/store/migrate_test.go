package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

const legacySchema = `
CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE arr_instances (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	kind TEXT NOT NULL CHECK (kind IN ('sonarr','radarr')),
	name TEXT NOT NULL,
	url TEXT NOT NULL,
	api_key TEXT NOT NULL,
	enabled INTEGER NOT NULL DEFAULT 1,
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE qbit_instances (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	url TEXT NOT NULL,
	username TEXT NOT NULL,
	password TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE profiles (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL UNIQUE,
	extra_args TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE tag_mappings (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	arr_kind TEXT NOT NULL CHECK (arr_kind IN ('sonarr','radarr','both')),
	tag_id INTEGER NOT NULL,
	profile_id INTEGER NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
	UNIQUE(arr_kind, tag_id)
);
CREATE TABLE jobs (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	arr_kind TEXT NOT NULL,
	arr_instance_id INTEGER NOT NULL,
	arr_item_id INTEGER NOT NULL,
	title TEXT NOT NULL,
	file_path TEXT NOT NULL,
	file_size INTEGER NOT NULL,
	download_id TEXT NOT NULL,
	profile_id INTEGER,
	status TEXT NOT NULL DEFAULT 'waiting_for_seed',
	error TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	started_at TIMESTAMP,
	finished_at TIMESTAMP
);
CREATE TABLE admin_users (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	username TEXT NOT NULL UNIQUE,
	password_hash TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE sessions (
	token TEXT PRIMARY KEY,
	user_id INTEGER NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	expires_at TIMESTAMP NOT NULL
);
`

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "recodarr.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func gooseVersion(t *testing.T, st *Store) int64 {
	t.Helper()
	var v sql.NullInt64
	if err := st.DB.QueryRow(`SELECT MAX(version_id) FROM goose_db_version`).Scan(&v); err != nil {
		t.Fatalf("read goose version: %v", err)
	}
	return v.Int64
}

func columns(t *testing.T, st *Store, table string) map[string]struct{} {
	t.Helper()
	cols, err := st.tableColumns(table)
	if err != nil {
		t.Fatalf("columns of %s: %v", table, err)
	}
	return cols
}

func TestOpenMigratesAFreshDatabase(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	for _, table := range []string{"settings", "arr_instances", "qbit_instances", "profiles", "tag_mappings", "jobs", "admin_users", "sessions", "goose_db_version"} {
		ok, err := st.tableExists(table)
		if err != nil || !ok {
			t.Fatalf("table %s missing after migrate (err %v)", table, err)
		}
	}
	if got := gooseVersion(t, st); got != 8 {
		t.Fatalf("goose version %d, want the latest migration (8)", got)
	}

	jobCols := columns(t, st, "jobs")
	for _, c := range []string{"tags", "source", "attempts", "encode_log", "refresh_error", "original_size", "final_size", "arr_parent_id"} {
		if _, ok := jobCols[c]; !ok {
			t.Fatalf("jobs.%s missing", c)
		}
	}
	profileCols := columns(t, st, "profiles")
	for _, c := range []string{"rate_control", "video_bitrate", "skip_bitrate_unit", "audio_bitrates_by_channels", "deleted_at"} {
		if _, ok := profileCols[c]; !ok {
			t.Fatalf("profiles.%s missing", c)
		}
	}
	if _, ok := columns(t, st, "arr_instances")["webhook_secret"]; ok {
		t.Fatal("arr_instances.webhook_secret should have been dropped by migration 008")
	}

	profiles, err := st.ListProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != len(defaultProfiles()) {
		t.Fatalf("got %d seeded profiles, want %d", len(profiles), len(defaultProfiles()))
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recodarr.db")
	first, err := Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	seeded, err := first.ListProfiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = second.Close() }()

	again, err := second.ListProfiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != len(seeded) {
		t.Fatalf("got %d profiles after reopening, want %d (defaults must not be re-seeded)", len(again), len(seeded))
	}
	if got := gooseVersion(t, second); got != 8 {
		t.Fatalf("goose version %d after reopening, want 8", got)
	}
}

func TestOpenAdoptsALegacyDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(legacySchema); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO profiles (name, extra_args) VALUES ('legacy profile', '--encopts x=1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO arr_instances (kind,name,url,api_key,enabled) VALUES ('sonarr','old','http://sonarr:8989','key',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO jobs (arr_kind,arr_instance_id,arr_item_id,title,file_path,file_size,download_id,profile_id,status)
		VALUES ('sonarr',1,7,'Old Show','/media/old.mkv',123,'HASH',1,'done')`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	if got := gooseVersion(t, st); got != 8 {
		t.Fatalf("goose version %d, want 8 after adoption", got)
	}
	for _, c := range []string{"attempts", "encode_log", "refresh_error", "arr_parent_id", "tags", "source", "original_size", "final_size"} {
		if _, ok := columns(t, st, "jobs")[c]; !ok {
			t.Fatalf("jobs.%s missing after adoption", c)
		}
	}
	for _, c := range []string{"encoder", "quality", "rate_control", "skip_bitrate_unit", "audio_bitrates_by_channels", "bloat_policy", "deleted_at"} {
		if _, ok := columns(t, st, "profiles")[c]; !ok {
			t.Fatalf("profiles.%s missing after adoption", c)
		}
	}
	if _, ok := columns(t, st, "tag_mappings")["tag_label"]; !ok {
		t.Fatal("tag_mappings.tag_label missing after adoption")
	}
	if _, ok := columns(t, st, "arr_instances")["webhook_secret"]; ok {
		t.Fatal("arr_instances.webhook_secret should be dropped even on an adopted database")
	}

	profiles, err := st.ListProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 || profiles[0].Name != "legacy profile" {
		t.Fatalf("got %d profiles %v, want only the pre-existing one (no re-seed)", len(profiles), profiles)
	}
	if profiles[0].Encoder != "x265" || profiles[0].RateControl != "crf" {
		t.Fatalf("got encoder=%q rate_control=%q, want the backfilled defaults", profiles[0].Encoder, profiles[0].RateControl)
	}

	job, err := st.GetJob(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if job.Title != "Old Show" || job.Status != "done" || job.FileSize != 123 {
		t.Fatalf("legacy job was not preserved: %+v", job)
	}
	if job.Attempts != 0 || job.EncodeLog != "" {
		t.Fatalf("backfilled columns carry unexpected values: %+v", job)
	}

	instances, err := st.ListArrInstances(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 1 || instances[0].APIKey != "key" {
		t.Fatalf("legacy arr instance was not preserved: %+v", instances)
	}
}

func TestAdoptLegacyDatabaseLeavesAFreshOneAlone(t *testing.T) {
	st := openTestStore(t)
	before := gooseVersion(t, st)
	if err := st.adoptLegacyDB(); err != nil {
		t.Fatalf("adopt on an already-managed db: %v", err)
	}
	if got := gooseVersion(t, st); got != before {
		t.Fatalf("goose version moved from %d to %d", before, got)
	}
}

func TestTableExistsAndColumns(t *testing.T) {
	st := openTestStore(t)
	ok, err := st.tableExists("definitely_not_a_table")
	if err != nil || ok {
		t.Fatalf("got %v (err %v), want false", ok, err)
	}
	cols, err := st.tableColumns("settings")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cols["key"]; !ok {
		t.Fatalf("got %v, want a key column", cols)
	}
}
