-- +goose Up
CREATE TABLE cloudflare_temp_inbox_ids (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    inbox_id uuid NOT NULL UNIQUE REFERENCES inboxes(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT cloudflare_temp_inbox_ids_positive CHECK (id > 0)
);

CREATE TABLE cloudflare_temp_message_ids (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    inbox_id uuid NOT NULL REFERENCES inboxes(id) ON DELETE CASCADE,
    message_id uuid NOT NULL UNIQUE REFERENCES messages(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT cloudflare_temp_message_ids_positive CHECK (id > 0)
);

CREATE INDEX cloudflare_temp_message_ids_inbox_idx
    ON cloudflare_temp_message_ids (inbox_id);

-- +goose Down
DROP TABLE IF EXISTS cloudflare_temp_message_ids;
DROP TABLE IF EXISTS cloudflare_temp_inbox_ids;
