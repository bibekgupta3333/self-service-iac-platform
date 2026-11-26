// Package migrations embeds the goose SQL migrations so the binary carries its schema.
package migrations

import "embed"

// FS holds every NNNN_name.sql migration in this directory.
//
//go:embed *.sql
var FS embed.FS
