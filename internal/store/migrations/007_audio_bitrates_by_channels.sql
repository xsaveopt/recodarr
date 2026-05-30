-- +goose Up
-- +goose StatementBegin
ALTER TABLE profiles ADD COLUMN audio_bitrates_by_channels TEXT NOT NULL DEFAULT '{}';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE profiles DROP COLUMN audio_bitrates_by_channels;
-- +goose StatementEnd
