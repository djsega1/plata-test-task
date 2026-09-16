-- +goose Up

CREATE INDEX quote_updates_quote_id_idx ON quote_updates (quote_id);

-- +goose Down

DROP INDEX quote_updates_quote_id_idx;
