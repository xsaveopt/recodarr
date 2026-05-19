-- +goose Up
-- +goose StatementBegin
ALTER TABLE profiles ADD COLUMN skip_bitrate_unit TEXT NOT NULL DEFAULT 'mb_per_hour';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE profiles DROP COLUMN skip_bitrate_unit;
-- +goose StatementEnd
