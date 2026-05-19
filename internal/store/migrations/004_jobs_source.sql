-- +goose Up
-- +goose StatementBegin
ALTER TABLE jobs ADD COLUMN source TEXT NOT NULL DEFAULT 'webhook';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE jobs DROP COLUMN source;
-- +goose StatementEnd
