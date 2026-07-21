-- +goose Up
ALTER TABLE arr_instances DROP COLUMN webhook_secret;

-- +goose Down
ALTER TABLE arr_instances ADD COLUMN webhook_secret TEXT NOT NULL DEFAULT '';
