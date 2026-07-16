package sql

import "embed"

// FS contains the immutable marketplace schema migrations.
//
//go:embed *.sql
var FS embed.FS
