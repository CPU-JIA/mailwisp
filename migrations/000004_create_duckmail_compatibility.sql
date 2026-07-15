-- +goose Up
ALTER TABLE messages
    ADD COLUMN seen_at timestamptz;

CREATE TABLE duckmail_credentials (
    inbox_id uuid PRIMARY KEY REFERENCES inboxes(id) ON DELETE CASCADE,
    password_hash text NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT duckmail_credentials_password_hash_valid CHECK (
        password_hash ~ '^\$argon2id\$v=19\$m=[0-9]+,t=[0-9]+,p=[0-9]+\$[A-Za-z0-9+/]+\$[A-Za-z0-9+/]+$'
        AND octet_length(password_hash) <= 512
    )
);

-- +goose Down
DROP TABLE IF EXISTS duckmail_credentials;

ALTER TABLE messages
    DROP COLUMN seen_at;
