// Package migrations exposes immutable SQL migrations to the application binary.
package migrations

import "embed"

// FS contains all versioned SQL migrations.
//
//go:embed *.sql
var FS embed.FS
