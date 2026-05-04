// Package migrations exposes the embedded SQL migration files.
// The dispatcher binary and integration tests both load migrations
// from this package via golang-migrate's iofs source driver.
package migrations

import "embed"

// FS holds the embedded *.sql migration files keyed by filename.
//
//go:embed *.sql
var FS embed.FS
