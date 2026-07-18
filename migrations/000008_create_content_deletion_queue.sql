-- +goose Up
CREATE TABLE content_deletion_queue (
    content_key text PRIMARY KEY,
    size_bytes bigint NOT NULL,
    generation bigint NOT NULL DEFAULT 1,
    enqueued_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT content_deletion_queue_key_valid CHECK (content_key ~ '^sha256/[0-9a-f]{64}$'),
    CONSTRAINT content_deletion_queue_size_valid CHECK (size_bytes >= 0),
    CONSTRAINT content_deletion_queue_generation_valid CHECK (generation > 0)
);

CREATE INDEX content_deletion_queue_order_idx
    ON content_deletion_queue (enqueued_at, content_key);

-- +goose Down
DROP TABLE IF EXISTS content_deletion_queue;
