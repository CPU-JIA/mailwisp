-- +goose Up
CREATE TABLE inboxes (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    address text NOT NULL UNIQUE,
    status text NOT NULL DEFAULT 'active',
    expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT inboxes_address_canonical CHECK (
        address = lower(btrim(address))
        AND octet_length(address) BETWEEN 3 AND 320
    ),
    CONSTRAINT inboxes_status_valid CHECK (status IN ('active', 'expired', 'disabled'))
);

CREATE TABLE mail_contents (
    content_key text PRIMARY KEY,
    size_bytes bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT mail_contents_key_valid CHECK (content_key ~ '^sha256/[0-9a-f]{64}$'),
    CONSTRAINT mail_contents_size_valid CHECK (size_bytes >= 0)
);

CREATE TABLE messages (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    inbox_id uuid NOT NULL REFERENCES inboxes(id) ON DELETE CASCADE,
    content_key text NOT NULL REFERENCES mail_contents(content_key) ON DELETE RESTRICT,
    envelope_sender text NOT NULL,
    received_at timestamptz NOT NULL,
    parse_status text NOT NULL DEFAULT 'pending',
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT messages_envelope_sender_size CHECK (octet_length(envelope_sender) <= 320),
    CONSTRAINT messages_parse_status_valid CHECK (parse_status IN ('pending', 'parsed', 'failed'))
);

CREATE INDEX messages_inbox_received_idx
    ON messages (inbox_id, received_at DESC, id DESC);

CREATE INDEX messages_content_key_idx
    ON messages (content_key);

-- +goose Down
DROP TABLE IF EXISTS messages;
DROP TABLE IF EXISTS mail_contents;
DROP TABLE IF EXISTS inboxes;
