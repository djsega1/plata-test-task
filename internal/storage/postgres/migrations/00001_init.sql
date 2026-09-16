-- +goose Up

CREATE TABLE quotes (
    id          BIGSERIAL      PRIMARY KEY,
    base        CHAR(3)        NOT NULL,
    quote       CHAR(3)        NOT NULL,
    rate        NUMERIC(24,10) NOT NULL,
    provider    TEXT           NOT NULL,
    quality     TEXT           NOT NULL,
    derived     BOOLEAN        NOT NULL DEFAULT false,
    indicative  BOOLEAN        NOT NULL DEFAULT true,
    quoted_at   TIMESTAMPTZ    NOT NULL,
    fetched_at  TIMESTAMPTZ    NOT NULL DEFAULT now(),
    stale_after TIMESTAMPTZ    NOT NULL
);
CREATE UNIQUE INDEX quotes_dedup_idx  ON quotes (provider, base, quote, quoted_at);
CREATE INDEX        quotes_latest_idx ON quotes (base, quote, quoted_at DESC, id DESC);

CREATE TABLE quote_updates (
    id              UUID PRIMARY KEY DEFAULT uuidv7(),
    base            CHAR(3)     NOT NULL,
    quote           CHAR(3)     NOT NULL,
    status          TEXT        NOT NULL,
    idempotency_key TEXT,
    attempts        INT         NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    locked_at       TIMESTAMPTZ,
    quote_id        BIGINT      REFERENCES quotes (id),
    error_code      TEXT,
    error_message   TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX quote_updates_idem_idx  ON quote_updates (idempotency_key)
    WHERE idempotency_key IS NOT NULL;
CREATE INDEX        quote_updates_queue_idx ON quote_updates (next_attempt_at)
    WHERE status = 'pending';
CREATE INDEX        quote_updates_stuck_idx ON quote_updates (locked_at)
    WHERE status = 'in_progress';

-- +goose Down

DROP TABLE quote_updates;
DROP TABLE quotes;
