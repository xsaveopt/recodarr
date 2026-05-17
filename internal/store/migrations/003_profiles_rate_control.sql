-- +goose Up
-- +goose StatementBegin
ALTER TABLE profiles ADD COLUMN rate_control TEXT NOT NULL DEFAULT 'crf';
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE profiles ADD COLUMN video_bitrate INTEGER NOT NULL DEFAULT 0;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE profiles DROP COLUMN rate_control;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE profiles DROP COLUMN video_bitrate;
-- +goose StatementEnd
