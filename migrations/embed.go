// Package migrations exposes immutable SQL migrations to the application binary.
package migrations

import "embed"

// LatestVersion is the exact schema version required by the running binary.
const LatestVersion int64 = 8

// FS contains all versioned SQL migrations.
//
//go:embed *.sql
var FS embed.FS
