package store

import (
	"context"
	cryptoRand "crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
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
	// RateControl is either "crf" (constant quality, uses Quality) or "abr"
	// (average bitrate, uses VideoBitrate). Empty defaults to "crf".
	RateControl     string
	Quality         int
	VideoBitrate    int // kbps; only meaningful when RateControl="abr"
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
	// Pre-encode filters; zero/empty = filter inactive.
	SkipCodecs            string // comma-separated, lowercase, e.g. "av1,hevc"
	SkipBitrateMBPerHour  int
	SkipFileSizeMB        int
	SkipDurationMinutes   int
	SkipHeightPx          int
	SkipHDR               bool
	// Post-encode size guard.
	BloatPolicy             string // "off" | "keep_original" | "retry_higher_crf"
	BloatRetryMax           int
	BloatRetryStep          int
	BloatMinSavingsPercent  int
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
	// Tags is a JSON-encoded []string of the *arr tag labels that were on
	// the item when the webhook fired. Used by the worker to re-resolve the
	// profile against the current tag→profile mappings, so mapping edits
	// take effect on queued jobs.
	Tags string
}

type JobStatsRow struct {
	WaitingForSeed  int64
	Ready           int64
	Encoding        int64
	Done            int64
	Failed          int64
	Skipped         int64
	TotalSavedBytes int64
}

const jobCols = `id,arr_kind,arr_instance_id,arr_item_id,arr_parent_id,title,file_path,file_size,download_id,profile_id,status,error,encode_log,refresh_error,attempts,created_at,updated_at,started_at,finished_at,original_size,final_size,tags`

func scanJob(scan func(...any) error) (JobRow, error) {
	var r JobRow
	err := scan(&r.ID, &r.ArrKind, &r.ArrInstanceID, &r.ArrItemID, &r.ArrParentID, &r.Title, &r.FilePath, &r.FileSize, &r.DownloadID, &r.ProfileID, &r.Status, &r.Error, &r.EncodeLog, &r.RefreshError, &r.Attempts, &r.CreatedAt, &r.UpdatedAt, &r.StartedAt, &r.FinishedAt, &r.OriginalSize, &r.FinalSize, &r.Tags)
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

const profileCols = `id,name,encoder,encoder_preset,encoder_profile,encoder_tune,encoder_level,rate_control,quality,video_bitrate,max_width,max_height,subtitle_copy,two_pass,container_format,extra_args,framerate,audio_encoder,audio_bitrate,audio_mixdown,skip_codecs,skip_bitrate_mb_per_hour,skip_file_size_mb,skip_duration_minutes,skip_height_px,skip_hdr,bloat_policy,bloat_retry_max,bloat_retry_step,bloat_min_savings_percent`

func scanProfile(scan func(...any) error) (ProfileRow, error) {
	var r ProfileRow
	var subtitleCopy, twoPass, skipHDR int
	err := scan(&r.ID, &r.Name, &r.Encoder, &r.EncoderPreset, &r.EncoderProfile, &r.EncoderTune, &r.EncoderLevel, &r.RateControl, &r.Quality, &r.VideoBitrate, &r.MaxWidth, &r.MaxHeight, &subtitleCopy, &twoPass, &r.ContainerFormat, &r.ExtraArgs, &r.Framerate, &r.AudioEncoder, &r.AudioBitrate, &r.AudioMixdown,
		&r.SkipCodecs, &r.SkipBitrateMBPerHour, &r.SkipFileSizeMB, &r.SkipDurationMinutes, &r.SkipHeightPx, &skipHDR,
		&r.BloatPolicy, &r.BloatRetryMax, &r.BloatRetryStep, &r.BloatMinSavingsPercent)
	r.SubtitleCopy = subtitleCopy != 0
	r.TwoPass = twoPass != 0
	r.SkipHDR = skipHDR != 0
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
	if r.RateControl == "" {
		r.RateControl = "crf"
	}
	if r.ID == 0 {
		res, err := s.DB.ExecContext(ctx,
			`INSERT INTO profiles (name,encoder,encoder_preset,encoder_profile,encoder_tune,encoder_level,rate_control,quality,video_bitrate,max_width,max_height,subtitle_copy,two_pass,container_format,extra_args,framerate,audio_encoder,audio_bitrate,audio_mixdown,skip_codecs,skip_bitrate_mb_per_hour,skip_file_size_mb,skip_duration_minutes,skip_height_px,skip_hdr,bloat_policy,bloat_retry_max,bloat_retry_step,bloat_min_savings_percent)
			 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			r.Name, r.Encoder, r.EncoderPreset, r.EncoderProfile, r.EncoderTune, r.EncoderLevel, r.RateControl, r.Quality, r.VideoBitrate, r.MaxWidth, r.MaxHeight, boolToInt(r.SubtitleCopy), boolToInt(r.TwoPass), r.ContainerFormat, r.ExtraArgs, r.Framerate, r.AudioEncoder, r.AudioBitrate, r.AudioMixdown,
			r.SkipCodecs, r.SkipBitrateMBPerHour, r.SkipFileSizeMB, r.SkipDurationMinutes, r.SkipHeightPx, boolToInt(r.SkipHDR),
			r.BloatPolicy, r.BloatRetryMax, r.BloatRetryStep, r.BloatMinSavingsPercent)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := s.DB.ExecContext(ctx,
		`UPDATE profiles SET name=?,encoder=?,encoder_preset=?,encoder_profile=?,encoder_tune=?,encoder_level=?,rate_control=?,quality=?,video_bitrate=?,max_width=?,max_height=?,subtitle_copy=?,two_pass=?,container_format=?,extra_args=?,framerate=?,audio_encoder=?,audio_bitrate=?,audio_mixdown=?,
		 skip_codecs=?,skip_bitrate_mb_per_hour=?,skip_file_size_mb=?,skip_duration_minutes=?,skip_height_px=?,skip_hdr=?,
		 bloat_policy=?,bloat_retry_max=?,bloat_retry_step=?,bloat_min_savings_percent=? WHERE id=?`,
		r.Name, r.Encoder, r.EncoderPreset, r.EncoderProfile, r.EncoderTune, r.EncoderLevel, r.RateControl, r.Quality, r.VideoBitrate, r.MaxWidth, r.MaxHeight, boolToInt(r.SubtitleCopy), boolToInt(r.TwoPass), r.ContainerFormat, r.ExtraArgs, r.Framerate, r.AudioEncoder, r.AudioBitrate, r.AudioMixdown,
		r.SkipCodecs, r.SkipBitrateMBPerHour, r.SkipFileSizeMB, r.SkipDurationMinutes, r.SkipHeightPx, boolToInt(r.SkipHDR),
		r.BloatPolicy, r.BloatRetryMax, r.BloatRetryStep, r.BloatMinSavingsPercent, r.ID)
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

// JobListOptions controls filtering and pagination for ListJobs. Any field
// left at its zero value is treated as "no filter" — the canonical "show
// everything" call is ListJobs(ctx, JobListOptions{Limit: N}).
//
// Statuses/Kinds are include-lists: nil/empty means "any", otherwise the row
// must match one of the listed values. The UI uses all-selected-by-default
// multi-select so users can untick a status to hide it.
type JobListOptions struct {
	Statuses  []string
	Kinds     []string
	ProfileID int64  // exact match; 0 = any
	Search    string // case-insensitive substring match against title
	Limit     int    // 0 = default 50; capped at 500
	Offset    int
}

func (s *Store) ListJobs(ctx context.Context, opts JobListOptions) ([]JobRow, int64, error) {
	where, args := jobsFilterClause(opts)

	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	} else if limit > 500 {
		limit = 500
	}

	var total int64
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM jobs `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	pageArgs := append(append([]any{}, args...), limit, opts.Offset)
	rows, err := s.DB.QueryContext(ctx,
		`SELECT `+jobCols+` FROM jobs `+where+` ORDER BY id DESC LIMIT ? OFFSET ?`, pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]JobRow, 0, limit)
	for rows.Next() {
		r, err := scanJob(rows.Scan)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

func jobsFilterClause(opts JobListOptions) (string, []any) {
	conds := make([]string, 0, 4)
	args := make([]any, 0, 4)
	if len(opts.Statuses) > 0 {
		conds = append(conds, "status IN ("+placeholders(len(opts.Statuses))+")")
		for _, s := range opts.Statuses {
			args = append(args, s)
		}
	}
	if len(opts.Kinds) > 0 {
		conds = append(conds, "arr_kind IN ("+placeholders(len(opts.Kinds))+")")
		for _, k := range opts.Kinds {
			args = append(args, k)
		}
	}
	if opts.ProfileID > 0 {
		conds = append(conds, "profile_id = ?")
		args = append(args, opts.ProfileID)
	}
	if opts.Search != "" {
		conds = append(conds, "lower(title) LIKE ?")
		args = append(args, "%"+strings.ToLower(opts.Search)+"%")
	}
	if len(conds) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(conds, " AND "), args
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
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
			COALESCE(SUM(CASE WHEN status='skipped' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status='done' AND original_size IS NOT NULL AND final_size IS NOT NULL THEN original_size - final_size ELSE 0 END), 0)
		FROM jobs`).Scan(&r.WaitingForSeed, &r.Ready, &r.Encoding, &r.Done, &r.Failed, &r.Skipped, &r.TotalSavedBytes)
	return r, err
}

func (s *Store) HasActiveJob(ctx context.Context, arrKind string, arrInstanceID, arrItemID int64) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM jobs WHERE arr_kind=? AND arr_instance_id=? AND arr_item_id=? AND status NOT IN ('done','failed','skipped')`,
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

// RequeueEncoding moves a job from encoding back to ready and rolls back the
// attempts counter that MarkJobEncoding incremented. Used when a running encode
// is cancelled by the worker for an external reason (pause, shutdown) — the job
// shouldn't be penalized in the retry budget for a cancellation it didn't cause.
func (s *Store) RequeueEncoding(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx,
		`UPDATE jobs SET status='ready', started_at=NULL, original_size=NULL,
		 attempts = MAX(attempts - 1, 0), updated_at = CURRENT_TIMESTAMP
		 WHERE id=? AND status='encoding'`, id)
	return err
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

// MarkJobSkipped marks a job as skipped by a pre-encode filter (codec already
// efficient, bitrate too low, etc.). The reason is stored in the `error`
// column for surfacing in the UI; it isn't an error per se but the column is
// already a free-text "why this is in a terminal state" slot.
func (s *Store) MarkJobSkipped(ctx context.Context, id int64, reason string) error {
	_, err := s.DB.ExecContext(ctx,
		`UPDATE jobs SET status = 'skipped', finished_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP, error = ?, encode_log = ''
		 WHERE id = ?`, reason, id)
	return err
}

func (s *Store) MarkJobFailed(ctx context.Context, id int64, msg, encodeLog string) error {
	_, err := s.DB.ExecContext(ctx,
		`UPDATE jobs SET status = 'failed', finished_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP, error = ?, encode_log = ?
		 WHERE id = ?`, msg, encodeLog, id)
	return err
}

// RetryJob re-queues any terminal job — failed, skipped, or done. Done jobs
// are useful for testing profile changes against an already-encoded file (the
// file on disk gets re-encoded with the current profile settings). The
// sidecar marker is only consulted at webhook time, so a manual retry
// bypasses it cleanly.
func (s *Store) RetryJob(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx,
		`UPDATE jobs SET status='waiting_for_seed', error='', encode_log='', refresh_error='', attempts=0, started_at=NULL, finished_at=NULL, original_size=NULL, final_size=NULL, updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status IN ('failed','skipped','done')`,
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
	_, err := s.DB.ExecContext(ctx, `DELETE FROM jobs WHERE id=? AND status IN ('done','failed','skipped')`, id)
	return err
}

// terminalStatuses is the canonical set DeleteTerminalJobs / DeleteJobsByIDs
// will touch. Encoding jobs are never deleted here — cancel them first.
var terminalStatuses = []string{"done", "failed", "skipped", "waiting_for_seed", "ready"}

func isTerminalDeletable(s string) bool {
	for _, t := range terminalStatuses {
		if t == s {
			return true
		}
	}
	return false
}

// DeleteTerminalJobs removes jobs in any of the given statuses. When statuses
// is empty, defaults to the historical {done, failed, skipped} set so old
// callers keep working. Any status not in terminalStatuses is silently dropped
// — we never delete encoding jobs through this path.
func (s *Store) DeleteTerminalJobs(ctx context.Context, statuses []string) (int64, error) {
	if len(statuses) == 0 {
		statuses = []string{"done", "failed", "skipped"}
	}
	clean := make([]any, 0, len(statuses))
	for _, st := range statuses {
		if isTerminalDeletable(st) {
			clean = append(clean, st)
		}
	}
	if len(clean) == 0 {
		return 0, nil
	}
	q := `DELETE FROM jobs WHERE status IN (` + placeholders(len(clean)) + `)`
	res, err := s.DB.ExecContext(ctx, q, clean...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteJobsByIDs removes the listed jobs, but only those in a deletable
// (non-encoding) status. Encoding jobs are skipped silently — the count
// reflects what actually got removed.
func (s *Store) DeleteJobsByIDs(ctx context.Context, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	q := `DELETE FROM jobs WHERE id IN (` + placeholders(len(ids)) + `) AND status != 'encoding'`
	res, err := s.DB.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RetryJobsByIDs re-queues every terminal (failed/skipped/done) job in the
// list. Encoding/waiting/ready jobs are skipped silently.
func (s *Store) RetryJobsByIDs(ctx context.Context, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	q := `UPDATE jobs SET status='waiting_for_seed', error='', encode_log='', refresh_error='',
	      attempts=0, started_at=NULL, finished_at=NULL, original_size=NULL, final_size=NULL,
	      updated_at=CURRENT_TIMESTAMP
	      WHERE id IN (` + placeholders(len(ids)) + `) AND status IN ('failed','skipped','done')`
	res, err := s.DB.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
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
	tags := r.Tags
	if tags == "" {
		tags = "[]"
	}
	res, err := s.DB.ExecContext(ctx,
		`INSERT INTO jobs (arr_kind,arr_instance_id,arr_item_id,arr_parent_id,title,file_path,file_size,download_id,profile_id,status,tags) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		r.ArrKind, r.ArrInstanceID, r.ArrItemID, r.ArrParentID, r.Title, r.FilePath, r.FileSize, r.DownloadID, r.ProfileID, r.Status, tags)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateJobProfile rewrites a job's profile_id. Used by the worker to
// re-route a queued job after the operator changes tag→profile mappings.
func (s *Store) UpdateJobProfile(ctx context.Context, id int64, profileID sql.NullInt64) error {
	_, err := s.DB.ExecContext(ctx,
		`UPDATE jobs SET profile_id = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		profileID, id)
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
