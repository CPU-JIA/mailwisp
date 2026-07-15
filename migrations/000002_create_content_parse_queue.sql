-- +goose Up
ALTER TABLE messages
    DROP CONSTRAINT messages_parse_status_valid,
    DROP COLUMN parse_status;

ALTER TABLE mail_contents
    ADD COLUMN parse_status text NOT NULL DEFAULT 'pending',
    ADD COLUMN parse_attempts integer NOT NULL DEFAULT 0,
    ADD COLUMN parse_available_at timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN parse_lease_token uuid,
    ADD COLUMN parse_lease_until timestamptz,
    ADD COLUMN parse_error_code text,
    ADD COLUMN parse_updated_at timestamptz NOT NULL DEFAULT now(),
    ADD CONSTRAINT mail_contents_parse_status_valid
        CHECK (parse_status IN ('pending', 'processing', 'parsed', 'failed')),
    ADD CONSTRAINT mail_contents_parse_attempts_valid
        CHECK (parse_attempts >= 0),
    ADD CONSTRAINT mail_contents_parse_lease_valid
        CHECK (
            (parse_status = 'processing' AND parse_lease_token IS NOT NULL AND parse_lease_until IS NOT NULL)
            OR
            (parse_status <> 'processing' AND parse_lease_token IS NULL AND parse_lease_until IS NULL)
        ),
    ADD CONSTRAINT mail_contents_parse_error_code_valid
        CHECK (
            parse_error_code IS NULL
            OR parse_error_code ~ '^[a-z][a-z0-9_]{0,63}$'
        ),
    ADD CONSTRAINT mail_contents_parse_terminal_state_valid
        CHECK (
            (parse_status = 'parsed' AND parse_error_code IS NULL)
            OR (parse_status = 'failed' AND parse_error_code IS NOT NULL)
            OR parse_status IN ('pending', 'processing')
        );

CREATE INDEX mail_contents_parse_queue_idx
    ON mail_contents (parse_available_at, created_at, content_key)
    WHERE parse_status IN ('pending', 'processing');

CREATE TABLE mail_content_parses (
    content_key text PRIMARY KEY REFERENCES mail_contents(content_key) ON DELETE CASCADE,
    parser_revision integer NOT NULL,
    subject text NOT NULL,
    header_message_id text NOT NULL,
    from_addresses jsonb NOT NULL,
    to_addresses jsonb NOT NULL,
    cc_addresses jsonb NOT NULL,
    sent_at timestamptz,
    text_body text NOT NULL,
    html_source text NOT NULL,
    attachments jsonb NOT NULL,
    warnings jsonb NOT NULL,
    parsed_at timestamptz NOT NULL,
    CONSTRAINT mail_content_parses_revision_valid CHECK (parser_revision > 0),
    CONSTRAINT mail_content_parses_subject_size CHECK (octet_length(subject) <= 998),
    CONSTRAINT mail_content_parses_message_id_size CHECK (octet_length(header_message_id) <= 998),
    CONSTRAINT mail_content_parses_text_size CHECK (octet_length(text_body) <= 524288),
    CONSTRAINT mail_content_parses_html_size CHECK (octet_length(html_source) <= 1048576),
    CONSTRAINT mail_content_parses_from_valid CHECK (
        jsonb_typeof(from_addresses) = 'array'
        AND jsonb_array_length(from_addresses) <= 100
        AND octet_length(from_addresses::text) <= 131072
    ),
    CONSTRAINT mail_content_parses_to_valid CHECK (
        jsonb_typeof(to_addresses) = 'array'
        AND jsonb_array_length(to_addresses) <= 100
        AND octet_length(to_addresses::text) <= 131072
    ),
    CONSTRAINT mail_content_parses_cc_valid CHECK (
        jsonb_typeof(cc_addresses) = 'array'
        AND jsonb_array_length(cc_addresses) <= 100
        AND octet_length(cc_addresses::text) <= 131072
    ),
    CONSTRAINT mail_content_parses_attachments_valid CHECK (
        jsonb_typeof(attachments) = 'array'
        AND jsonb_array_length(attachments) <= 100
        AND octet_length(attachments::text) <= 262144
    ),
    CONSTRAINT mail_content_parses_warnings_valid CHECK (
        jsonb_typeof(warnings) = 'array'
        AND jsonb_array_length(warnings) <= 100
        AND octet_length(warnings::text) <= 65536
    )
);

-- +goose Down
DROP TABLE IF EXISTS mail_content_parses;
DROP INDEX IF EXISTS mail_contents_parse_queue_idx;

ALTER TABLE mail_contents
    DROP CONSTRAINT mail_contents_parse_terminal_state_valid,
    DROP CONSTRAINT mail_contents_parse_error_code_valid,
    DROP CONSTRAINT mail_contents_parse_lease_valid,
    DROP CONSTRAINT mail_contents_parse_attempts_valid,
    DROP CONSTRAINT mail_contents_parse_status_valid,
    DROP COLUMN parse_updated_at,
    DROP COLUMN parse_error_code,
    DROP COLUMN parse_lease_until,
    DROP COLUMN parse_lease_token,
    DROP COLUMN parse_available_at,
    DROP COLUMN parse_attempts,
    DROP COLUMN parse_status;

ALTER TABLE messages
    ADD COLUMN parse_status text NOT NULL DEFAULT 'pending',
    ADD CONSTRAINT messages_parse_status_valid
        CHECK (parse_status IN ('pending', 'parsed', 'failed'));
