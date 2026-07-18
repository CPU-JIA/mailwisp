-- +goose Up
UPDATE inboxes
SET expires_at = now(),
    status = CASE WHEN status = 'active' THEN 'expired' ELSE status END,
    updated_at = now()
WHERE expires_at IS NULL;

ALTER TABLE inboxes
    ALTER COLUMN expires_at SET NOT NULL;

-- +goose Down
ALTER TABLE inboxes
    ALTER COLUMN expires_at DROP NOT NULL;
