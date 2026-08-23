// Package migrations embeds the observability schema so obsplane (and the
// integration tests) can apply it without shipping a separate SQL bundle.
package migrations

import "embed"

// FS holds every .sql file in this directory. The migration runner applies
// them in lexical filename order, so the numeric prefix is the ordering key.
//
//go:embed *.sql
var FS embed.FS
