-- +goose Up
-- +goose StatementBegin
ALTER TABLE profiles ADD COLUMN deleted_at TIMESTAMP NULL;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE arr_instances ADD COLUMN deleted_at TIMESTAMP NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE profiles DROP COLUMN deleted_at;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE arr_instances DROP COLUMN deleted_at;
-- +goose StatementEnd
