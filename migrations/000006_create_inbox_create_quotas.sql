-- +goose Up
CREATE TABLE inbox_create_quotas (
    identity_digest bytea NOT NULL,
    bucket_date date NOT NULL,
    used integer NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (identity_digest, bucket_date),
    CONSTRAINT inbox_create_quotas_digest_length CHECK (octet_length(identity_digest) = 32),
    CONSTRAINT inbox_create_quotas_used_positive CHECK (used > 0)
);

CREATE INDEX inbox_create_quotas_bucket_date_idx
    ON inbox_create_quotas (bucket_date, identity_digest);

-- +goose Down
DROP TABLE IF EXISTS inbox_create_quotas;
