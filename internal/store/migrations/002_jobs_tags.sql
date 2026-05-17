-- +goose Up
-- +goose StatementBegin
ALTER TABLE jobs ADD COLUMN tags TEXT NOT NULL DEFAULT '[]';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE jobs DROP COLUMN tags;
-- +goose StatementEnd
