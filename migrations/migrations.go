// Package migrations embeds the forward-only SQLite schema migrations.
package migrations

import "embed"

// FS contains all numbered SQL migrations.
//
//go:embed *.sql
var FS embed.FS
