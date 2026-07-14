-- +goose Up
CREATE TABLE inbox_capabilities (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    inbox_id uuid NOT NULL REFERENCES inboxes(id) ON DELETE CASCADE,
    kid text NOT NULL UNIQUE,
    secret_digest bytea NOT NULL,
    scope_mask integer NOT NULL,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    last_used_at timestamptz,
    revoked_at timestamptz,
    rotated_from_id uuid UNIQUE REFERENCES inbox_capabilities(id) ON DELETE SET NULL,
    CONSTRAINT inbox_capabilities_kid_valid CHECK (kid ~ '^[0-9a-f]{24}$'),
    CONSTRAINT inbox_capabilities_digest_valid CHECK (octet_length(secret_digest) = 32),
    CONSTRAINT inbox_capabilities_scope_valid CHECK (scope_mask > 0 AND (scope_mask & ~31) = 0),
    CONSTRAINT inbox_capabilities_lifetime_valid CHECK (expires_at > created_at),
    CONSTRAINT inbox_capabilities_last_used_valid CHECK (last_used_at IS NULL OR last_used_at >= created_at),
    CONSTRAINT inbox_capabilities_revoked_valid CHECK (revoked_at IS NULL OR revoked_at >= created_at),
    CONSTRAINT inbox_capabilities_rotation_valid CHECK (rotated_from_id IS NULL OR rotated_from_id <> id)
);

CREATE INDEX inbox_capabilities_inbox_idx
    ON inbox_capabilities (inbox_id, created_at DESC, id DESC);

-- +goose Down
DROP TABLE IF EXISTS inbox_capabilities;
