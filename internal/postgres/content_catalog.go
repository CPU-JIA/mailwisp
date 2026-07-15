package postgres

import (
	"context"
	"errors"
	"fmt"

	"mailwisp/internal/message"
)

// WalkContentRefs visits PostgreSQL content metadata in canonical key order
// using keyset pagination so memory use remains bounded.
func (r *DeliveryRepository) WalkContentRefs(
	ctx context.Context,
	batchSize int,
	visit func(message.ContentRef) error,
) error {
	if batchSize <= 0 || batchSize > 10_000 {
		return errors.New("content catalog batch size must be between 1 and 10000")
	}
	if visit == nil {
		return errors.New("content catalog visitor is required")
	}

	after := ""
	for {
		rows, err := r.pool.Query(ctx, `
			SELECT content_key, size_bytes
			FROM mail_contents
			WHERE content_key > $1
			ORDER BY content_key
			LIMIT $2
		`, after, batchSize)
		if err != nil {
			return fmt.Errorf("query content metadata page: %w", err)
		}

		page := make([]message.ContentRef, 0, batchSize)
		for rows.Next() {
			var ref message.ContentRef
			if err := rows.Scan(&ref.Key, &ref.SizeBytes); err != nil {
				rows.Close()
				return fmt.Errorf("scan content metadata: %w", err)
			}
			page = append(page, ref)
		}
		rowsErr := rows.Err()
		rows.Close()
		if rowsErr != nil {
			return fmt.Errorf("iterate content metadata: %w", rowsErr)
		}
		for _, ref := range page {
			if err := visit(ref); err != nil {
				return err
			}
		}
		if len(page) < batchSize {
			return nil
		}
		after = page[len(page)-1].Key
	}
}

// ExistingContentKeys returns the subset of keys currently represented in
// PostgreSQL metadata.
func (r *DeliveryRepository) ExistingContentKeys(ctx context.Context, keys []string) (map[string]struct{}, error) {
	if len(keys) == 0 {
		return map[string]struct{}{}, nil
	}
	if len(keys) > 10_000 {
		return nil, errors.New("content key batch must not exceed 10000")
	}
	rows, err := r.pool.Query(ctx, `
		SELECT content_key
		FROM mail_contents
		WHERE content_key = ANY($1::text[])
	`, keys)
	if err != nil {
		return nil, fmt.Errorf("query existing content keys: %w", err)
	}
	defer rows.Close()

	existing := make(map[string]struct{}, len(keys))
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("scan existing content key: %w", err)
		}
		existing[key] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate existing content keys: %w", err)
	}
	return existing, nil
}
