package store

import (
	"context"
	cryptoRand "crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

var ErrNotFound = errors.New("not found")

type ArrInstanceRow struct {
	ID            int64
	Kind          string
	Name          string
	URL           string
	APIKey        string
	Enabled       bool
	WebhookSecret string
}

type QbitInstanceRow struct {
	ID       int64
	Name     string
	URL      string
	Username string
	Password string
}

type ProfileRow struct {
	ID              int64
	Name            string
	Encoder         string
	EncoderPreset   string
	EncoderProfile  string
	EncoderTune     string
	EncoderLevel    string
	Quality         int
	MaxWidth        int
	MaxHeight       int
	SubtitleCopy    bool
	TwoPass         bool
	ContainerFormat string
	ExtraArgs       string
	Framerate       string
	AudioEncoder    string
	AudioBitrate    int
	AudioMixdown    string
}

type TagMappingRow struct {
	ID        int64
	ArrKind   string
	TagID     int64
	TagLabel  string
	ProfileID int64
}

type JobRow struct {
	ID            int64
	ArrKind       string
	ArrInstanceID int64
	ArrItemID     int64
	ArrParentID   int64
	Title         string
	FilePath      string
	FileSize      int64
	DownloadID    string
	ProfileID     sql.NullInt64
	Status        string
	Error         string
	EncodeLog     string
	RefreshError  string
	Attempts      int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
	StartedAt     sql.NullTime
	FinishedAt    sql.NullTime
	OriginalSize  sql.NullInt64
	FinalSize     sql.NullInt64
}

type JobStatsRow struct {
	WaitingForSeed  int64
	Ready           int64
	Encoding        int64
	Done            int64
	Failed          int64
	TotalSavedBytes int64
}

const jobCols = `id,arr_kind,arr_instance_id,arr_item_id,arr_parent_id,title,file_path,file_size,download_id,profile_id,status,error,encode_log,refresh_error,attempts,created_at,updated_at,started_at,finished_at,original_size,final_size`

func scanJob(scan func(...any) error) (JobRow, error) {
	var r JobRow
	err := scan(&r.ID, &r.ArrKind, &r.ArrInstanceID, &r.ArrItemID, &r.ArrParentID, &r.Title, &r.FilePath, &r.FileSize, &r.DownloadID, &r.ProfileID, &r.Status, &r.Error, &r.EncodeLog, &r.RefreshError, &r.Attempts, &r.CreatedAt, &r.UpdatedAt, &r.StartedAt, &r.FinishedAt, &r.OriginalSize, &r.FinalSize)
	return r, err
}

// --- settings ---

func (s *Store) GetSetting(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return v, true, err
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO settings (key,value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		key, value)
	return err
}

func (s *Store) GetAllSettings(ctx context.Context) (map[string]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT key,value FROM settings ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// --- arr instances ---

func (s *Store) ListArrInstances(ctx context.Context) ([]ArrInstanceRow, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,kind,name,url,api_key,enabled,webhook_secret FROM arr_instances ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ArrInstanceRow
	for rows.Next() {
		var r ArrInstanceRow
		var enabled int
		if err := rows.Scan(&r.ID, &r.Kind, &r.Name, &r.URL, &r.APIKey, &enabled, &r.WebhookSecret); err != nil {
			return nil, err
		}
		r.Enabled = enabled != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetArrInstance(ctx context.Context, id int64) (*ArrInstanceRow, error) {
	var r ArrInstanceRow
	var enabled int
	err := s.DB.QueryRowContext(ctx,
		`SELECT id,kind,name,url,api_key,enabled,webhook_secret FROM arr_instances WHERE id = ?`, id).
		Scan(&r.ID, &r.Kind, &r.Name, &r.URL, &r.APIKey, &enabled, &r.WebhookSecret)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r.Enabled = enabled != 0
	return &r, nil
}

func (s *Store) CreateArrInstance(ctx context.Context, r ArrInstanceRow) (int64, error) {
	if r.WebhookSecret == "" {
		tok, err := newWebhookSecret()
		if err != nil {
			return 0, err
		}
		r.WebhookSecret = tok
	}
	res, err := s.DB.ExecContext(ctx,
		`INSERT INTO arr_instances (kind,name,url,api_key,enabled,webhook_secret) VALUES (?,?,?,?,?,?)`,
		r.Kind, r.Name, r.URL, r.APIKey, boolToInt(r.Enabled), r.WebhookSecret)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateArrInstance(ctx context.Context, r ArrInstanceRow) error {
	// Preserve secrets the SPA never received: blank api_key/webhook_secret on input
	// means "keep what's there", since GET intentionally redacts both.
	var existingKey, existingSecret string
	_ = s.DB.QueryRowContext(ctx,
		`SELECT api_key, webhook_secret FROM arr_instances WHERE id=?`, r.ID).
		Scan(&existingKey, &existingSecret)
	if r.APIKey == "" {
		r.APIKey = existingKey
	}
	if r.WebhookSecret == "" {
		if existingSecret != "" {
			r.WebhookSecret = existingSecret
		} else {
			tok, err := newWebhookSecret()
			if err != nil {
				return err
			}
			r.WebhookSecret = tok
		}
	}
	_, err := s.DB.ExecContext(ctx,
		`UPDATE arr_instances SET kind=?,name=?,url=?,api_key=?,enabled=?,webhook_secret=? WHERE id=?`,
		r.Kind, r.Name, r.URL, r.APIKey, boolToInt(r.Enabled), r.WebhookSecret, r.ID)
	return err
}

func newWebhookSecret() (string, error) {
	b := make([]byte, 24)
	if _, err := cryptoRand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *Store) DeleteArrInstance(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM arr_instances WHERE id=?`, id)
	return err
}

// --- qbit instance ---

func (s *Store) ListQbitInstances(ctx context.Context) ([]QbitInstanceRow, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,name,url,username,password FROM qbit_instances ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []QbitInstanceRow
	for rows.Next() {
		var r QbitInstanceRow
		if err := rows.Scan(&r.ID, &r.Name, &r.URL, &r.Username, &r.Password); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetQbitInstance(ctx context.Context, id int64) (*QbitInstanceRow, error) {
	var r QbitInstanceRow
	err := s.DB.QueryRowContext(ctx,
		`SELECT id,name,url,username,password FROM qbit_instances WHERE id=?`, id).
		Scan(&r.ID, &r.Name, &r.URL, &r.Username, &r.Password)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &r, err
}

func (s *Store) UpsertQbitInstance(ctx context.Context, r QbitInstanceRow) (int64, error) {
	if r.ID == 0 {
		res, err := s.DB.ExecContext(ctx, `INSERT INTO qbit_instances (name,url,username,password) VALUES (?,?,?,?)`, r.Name, r.URL, r.Username, r.Password)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	if r.Password == "" {
		var existing string
		if err := s.DB.QueryRowContext(ctx, `SELECT password FROM qbit_instances WHERE id=?`, r.ID).Scan(&existing); err == nil {
			r.Password = existing
		}
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE qbit_instances SET name=?,url=?,username=?,password=? WHERE id=?`, r.Name, r.URL, r.Username, r.Password, r.ID)
	return r.ID, err
}

func (s *Store) DeleteQbitInstance(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM qbit_instances WHERE id=?`, id)
	return err
}

// --- profiles ---

const profileCols = `id,name,encoder,encoder_preset,encoder_profile,encoder_tune,encoder_level,quality,max_width,max_height,subtitle_copy,two_pass,container_format,extra_args,framerate,audio_encoder,audio_bitrate,audio_mixdown`

func scanProfile(scan func(...any) error) (ProfileRow, error) {
	var r ProfileRow
	var subtitleCopy, twoPass int
	err := scan(&r.ID, &r.Name, &r.Encoder, &r.EncoderPreset, &r.EncoderProfile, &r.EncoderTune, &r.EncoderLevel, &r.Quality, &r.MaxWidth, &r.MaxHeight, &subtitleCopy, &twoPass, &r.ContainerFormat, &r.ExtraArgs, &r.Framerate, &r.AudioEncoder, &r.AudioBitrate, &r.AudioMixdown)
	r.SubtitleCopy = subtitleCopy != 0
	r.TwoPass = twoPass != 0
	return r, err
}

func (s *Store) ListProfiles(ctx context.Context) ([]ProfileRow, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+profileCols+` FROM profiles ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ProfileRow
	for rows.Next() {
		r, err := scanProfile(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetProfile(ctx context.Context, id int64) (*ProfileRow, error) {
	r, err := scanProfile(s.DB.QueryRowContext(ctx, `SELECT `+profileCols+` FROM profiles WHERE id = ?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) UpsertProfile(ctx context.Context, r ProfileRow) (int64, error) {
	if r.ID == 0 {
		res, err := s.DB.ExecContext(ctx,
			`INSERT INTO profiles (name,encoder,encoder_preset,encoder_profile,encoder_tune,encoder_level,quality,max_width,max_height,subtitle_copy,two_pass,container_format,extra_args,framerate,audio_encoder,audio_bitrate,audio_mixdown) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			r.Name, r.Encoder, r.EncoderPreset, r.EncoderProfile, r.EncoderTune, r.EncoderLevel, r.Quality, r.MaxWidth, r.MaxHeight, boolToInt(r.SubtitleCopy), boolToInt(r.TwoPass), r.ContainerFormat, r.ExtraArgs, r.Framerate, r.AudioEncoder, r.AudioBitrate, r.AudioMixdown)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := s.DB.ExecContext(ctx,
		`UPDATE profiles SET name=?,encoder=?,encoder_preset=?,encoder_profile=?,encoder_tune=?,encoder_level=?,quality=?,max_width=?,max_height=?,subtitle_copy=?,two_pass=?,container_format=?,extra_args=?,framerate=?,audio_encoder=?,audio_bitrate=?,audio_mixdown=? WHERE id=?`,
		r.Name, r.Encoder, r.EncoderPreset, r.EncoderProfile, r.EncoderTune, r.EncoderLevel, r.Quality, r.MaxWidth, r.MaxHeight, boolToInt(r.SubtitleCopy), boolToInt(r.TwoPass), r.ContainerFormat, r.ExtraArgs, r.Framerate, r.AudioEncoder, r.AudioBitrate, r.AudioMixdown, r.ID)
	return r.ID, err
}

func (s *Store) DeleteProfile(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM profiles WHERE id=?`, id)
	return err
}

// --- tag mappings ---

func (s *Store) ListTagMappings(ctx context.Context) ([]TagMappingRow, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id,arr_kind,tag_id,tag_label,profile_id FROM tag_mappings ORDER BY arr_kind,tag_label`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []TagMappingRow
	for rows.Next() {
		var r TagMappingRow
		if err := rows.Scan(&r.ID, &r.ArrKind, &r.TagID, &r.TagLabel, &r.ProfileID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListTagMappingsByKind returns mappings that apply to the given kind or to 'both'.
func (s *Store) ListTagMappingsByKind(ctx context.Context, kind string) ([]TagMappingRow, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id,arr_kind,tag_id,tag_label,profile_id FROM tag_mappings WHERE arr_kind = ? OR arr_kind = 'both' ORDER BY id`,
		kind)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []TagMappingRow
	for rows.Next() {
		var r TagMappingRow
		if err := rows.Scan(&r.ID, &r.ArrKind, &r.TagID, &r.TagLabel, &r.ProfileID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) CreateTagMapping(ctx context.Context, r TagMappingRow) (int64, error) {
	res, err := s.DB.ExecContext(ctx,
		`INSERT INTO tag_mappings (arr_kind,tag_id,tag_label,profile_id) VALUES (?,?,?,?)`,
		r.ArrKind, r.TagID, r.TagLabel, r.ProfileID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) DeleteTagMapping(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM tag_mappings WHERE id=?`, id)
	return err
}

// --- jobs ---

func (s *Store) ListJobs(ctx context.Context) ([]JobRow, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+jobCols+` FROM jobs ORDER BY id DESC LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []JobRow
	for rows.Next() {
		r, err := scanJob(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetJob(ctx context.Context, id int64) (*JobRow, error) {
	r, err := scanJob(s.DB.QueryRowContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE id=?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) GetJobStats(ctx context.Context) (JobStatsRow, error) {
	var r JobStatsRow
	err := s.DB.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN status='waiting_for_seed' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status='ready' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status='encoding' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status='done' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status='done' AND original_size IS NOT NULL AND final_size IS NOT NULL THEN original_size - final_size ELSE 0 END), 0)
		FROM jobs`).Scan(&r.WaitingForSeed, &r.Ready, &r.Encoding, &r.Done, &r.Failed, &r.TotalSavedBytes)
	return r, err
}

func (s *Store) HasActiveJob(ctx context.Context, arrKind string, arrInstanceID, arrItemID int64) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM jobs WHERE arr_kind=? AND arr_instance_id=? AND arr_item_id=? AND status NOT IN ('done','failed')`,
		arrKind, arrInstanceID, arrItemID).Scan(&n)
	return n > 0, err
}

func (s *Store) JobsByStatus(ctx context.Context, status string, limit int) ([]JobRow, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT `+jobCols+` FROM jobs WHERE status = ? ORDER BY id ASC LIMIT ?`, status, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []JobRow
	for rows.Next() {
		r, err := scanJob(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecoverOrphanEncoding resets jobs stuck in 'encoding' back to 'ready' and clears their
// started_at. Called on startup so a crash/SIGKILL during encode doesn't permanently wedge a job.
// Returns the list of file paths that were stuck so the caller can clean up *.recodarr.tmp* files.
func (s *Store) RecoverOrphanEncoding(ctx context.Context) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT file_path FROM jobs WHERE status = 'encoding'`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	if len(paths) == 0 {
		return nil, rows.Err()
	}
	_, err = s.DB.ExecContext(ctx,
		`UPDATE jobs SET status = 'ready', started_at = NULL, updated_at = CURRENT_TIMESTAMP WHERE status = 'encoding'`)
	return paths, err
}

func (s *Store) TransitionJobStatus(ctx context.Context, id int64, from, to string) (bool, error) {
	res, err := s.DB.ExecContext(ctx,
		`UPDATE jobs SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND status = ?`,
		to, id, from)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// MaxJobAttempts caps how many times a job can be claimed for encoding before
// MarkJobEncoding refuses. Prevents a job that crashes the binary from looping forever.
const MaxJobAttempts = 5

func (s *Store) MarkJobEncoding(ctx context.Context, id int64) (bool, error) {
	res, err := s.DB.ExecContext(ctx,
		`UPDATE jobs SET status = 'encoding', started_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP, original_size = file_size, attempts = attempts + 1
		 WHERE id = ? AND status = 'ready' AND attempts < ?`, id, MaxJobAttempts)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// SetRefreshError records why the post-encode *arr refresh failed (or clears it).
func (s *Store) SetRefreshError(ctx context.Context, id int64, msg string) error {
	_, err := s.DB.ExecContext(ctx,
		`UPDATE jobs SET refresh_error = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		msg, id)
	return err
}

func (s *Store) MarkJobDone(ctx context.Context, id int64, finalSize int64) error {
	_, err := s.DB.ExecContext(ctx,
		`UPDATE jobs SET status = 'done', final_size = ?, finished_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP, error = ''
		 WHERE id = ?`, finalSize, id)
	return err
}

func (s *Store) MarkJobFailed(ctx context.Context, id int64, msg, encodeLog string) error {
	_, err := s.DB.ExecContext(ctx,
		`UPDATE jobs SET status = 'failed', finished_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP, error = ?, encode_log = ?
		 WHERE id = ?`, msg, encodeLog, id)
	return err
}

func (s *Store) RetryJob(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx,
		`UPDATE jobs SET status='waiting_for_seed', error='', encode_log='', refresh_error='', attempts=0, started_at=NULL, finished_at=NULL, original_size=NULL, final_size=NULL, updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status='failed'`,
		id)
	return err
}

func (s *Store) RetryAllFailed(ctx context.Context) (int64, error) {
	res, err := s.DB.ExecContext(ctx,
		`UPDATE jobs SET status='waiting_for_seed', error='', encode_log='', refresh_error='', attempts=0, started_at=NULL, finished_at=NULL, original_size=NULL, final_size=NULL, updated_at=CURRENT_TIMESTAMP
		 WHERE status='failed'`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) DeleteJob(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM jobs WHERE id=? AND status IN ('done','failed')`, id)
	return err
}

func (s *Store) DeleteTerminalJobs(ctx context.Context) (int64, error) {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM jobs WHERE status IN ('done','failed')`)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return n, err
}

func (s *Store) FirstQbitInstance(ctx context.Context) (*QbitInstanceRow, error) {
	var r QbitInstanceRow
	err := s.DB.QueryRowContext(ctx,
		`SELECT id,name,url,username,password FROM qbit_instances ORDER BY id LIMIT 1`).
		Scan(&r.ID, &r.Name, &r.URL, &r.Username, &r.Password)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) InsertJob(ctx context.Context, r JobRow) (int64, error) {
	res, err := s.DB.ExecContext(ctx,
		`INSERT INTO jobs (arr_kind,arr_instance_id,arr_item_id,arr_parent_id,title,file_path,file_size,download_id,profile_id,status) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		r.ArrKind, r.ArrInstanceID, r.ArrItemID, r.ArrParentID, r.Title, r.FilePath, r.FileSize, r.DownloadID, r.ProfileID, r.Status)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
