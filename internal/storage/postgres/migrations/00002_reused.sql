-- +goose Up

ALTER TABLE quote_updates ADD COLUMN reused BOOLEAN;

-- +goose Down

ALTER TABLE quote_updates DROP COLUMN reused;
